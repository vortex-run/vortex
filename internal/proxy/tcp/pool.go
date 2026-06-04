package tcp

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Pool defaults.
const (
	defaultPoolMaxIdle     = 8
	defaultPoolMaxOpen     = 64
	defaultPoolIdleTimeout = 90 * time.Second
	defaultPoolDialTimeout = 10 * time.Second
)

// ErrPoolClosed is returned by Get after the pool has been closed.
var ErrPoolClosed = errors.New("tcp pool: closed")

// PoolConfig tunes a connection Pool.
type PoolConfig struct {
	// MaxIdle is the maximum idle (kept-warm) connections retained per backend
	// address. Default 8.
	MaxIdle int
	// MaxOpen is the maximum total connections (idle + borrowed) per backend
	// address. Get blocks once this is reached until a Put or ctx cancel.
	// Default 64.
	MaxOpen int
	// IdleTimeout closes idle connections older than this. Default 90s.
	IdleTimeout time.Duration
	// DialTimeout bounds dialing a new connection. Default 10s.
	DialTimeout time.Duration
}

func (c PoolConfig) withDefaults() PoolConfig {
	if c.MaxIdle <= 0 {
		c.MaxIdle = defaultPoolMaxIdle
	}
	if c.MaxOpen <= 0 {
		c.MaxOpen = defaultPoolMaxOpen
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = defaultPoolIdleTimeout
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = defaultPoolDialTimeout
	}
	return c
}

// idleConn is a pooled connection with the time it became idle.
type idleConn struct {
	conn   net.Conn
	idleAt time.Time
}

// addrPool holds per-backend-address state: the idle list and a semaphore
// bounding total open connections.
type addrPool struct {
	mu   sync.Mutex
	idle []idleConn
	// sem is a counting semaphore with MaxOpen tokens; acquiring a token
	// represents an open connection (idle or borrowed). An idle connection
	// keeps its token while parked, so idle+borrowed never exceeds MaxOpen.
	sem chan struct{}
	// notify is signalled (non-blocking) whenever a connection is returned to
	// the idle list, so a Get blocked on MaxOpen can wake and reuse it.
	notify chan struct{}
}

// signal wakes one waiter without blocking if none are waiting.
func (ap *addrPool) signal() {
	select {
	case ap.notify <- struct{}{}:
	default:
	}
}

// PoolStats is a point-in-time snapshot of pool counters.
type PoolStats struct {
	Idle      int
	Active    int   // connections currently borrowed via Get (not yet Put back)
	WaitCount int64 // cumulative times Get blocked waiting on MaxOpen
}

// Pool is a per-backend-address connection pool. The zero value is not usable;
// construct one with NewPool. It is safe for concurrent use.
type Pool struct {
	cfg PoolConfig

	mu     sync.Mutex
	pools  map[string]*addrPool
	closed bool

	active    atomic.Int64
	idleCount atomic.Int64
	waitCount atomic.Int64

	dialer *net.Dialer
}

// NewPool constructs a Pool with cfg (zero fields take defaults).
func NewPool(cfg PoolConfig) *Pool {
	cfg = cfg.withDefaults()
	return &Pool{
		cfg:    cfg,
		pools:  make(map[string]*addrPool),
		dialer: &net.Dialer{Timeout: cfg.DialTimeout},
	}
}

// poolFor returns (creating if needed) the addrPool for a backend address.
func (p *Pool) poolFor(addr string) (*addrPool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrPoolClosed
	}
	ap := p.pools[addr]
	if ap == nil {
		ap = &addrPool{
			sem:    make(chan struct{}, p.cfg.MaxOpen),
			notify: make(chan struct{}, 1),
		}
		p.pools[addr] = ap
	}
	return ap, nil
}

