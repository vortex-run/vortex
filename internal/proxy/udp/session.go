// Package proxyudp implements VORTEX's UDP tunnel (build plan M2.5): a
// session-tracking forwarder for connectionless UDP traffic with a per-source-IP
// token-bucket rate limiter. UDP has no connection concept, so client→backend
// "sessions" are tracked manually by client address and reaped after an idle
// TTL. Standard library only (Non-Negotiable Rule #10).
package proxyudp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// defaultSessionTTL is used when NewSessionTable is given ttl <= 0.
const defaultSessionTTL = 30 * time.Second

// Session is one client's UDP flow: a backend connection plus liveness and byte
// counters. LastSeen is stored as a Unix-nano atomic so concurrent reader
// (sweeper) and writers (read loop, reply pump) need no lock.
type Session struct {
	ClientAddr  *net.UDPAddr
	BackendConn *net.UDPConn

	lastSeen atomic.Int64 // UnixNano; use LastSeen()/touch()
	BytesIn  atomic.Int64 // client → backend
	BytesOut atomic.Int64 // backend → client
	created  time.Time
}

// LastSeen returns the time of the most recent activity on the session.
func (s *Session) LastSeen() time.Time { return time.Unix(0, s.lastSeen.Load()) }

// touch records activity now.
func (s *Session) touch() { s.lastSeen.Store(time.Now().UnixNano()) }

// SessionStats is a snapshot of session-table counters.
type SessionStats struct {
	Active  int
	Total   int64
	Cleaned int64
}

// SessionTable tracks active UDP sessions keyed by client address string.
type SessionTable struct {
	sessions sync.Map // string(clientAddr) -> *Session
	ttl      time.Duration
	active   atomic.Int64
	total    atomic.Int64
	cleaned  atomic.Int64
}

// NewSessionTable returns a table with the given idle TTL (default 30s).
func NewSessionTable(ttl time.Duration) *SessionTable {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	return &SessionTable{ttl: ttl}
}

// GetOrCreate returns the session for clientAddr, creating one (dialing
// backendAddr) if none exists. The bool is true when a new session was created.
func (t *SessionTable) GetOrCreate(clientAddr *net.UDPAddr, backendAddr string) (*Session, bool, error) {
	key := clientAddr.String()
	if v, ok := t.sessions.Load(key); ok {
		return v.(*Session), false, nil
	}

	conn, err := net.Dial("udp", backendAddr)
	if err != nil {
		return nil, false, fmt.Errorf("dialing backend %s: %w", backendAddr, err)
	}
	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		_ = conn.Close()
		return nil, false, fmt.Errorf("backend %s did not yield a *net.UDPConn", backendAddr)
	}

	s := &Session{ClientAddr: clientAddr, BackendConn: udpConn, created: time.Now()}
	s.touch()

	// Guard against a concurrent creator: LoadOrStore keeps the first winner.
	if existing, loaded := t.sessions.LoadOrStore(key, s); loaded {
		_ = udpConn.Close()
		return existing.(*Session), false, nil
	}
	t.active.Add(1)
	t.total.Add(1)
	return s, true, nil
}

// Touch updates LastSeen for the session with the given client-address key.
func (t *SessionTable) Touch(clientAddr string) {
	if v, ok := t.sessions.Load(clientAddr); ok {
		v.(*Session).touch()
	}
}

// Delete removes and closes the session for the given client-address key.
func (t *SessionTable) Delete(clientAddr string) {
	if v, ok := t.sessions.LoadAndDelete(clientAddr); ok {
		s := v.(*Session)
		_ = s.BackendConn.Close()
		t.active.Add(-1)
		t.cleaned.Add(1)
	}
}

// StartCleanup runs a goroutine that reaps idle sessions every ttl/2 until ctx
// is cancelled.
func (t *SessionTable) StartCleanup(ctx context.Context) {
	go func() {
		interval := t.ttl / 2
		if interval <= 0 {
			interval = t.ttl
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.sweep()
			}
		}
	}()
}

// sweep deletes sessions idle longer than the TTL.
func (t *SessionTable) sweep() {
	now := time.Now()
	t.sessions.Range(func(key, value any) bool {
		if now.Sub(value.(*Session).LastSeen()) > t.ttl {
			t.Delete(key.(string))
		}
		return true
	})
}

// ActiveCount returns the number of live sessions.
func (t *SessionTable) ActiveCount() int { return int(t.active.Load()) }

// Stats returns a snapshot of table counters.
func (t *SessionTable) Stats() SessionStats {
	return SessionStats{
		Active:  int(t.active.Load()),
		Total:   t.total.Load(),
		Cleaned: t.cleaned.Load(),
	}
}
