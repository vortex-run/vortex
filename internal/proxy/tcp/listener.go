package tcp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// drainTimeout bounds how long Listen waits for active tunnels to finish after
// the context is cancelled before returning anyway.
const drainTimeout = 30 * time.Second

// ListenerConfig configures a TCP tunnel Listener.
type ListenerConfig struct {
	// ListenAddr is the local bind address, e.g. ":5432".
	ListenAddr string
	// Backends are the upstream targets (weighted).
	Backends []BackendAddr
	// Pool supplies backend connections. Required.
	Pool *Pool
	// Tunnel configures each bidirectional copy.
	Tunnel TunnelConfig
	// MaxConnections caps concurrent tunnels; 0 means unlimited.
	MaxConnections int
	// Logger receives tunnel/accept diagnostics; defaults to slog.Default.
	Logger *slog.Logger
}

// ListenerStats is a point-in-time snapshot of listener counters.
type ListenerStats struct {
	Active   int64
	Total    int64
	Rejected int64
	BytesIn  int64 // stub — wired in M5 observability
	BytesOut int64 // stub — wired in M5 observability
}

// Listener accepts client connections and tunnels each to a selected backend.
type Listener struct {
	cfg ListenerConfig
	log *slog.Logger
	rr  *WeightedRR
	wg  sync.WaitGroup // tracks active tunnel goroutines

	active   atomic.Int64
	total    atomic.Int64
	rejected atomic.Int64
}

// NewListener validates cfg and constructs a Listener.
func NewListener(cfg ListenerConfig) (*Listener, error) {
	if cfg.ListenAddr == "" {
		return nil, errors.New("tcp listener: ListenAddr is required")
	}
	if len(cfg.Backends) == 0 {
		return nil, errors.New("tcp listener: at least one backend is required")
	}
	if cfg.Pool == nil {
		return nil, errors.New("tcp listener: Pool is required")
	}
	rr, err := NewWeightedRR(cfg.Backends)
	if err != nil {
		return nil, err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Listener{cfg: cfg, log: log, rr: rr}, nil
}

// Listen binds the listener and serves until ctx is cancelled. On cancel it
// stops accepting, waits up to drainTimeout for active tunnels, then returns
// nil. It returns an error only on an unexpected bind/accept failure.
func (l *Listener) Listen(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", l.cfg.ListenAddr)
	if err != nil {
		return err
	}

	// Close the listener when ctx is cancelled to unblock Accept.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	l.log.Info("tcp route started",
		"listen", l.cfg.ListenAddr,
		"backends", len(l.cfg.Backends),
		"max_conns", l.cfg.MaxConnections,
	)

	for {
		conn, aerr := ln.Accept()
		if aerr != nil {
			if ctx.Err() != nil {
				break // expected: listener closed on cancel
			}
			// Transient accept error: log and keep serving.
			l.log.Error("tcp accept error", "err", aerr)
			continue
		}
		l.handleAccept(ctx, conn)
	}

	l.drain()
	l.log.Info("tcp route stopped",
		"listen", l.cfg.ListenAddr,
		"total_conns", l.total.Load(),
	)
	return nil
}

// handleAccept enforces MaxConnections, selects a backend, borrows a pooled
// connection, and dispatches a tunnel goroutine.
func (l *Listener) handleAccept(ctx context.Context, client net.Conn) {
	if l.cfg.MaxConnections > 0 && l.active.Load() >= int64(l.cfg.MaxConnections) {
		l.rejected.Add(1)
		_ = client.Close()
		return
	}
	l.total.Add(1)

	backend, err := l.rr.Next()
	if err != nil {
		l.log.Error("tcp backend selection failed", "err", err)
		_ = client.Close()
		return
	}

	backendConn, err := l.cfg.Pool.Get(ctx, "tcp", backend.Addr)
	if err != nil {
		l.log.Error("tcp backend dial failed", "backend", backend.Addr, "err", err)
		_ = client.Close()
		return
	}

	l.active.Add(1)
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		defer l.active.Add(-1)
		defer l.cfg.Pool.Put(backendConn, backend.Addr)
		defer func() { _ = client.Close() }()

		if terr := Tunnel(ctx, client, backendConn, l.cfg.Tunnel); terr != nil {
			if !errors.Is(terr, io.EOF) && !errors.Is(terr, context.Canceled) {
				l.log.Debug("tcp tunnel ended", "backend", backend.Addr, "err", terr)
			}
		}
	}()
}

// drain waits for active tunnel goroutines to finish, bounded by drainTimeout.
func (l *Listener) drain() {
	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(drainTimeout):
		l.log.Warn("tcp drain timeout; abandoning active tunnels",
			"active", l.active.Load(), "timeout", drainTimeout.String())
	}
}

// Stats returns a snapshot of the listener's counters.
func (l *Listener) Stats() ListenerStats {
	return ListenerStats{
		Active:   l.active.Load(),
		Total:    l.total.Load(),
		Rejected: l.rejected.Load(),
	}
}

// UpdateBackends atomically replaces the backend set for zero-downtime config
// reload. Existing tunnels are unaffected; new connections use the new set.
func (l *Listener) UpdateBackends(backends []BackendAddr) error {
	return l.rr.Update(backends)
}
