package proxyudp

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// udpBackend starts a UDP socket that discards what it receives (it just needs
// to be a valid dial target) and returns its address.
func udpBackend(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := conn.ReadFromUDP(buf); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return conn.LocalAddr().String()
}

// clientAddr returns a synthetic UDP client address for keying sessions.
func clientAddr(t *testing.T, port int) *net.UDPAddr {
	t.Helper()
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
}

func TestSession_GetOrCreateNew(t *testing.T) {
	be := udpBackend(t)
	tbl := NewSessionTable(time.Minute)
	s, created, err := tbl.GetOrCreate(clientAddr(t, 5001), be)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("first GetOrCreate should report created=true")
	}
	if s.BackendConn == nil {
		t.Error("session BackendConn is nil")
	}
}

func TestSession_GetOrCreateExisting(t *testing.T) {
	be := udpBackend(t)
	tbl := NewSessionTable(time.Minute)
	ca := clientAddr(t, 5002)
	s1, _, _ := tbl.GetOrCreate(ca, be)
	s2, created, err := tbl.GetOrCreate(ca, be)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("second GetOrCreate should report created=false")
	}
	if s1 != s2 {
		t.Error("expected the same session pointer on second call")
	}
	if s1.BackendConn != s2.BackendConn {
		t.Error("expected the same BackendConn")
	}
}

func TestSession_DialsCorrectBackend(t *testing.T) {
	be := udpBackend(t)
	tbl := NewSessionTable(time.Minute)
	s, _, err := tbl.GetOrCreate(clientAddr(t, 5003), be)
	if err != nil {
		t.Fatal(err)
	}
	if s.BackendConn.RemoteAddr().String() != be {
		t.Errorf("backend remote = %s, want %s", s.BackendConn.RemoteAddr(), be)
	}
}

func TestSession_DeleteClosesConn(t *testing.T) {
	be := udpBackend(t)
	tbl := NewSessionTable(time.Minute)
	ca := clientAddr(t, 5004)
	s, _, _ := tbl.GetOrCreate(ca, be)
	tbl.Delete(ca.String())

	if tbl.ActiveCount() != 0 {
		t.Errorf("active = %d after delete, want 0", tbl.ActiveCount())
	}
	// Writing on a closed conn should fail.
	if _, err := s.BackendConn.Write([]byte("x")); err == nil {
		t.Error("BackendConn should be closed after Delete")
	}
}

func TestSession_StatsActive(t *testing.T) {
	be := udpBackend(t)
	tbl := NewSessionTable(time.Minute)
	_, _, _ = tbl.GetOrCreate(clientAddr(t, 5005), be)
	_, _, _ = tbl.GetOrCreate(clientAddr(t, 5006), be)
	if s := tbl.Stats(); s.Active != 2 {
		t.Errorf("Active = %d, want 2", s.Active)
	}
	tbl.Delete(clientAddr(t, 5005).String())
	if s := tbl.Stats(); s.Active != 1 {
		t.Errorf("Active after delete = %d, want 1", s.Active)
	}
}

func TestSession_StatsTotal(t *testing.T) {
	be := udpBackend(t)
	tbl := NewSessionTable(time.Minute)
	_, _, _ = tbl.GetOrCreate(clientAddr(t, 5007), be)
	_, _, _ = tbl.GetOrCreate(clientAddr(t, 5008), be)
	if s := tbl.Stats(); s.Total != 2 {
		t.Errorf("Total = %d, want 2", s.Total)
	}
}

func TestSession_StatsCleaned(t *testing.T) {
	be := udpBackend(t)
	tbl := NewSessionTable(50 * time.Millisecond)
	s, _, _ := tbl.GetOrCreate(clientAddr(t, 5009), be)
	// Backdate LastSeen so the sweep removes it.
	s.lastSeen.Store(time.Now().Add(-time.Hour).UnixNano())
	tbl.sweep()
	if st := tbl.Stats(); st.Cleaned < 1 {
		t.Errorf("Cleaned = %d, want >= 1", st.Cleaned)
	}
}

func TestSession_TTLSweepRemovesIdle(t *testing.T) {
	be := udpBackend(t)
	tbl := NewSessionTable(50 * time.Millisecond)
	ca := clientAddr(t, 5010)
	s, _, _ := tbl.GetOrCreate(ca, be)
	s.lastSeen.Store(time.Now().Add(-time.Hour).UnixNano())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tbl.StartCleanup(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tbl.ActiveCount() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tbl.ActiveCount() != 0 {
		t.Error("idle session should have been swept")
	}
}

func TestSession_TouchSurvivesSweep(t *testing.T) {
	be := udpBackend(t)
	tbl := NewSessionTable(200 * time.Millisecond)
	ca := clientAddr(t, 5011)
	_, _, _ = tbl.GetOrCreate(ca, be)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tbl.StartCleanup(ctx)

	// Keep touching for longer than the TTL; the session must survive.
	for i := 0; i < 8; i++ {
		tbl.Touch(ca.String())
		time.Sleep(50 * time.Millisecond)
	}
	if tbl.ActiveCount() != 1 {
		t.Error("touched session should survive the sweep")
	}
}

func TestSession_DialFailure(t *testing.T) {
	tbl := NewSessionTable(time.Minute)
	// Port 0 is not a valid dial target for a connected UDP socket.
	_, _, err := tbl.GetOrCreate(clientAddr(t, 5012), "256.256.256.256:9")
	if err == nil {
		t.Error("expected dial error for an invalid backend address")
	}
}

func TestSession_ConcurrentGetOrCreate(t *testing.T) {
	be := udpBackend(t)
	tbl := NewSessionTable(time.Minute)
	ca := clientAddr(t, 5013)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := tbl.GetOrCreate(ca, be); err != nil {
				t.Errorf("GetOrCreate: %v", err)
			}
		}()
	}
	wg.Wait()
	// Despite 50 concurrent creators, exactly one session should exist.
	if a := tbl.ActiveCount(); a != 1 {
		t.Errorf("active = %d, want exactly 1 after concurrent creates", a)
	}
}
