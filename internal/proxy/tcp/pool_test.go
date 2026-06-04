package tcp

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeBackend is a TCP listener that accepts connections and holds them open
// (without sending data) so pooled connections probe as healthy.
type fakeBackend struct {
	ln     net.Listener
	mu     sync.Mutex
	accept []net.Conn
}

func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fb := &fakeBackend{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			fb.mu.Lock()
			fb.accept = append(fb.accept, c)
			fb.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		fb.mu.Lock()
		for _, c := range fb.accept {
			_ = c.Close()
		}
		fb.mu.Unlock()
	})
	return fb
}

func (fb *fakeBackend) addr() string { return fb.ln.Addr().String() }

func TestPool_GetReturnsConnection(t *testing.T) {
	fb := newFakeBackend(t)
	p := NewPool(PoolConfig{})
	defer func() { _ = p.Close() }()

	conn, err := p.Get(context.Background(), "tcp", fb.addr())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if conn == nil {
		t.Fatal("Get returned nil conn")
	}
	if conn.RemoteAddr().String() != fb.addr() {
		t.Errorf("remote addr = %s, want %s", conn.RemoteAddr(), fb.addr())
	}
	p.Put(conn, fb.addr())
}

func TestPool_PutRecyclesConnection(t *testing.T) {
	fb := newFakeBackend(t)
	p := NewPool(PoolConfig{MaxIdle: 4})
	defer func() { _ = p.Close() }()

	c1, err := p.Get(context.Background(), "tcp", fb.addr())
	if err != nil {
		t.Fatal(err)
	}
	localAddr := c1.LocalAddr().String()
	p.Put(c1, fb.addr())

	c2, err := p.Get(context.Background(), "tcp", fb.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Put(c2, fb.addr())
	if c2.LocalAddr().String() != localAddr {
		t.Errorf("expected recycled conn (local %s), got %s", localAddr, c2.LocalAddr())
	}
}

func TestPool_MaxIdleEnforced(t *testing.T) {
	fb := newFakeBackend(t)
	p := NewPool(PoolConfig{MaxIdle: 1, MaxOpen: 4})
	defer func() { _ = p.Close() }()

	c1, _ := p.Get(context.Background(), "tcp", fb.addr())
	c2, _ := p.Get(context.Background(), "tcp", fb.addr())

	p.Put(c1, fb.addr()) // retained (idle pool now full at 1)
	p.Put(c2, fb.addr()) // over MaxIdle → must be closed

	// c2 should be closed: a write/read should fail.
	_ = c2.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 1)
	if _, err := c2.Read(buf); err == nil {
		t.Error("expected c2 to be closed after exceeding MaxIdle")
	}
	if s := p.Stats(); s.Idle != 1 {
		t.Errorf("idle = %d, want 1", s.Idle)
	}
}

func TestPool_MaxOpenBlocksUntilPut(t *testing.T) {
	fb := newFakeBackend(t)
	p := NewPool(PoolConfig{MaxOpen: 1, MaxIdle: 1})
	defer func() { _ = p.Close() }()

	c1, err := p.Get(context.Background(), "tcp", fb.addr())
	if err != nil {
		t.Fatal(err)
	}

	unblocked := make(chan net.Conn, 1)
	go func() {
		c, _ := p.Get(context.Background(), "tcp", fb.addr())
		unblocked <- c
	}()

	// The second Get must block while we hold the only slot.
	select {
	case <-unblocked:
		t.Fatal("second Get should have blocked at MaxOpen")
	case <-time.After(50 * time.Millisecond):
	}

	p.Put(c1, fb.addr()) // frees/returns the slot

	select {
	case c := <-unblocked:
		if c == nil {
			t.Fatal("unblocked Get returned nil")
		}
		p.Put(c, fb.addr())
	case <-time.After(2 * time.Second):
		t.Fatal("Get did not unblock within 2s after Put")
	}

	if s := p.Stats(); s.WaitCount < 1 {
		t.Errorf("WaitCount = %d, want >= 1", s.WaitCount)
	}
}

func TestPool_DeadConnectionDiscarded(t *testing.T) {
	fb := newFakeBackend(t)
	p := NewPool(PoolConfig{MaxIdle: 4, MaxOpen: 4})
	defer func() { _ = p.Close() }()

	c1, err := p.Get(context.Background(), "tcp", fb.addr())
	if err != nil {
		t.Fatal(err)
	}
	local1 := c1.LocalAddr().String()
	p.Put(c1, fb.addr())

	// Kill the pooled connection out from under the pool.
	c1.Close()

	c2, err := p.Get(context.Background(), "tcp", fb.addr())
	if err != nil {
		t.Fatalf("Get after dead conn: %v", err)
	}
	defer p.Put(c2, fb.addr())
	if c2.LocalAddr().String() == local1 {
		t.Error("expected a fresh connection; dead one should have been discarded")
	}
}

func TestPool_CloseRejectsGet(t *testing.T) {
	fb := newFakeBackend(t)
	p := NewPool(PoolConfig{})
	_ = p.Close()
	if _, err := p.Get(context.Background(), "tcp", fb.addr()); err == nil {
		t.Error("Get after Close should return an error")
	}
}

func TestPool_StatsActiveAndWaitCount(t *testing.T) {
	fb := newFakeBackend(t)
	p := NewPool(PoolConfig{MaxOpen: 2, MaxIdle: 2})
	defer func() { _ = p.Close() }()

	c1, _ := p.Get(context.Background(), "tcp", fb.addr())
	c2, _ := p.Get(context.Background(), "tcp", fb.addr())
	if s := p.Stats(); s.Active != 2 {
		t.Errorf("Active = %d, want 2", s.Active)
	}

	p.Put(c1, fb.addr())
	if s := p.Stats(); s.Active != 1 {
		t.Errorf("Active after one Put = %d, want 1", s.Active)
	}
	p.Put(c2, fb.addr())
	if s := p.Stats(); s.Active != 0 {
		t.Errorf("Active after both Put = %d, want 0", s.Active)
	}
}
