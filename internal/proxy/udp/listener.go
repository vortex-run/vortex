package proxyudp

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// UDP listener defaults.
const (
	defaultMaxSessions = 10000
	defaultBufferSize  = 64 * 1024 // max UDP datagram
)

// UDPListenerConfig configures a UDPListener.
type UDPListenerConfig struct {
	// ListenAddr is the local UDP bind address, e.g. ":53".
	ListenAddr string
	// BackendAddr is the upstream UDP target, "host:port".
	BackendAddr string
	// MaxSessions caps concurrent client sessions. Default 10000.
	MaxSessions int
	// RateLimit is sustained packets/sec per source IP; 0 disables limiting.
	RateLimit int
	// RateBurst is the burst size; defaults to RateLimit*2 when unset.
	RateBurst int
	// SessionTTL is the idle timeout before a session is reaped. Default 30s.
	SessionTTL time.Duration
	// BufferSize is the per-read datagram buffer. Default 64KiB.
	BufferSize int
	// Logger receives diagnostics; defaults to slog.Default.
	Logger *slog.Logger
}

// UDPStats is a snapshot of UDP listener counters.
type UDPStats struct {
	Sessions SessionStats
	Dropped  int64 // rate-limited + max-sessions drops
}

// UDPListener forwards UDP datagrams to a backend, tracking per-client sessions
// and rate-limiting by source IP.
type UDPListener struct {
	cfg     UDPListenerConfig
	log     *slog.Logger
	table   *SessionTable
	limiter *RateLimiter

	bufSize int
	dropped atomic.Int64

	conn      *net.UDPConn
	boundAddr atomic.Pointer[string] // race-free bound address for callers/tests
	wg        sync.WaitGroup
}

// LocalAddr returns the bound UDP address once Listen has bound the socket, or
// "" before then. Safe for concurrent use.
func (l *UDPListener) LocalAddr() string {
	if p := l.boundAddr.Load(); p != nil {
		return *p
	}
	return ""
}

// NewListener validates cfg and constructs a UDPListener.
func NewListener(cfg UDPListenerConfig) (*UDPListener, error) {
	if cfg.ListenAddr == "" {
		return nil, errors.New("proxyudp: ListenAddr is required")
	}
	if cfg.BackendAddr == "" {
		return nil, errors.New("proxyudp: BackendAddr is required")
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = defaultMaxSessions
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = defaultBufferSize
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	l := &UDPListener{
		cfg:     cfg,
		log:     cfg.Logger,
		table:   NewSessionTable(cfg.SessionTTL),
		bufSize: cfg.BufferSize,
	}

	if cfg.RateLimit > 0 {
		burst := cfg.RateBurst
		if burst <= 0 {
			burst = cfg.RateLimit * 2
		}
		rl, err := NewRateLimiter(cfg.RateLimit, burst)
		if err != nil {
			return nil, err
		}
		l.limiter = rl
	}
	return l, nil
}

// Listen binds the UDP socket and forwards datagrams until ctx is cancelled.
// It starts the session and rate-limiter cleanup goroutines, runs the read
// loop, and on cancellation closes the socket and all sessions. It returns nil
// on clean shutdown.
func (l *UDPListener) Listen(ctx context.Context) error {
	udpAddr, err := net.ResolveUDPAddr("udp", l.cfg.ListenAddr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	l.conn = conn
	bound := conn.LocalAddr().String()
	l.boundAddr.Store(&bound)

	l.table.StartCleanup(ctx)
	if l.limiter != nil {
		l.limiter.StartCleanup(ctx)
	}

	// Close the socket on cancel to unblock ReadFromUDP.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	l.log.Info("udp route started",
		"listen", l.cfg.ListenAddr,
		"backend", l.cfg.BackendAddr,
		"max_sessions", l.cfg.MaxSessions,
	)

	buf := make([]byte, l.bufSize)
	for {
		n, clientAddr, rerr := conn.ReadFromUDP(buf)
		if rerr != nil {
			if ctx.Err() != nil {
				break // expected on shutdown
			}
			l.log.Error("udp read error", "err", rerr)
			continue
		}
		l.handlePacket(ctx, conn, clientAddr, buf[:n])
	}

	// Close all sessions so reply pumps blocked on backend reads unblock, then
	// wait for them to drain.
	l.table.CloseAll()
	l.wg.Wait()
	l.log.Info("udp route stopped", "listen", l.cfg.ListenAddr)
	return nil
}

// handlePacket applies rate limiting and session caps, forwards the datagram to
// the backend, and starts a reply pump for new sessions.
func (l *UDPListener) handlePacket(ctx context.Context, main *net.UDPConn, clientAddr *net.UDPAddr, data []byte) {
	if l.limiter != nil && !l.limiter.Allow(clientAddr.IP.String()) {
		l.dropped.Add(1)
		return
	}

	key := clientAddr.String()
	// Enforce MaxSessions only for brand-new clients.
	if _, exists := l.table.sessions.Load(key); !exists {
		if l.table.ActiveCount() >= l.cfg.MaxSessions {
			l.dropped.Add(1)
			return
		}
	}

	session, created, err := l.table.GetOrCreate(clientAddr, l.cfg.BackendAddr)
	if err != nil {
		l.log.Error("udp session create failed", "client", key, "err", err)
		l.dropped.Add(1)
		return
	}

	if created {
		l.wg.Add(1)
		go l.replyPump(ctx, main, clientAddr, session)
	}

	if _, werr := session.BackendConn.Write(data); werr != nil {
		l.log.Debug("udp backend write failed", "client", key, "err", werr)
		return
	}
	session.BytesIn.Add(int64(len(data)))
	l.table.Touch(key)
}

// replyPump reads datagrams from the backend connection and writes them back to
// the client via the main socket. It exits when the backend connection is
// closed (session reaped) or the context is cancelled.
func (l *UDPListener) replyPump(ctx context.Context, main *net.UDPConn, clientAddr *net.UDPAddr, session *Session) {
	defer l.wg.Done()
	buf := make([]byte, l.bufSize)
	for {
		n, err := session.BackendConn.Read(buf)
		if err != nil {
			return // backend conn closed (TTL reap) or read error
		}
		if _, werr := main.WriteToUDP(buf[:n], clientAddr); werr != nil {
			if ctx.Err() != nil {
				return
			}
			l.log.Debug("udp client write failed", "client", clientAddr.String(), "err", werr)
			return
		}
		session.BytesOut.Add(int64(n))
		l.table.Touch(clientAddr.String())
	}
}

// Stats returns a snapshot of the listener's counters.
func (l *UDPListener) Stats() UDPStats {
	return UDPStats{
		Sessions: l.table.Stats(),
		Dropped:  l.dropped.Load(),
	}
}
