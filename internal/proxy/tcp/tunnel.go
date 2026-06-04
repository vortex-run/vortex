// Package tcp implements VORTEX's raw TCP tunnel engine (build plan M2.1): a
// bidirectional byte pump between a client connection and a backend connection,
// a per-backend connection pool, a weighted round-robin selector, and an accept
// loop that wires them together. It is the data plane for `protocol: "tcp"`
// routes. Standard library only (Non-Negotiable Rule #10).
package tcp

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Defaults for TunnelConfig fields left zero.
const (
	defaultDialTimeout = 10 * time.Second
	defaultIdleTimeout = 90 * time.Second
	defaultMaxConns    = 1000
	defaultBufferSize  = 32 * 1024
)

// ErrIdleTimeout is returned when a tunnel is closed because no bytes flowed in
// either direction within the configured IdleTimeout.
var ErrIdleTimeout = errors.New("tcp tunnel: idle timeout")

// TunnelConfig tunes a single tunnel.
type TunnelConfig struct {
	// DialTimeout bounds backend dialing (used by the pool/listener, kept here
	// so the whole tunnel behavior is configured in one place). Default 10s.
	DialTimeout time.Duration
	// IdleTimeout closes the tunnel if no bytes move in either direction for
	// this long. Default 90s.
	IdleTimeout time.Duration
	// MaxConnections is the per-route connection ceiling (enforced by the
	// listener). Default 1000.
	MaxConnections int
	// BufferSize is the copy buffer size per direction. Default 32KiB.
	BufferSize int
}

// withDefaults returns a copy of cfg with zero fields filled in.
func (c TunnelConfig) withDefaults() TunnelConfig {
	if c.DialTimeout <= 0 {
		c.DialTimeout = defaultDialTimeout
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = defaultIdleTimeout
	}
	if c.MaxConnections <= 0 {
		c.MaxConnections = defaultMaxConns
	}
	if c.BufferSize <= 0 {
		c.BufferSize = defaultBufferSize
	}
	return c
}

// bufPool supplies copy buffers so steady-state tunneling allocates nothing.
// Buffers are sized to the most common BufferSize; differently-sized configs
// simply allocate their own (rare).
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, defaultBufferSize)
		return &b
	},
}

// Tunnel copies bytes bidirectionally between client and backend until either
// side closes, an error occurs, the idle timeout fires, or ctx is cancelled.
//
// Each direction runs in its own goroutine. When one direction finishes, the
// peer's write half is closed (CloseWrite on *net.TCPConn) so the other end
// observes EOF and drains cleanly; the function then waits for the second
// direction before returning. A non-nil result reports the first unexpected
// error; a clean close (EOF on both sides) returns nil. Idle timeout returns
// ErrIdleTimeout.
func Tunnel(ctx context.Context, client, backend net.Conn, cfg TunnelConfig) error {
	cfg = cfg.withDefaults()

	// A shared idle deadline: every successful read in either direction pushes
	// it forward. When it lapses, both connections' in-flight reads error with a
	// timeout, which we map to ErrIdleTimeout.
	idle := &idleDeadline{conns: []net.Conn{client, backend}, timeout: cfg.IdleTimeout}
	idle.reset()

	// Cancel support: when ctx is done, unblock both directions by setting an
	// immediate deadline on both connections.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			now := time.Now()
			_ = client.SetDeadline(now)
			_ = backend.SetDeadline(now)
		case <-stop:
		}
	}()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)

	go func() {
		defer wg.Done()
		err := copyHalfConn(backend, client, idle, cfg.BufferSize)
		halfCloseWrite(backend) // signal EOF to backend's reader (the other dir)
		errCh <- err
	}()
	go func() {
		defer wg.Done()
		err := copyHalfConn(client, backend, idle, cfg.BufferSize)
		halfCloseWrite(client)
		errCh <- err
	}()

	wg.Wait()
	close(errCh)

	if ctx.Err() != nil {
		return ctx.Err()
	}
	var firstErr error
	for err := range errCh {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// copyHalfConn copies src→dst using a pooled buffer, advancing the shared idle
// deadline on every successful read. io.EOF is a clean close (returns nil); a
// read timeout caused by the idle deadline lapsing returns ErrIdleTimeout.
func copyHalfConn(dst, src net.Conn, idle *idleDeadline, bufSize int) error {
	var buf []byte
	if bufSize == defaultBufferSize {
		bp := bufPool.Get().(*[]byte)
		defer bufPool.Put(bp)
		buf = *bp
	} else {
		buf = make([]byte, bufSize)
	}

	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			idle.reset()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			var ne net.Error
			if errors.As(rerr, &ne) && ne.Timeout() {
				// Distinguish an idle-timeout lapse from a ctx-cancel deadline.
				if idle.expired() {
					return ErrIdleTimeout
				}
				return rerr
			}
			return rerr
		}
	}
}

// halfCloseWrite closes only the write side of a TCP connection so the peer
// reads EOF while still being able to send any final bytes back. For non-TCP
// connections it is a no-op (the full Close happens in the caller's cleanup).
func halfCloseWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}

// idleDeadline coordinates a shared inactivity timeout across both connections.
type idleDeadline struct {
	conns   []net.Conn
	timeout time.Duration

	mu       sync.Mutex
	deadline time.Time
}

// reset pushes the idle deadline forward by the timeout on both connections.
func (d *idleDeadline) reset() {
	d.mu.Lock()
	d.deadline = time.Now().Add(d.timeout)
	dl := d.deadline
	d.mu.Unlock()
	for _, c := range d.conns {
		_ = c.SetReadDeadline(dl)
	}
}

// expired reports whether the idle deadline has passed.
func (d *idleDeadline) expired() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.deadline.IsZero() && time.Now().After(d.deadline)
}