// Get returns a connection to addr: a healthy idle one if available, otherwise
// a freshly dialed one. If MaxOpen connections are already open to addr, Get
// blocks until a Put frees a slot or ctx is cancelled.
func (p *Pool) Get(ctx context.Context, network, addr string) (net.Conn, error) {
	ap, err := p.poolFor(addr)
	if err != nil {
		return nil, err
	}

	waited := false
	for {
		// 1. Reuse a healthy idle connection (it already holds a token).
		for {
			c := ap.popIdle()
			if c == nil {
				break
			}
			p.idleCount.Add(-1)
			if connHealthy(c) {
				p.active.Add(1)
				return c, nil
			}
			_ = c.Close() // dead idle conn: drop it and release its token
			<-ap.sem
		}

		// 2. Try to acquire a token to dial a fresh connection.
		select {
		case ap.sem <- struct{}{}:
			conn, derr := p.dialer.DialContext(ctx, network, addr)
			if derr != nil {
				<-ap.sem // release the token we reserved
				return nil, derr
			}
			p.active.Add(1)
			return conn, nil
		default:
		}

		// 3. At MaxOpen with no idle conn: block until a Put frees/returns one
		//    or ctx is cancelled, then retry.
		if !waited {
			p.waitCount.Add(1)
			waited = true
		}
		select {
		case <-ap.notify:
			// A connection was returned or a slot freed — loop and retry.
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Put returns conn (previously obtained from Get for addr) to the pool. If the
// idle pool is under MaxIdle and the connection is healthy it is retained;
// otherwise it is closed. Either way the open-connection slot accounting is
// updated so a blocked Get can proceed.
func (p *Pool) Put(conn net.Conn, addr string) {
	if conn == nil {
		return
	}
	p.active.Add(-1)

	p.mu.Lock()
	closed := p.closed
	ap := p.pools[addr]
	p.mu.Unlock()

	if closed || ap == nil {
		_ = conn.Close()
		return
	}

	if !connHealthy(conn) {
		_ = conn.Close()
		<-ap.sem    // free the slot
		ap.signal() // a waiter can now acquire the freed token
		return
	}

	ap.mu.Lock()
	if len(ap.idle) >= p.cfg.MaxIdle {
		ap.mu.Unlock()
		_ = conn.Close()
		<-ap.sem    // over idle cap: free the slot
		ap.signal() // a waiter can now acquire the freed token
		return
	}
	ap.idle = append(ap.idle, idleConn{conn: conn, idleAt: time.Now()})
	ap.mu.Unlock()
	p.idleCount.Add(1)
	// The connection keeps its semaphore token while idle (still counts toward
	// MaxOpen); a waiting Get reuses it via popIdle once woken.
	ap.signal()
}

// popIdle removes and returns the most-recently-added idle connection (LIFO,
// which keeps the warmest connection in use), or nil if the idle list is empty.
// Liveness — including connections the OS closed after IdleTimeout — is checked
// by the caller via connHealthy, which discards and releases dead connections.
func (ap *addrPool) popIdle() net.Conn {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if n := len(ap.idle); n > 0 {
		c := ap.idle[n-1].conn
		ap.idle = ap.idle[:n-1]
		return c
	}
	return nil
}

// Close closes all idle connections and marks the pool closed. Subsequent Get
// calls return ErrPoolClosed. Borrowed (active) connections are the caller's
// responsibility to close.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	pools := p.pools
	p.pools = make(map[string]*addrPool)
	p.mu.Unlock()

	for _, ap := range pools {
		ap.mu.Lock()
		for _, ic := range ap.idle {
			_ = ic.conn.Close()
			p.idleCount.Add(-1)
		}
		ap.idle = nil
		ap.mu.Unlock()
	}
	return nil
}

// Stats returns a snapshot of pool counters.
func (p *Pool) Stats() PoolStats {
	return PoolStats{
		Idle:      int(p.idleCount.Load()),
		Active:    int(p.active.Load()),
		WaitCount: p.waitCount.Load(),
	}
}

// connHealthy probes whether conn is still usable: a zero-effective read with a
// 1ms deadline. A timeout means the peer sent nothing but the socket is alive
// (healthy); io.EOF or another error means the peer closed or the socket is
// broken (unhealthy). The read deadline is cleared before returning so the
// caller gets a clean connection.
func connHealthy(conn net.Conn) bool {
	if err := conn.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
		return false
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	var b [1]byte
	_, err := conn.Read(b[:])
	if err == nil {
		// Unexpected: peer sent a byte while idle in the pool. Treat as
		// unhealthy since we have now consumed and dropped that byte.
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true // alive, just no data
	}
	return false // EOF or hard error → dead
}
