package proxyudp

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

// udpEcho starts a UDP echo server and returns its address.
func udpEcho(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(buf[:n], addr)
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return conn.LocalAddr().String()
}

// startUDPListener builds and runs a UDPListener on an ephemeral port, returning
// its bound address.
func startUDPListener(t *testing.T, cfg UDPListenerConfig) (string, *UDPListener, context.CancelFunc) {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	cfg.ListenAddr = "127.0.0.1:0"

	l, err := NewListener(cfg)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = l.Listen(ctx) }()

	// Wait until the socket is bound (LocalAddr is race-free).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a := l.LocalAddr(); a != "" {
			t.Cleanup(cancel)
			return a, l, cancel
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("UDP listener did not bind in time")
	return "", nil, cancel
}

// sendRecv sends payload to addr and waits for one datagram back.
func sendRecv(t *testing.T, addr string, payload []byte, timeout time.Duration) ([]byte, error) {
	t.Helper()
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	c, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write(payload); err != nil {
		return nil, err
	}
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	n, _, err := c.ReadFromUDP(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func TestUDPListener_ForwardsDatagram(t *testing.T) {
	be := udpEcho(t)
	addr, _, _ := startUDPListener(t, UDPListenerConfig{BackendAddr: be})

	got, err := sendRecv(t, addr, []byte("hello"), 2*time.Second)
	if err != nil {
		t.Fatalf("sendRecv: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestUDPListener_MultipleClientsIsolated(t *testing.T) {
	be := udpEcho(t)
	addr, _, _ := startUDPListener(t, UDPListenerConfig{BackendAddr: be})

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			payload := []byte{byte('A' + id), byte('0' + id)}
			got, err := sendRecv(t, addr, payload, 2*time.Second)
			if err != nil {
				t.Errorf("client %d: %v", id, err)
				return
			}
			if string(got) != string(payload) {
				t.Errorf("client %d got %q, want %q", id, got, payload)
			}
		}(i)
	}
	wg.Wait()
}

func TestUDPListener_RateLimitDrops(t *testing.T) {
	// Count datagrams the backend actually receives.
	beConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = beConn.Close() }()
	var received int
	var mu sync.Mutex
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := beConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			mu.Lock()
			received++
			mu.Unlock()
			_, _ = beConn.WriteToUDP(buf[:n], addr)
		}
	}()

	addr, _, _ := startUDPListener(t, UDPListenerConfig{
		BackendAddr: beConn.LocalAddr().String(),
		RateLimit:   1,
		RateBurst:   1,
	})

	// Send 10 packets rapidly from one client (one source port → one IP).
	raddr, _ := net.ResolveUDPAddr("udp", addr)
	c, _ := net.DialUDP("udp", nil, raddr)
	defer func() { _ = c.Close() }()
	for i := 0; i < 10; i++ {
		_, _ = c.Write([]byte("x"))
	}
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	got := received
	mu.Unlock()
	if got >= 10 {
		t.Errorf("backend received %d of 10 packets; rate limit should have dropped some", got)
	}
}

func TestUDPListener_MaxSessions(t *testing.T) {
	be := udpEcho(t)
	addr, l, _ := startUDPListener(t, UDPListenerConfig{
		BackendAddr: be,
		MaxSessions: 2,
		SessionTTL:  time.Minute,
	})
	raddr, _ := net.ResolveUDPAddr("udp", addr)

	// Three distinct clients (distinct source ports) send simultaneously.
	var conns []*net.UDPConn
	for i := 0; i < 3; i++ {
		c, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		_, _ = c.Write([]byte("hi"))
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	time.Sleep(300 * time.Millisecond)

	if a := l.Stats().Sessions.Active; a > 2 {
		t.Errorf("active sessions = %d, must not exceed MaxSessions (2)", a)
	}
}

func TestUDPListener_CtxCancelStops(t *testing.T) {
	be := udpEcho(t)
	cfg := UDPListenerConfig{BackendAddr: be, ListenAddr: "127.0.0.1:0",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	l, err := NewListener(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- l.Listen(ctx) }()

	// Wait for bind, send a datagram, then cancel.
	for i := 0; i < 100 && l.LocalAddr() == ""; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if a := l.LocalAddr(); a != "" {
		_, _ = sendRecv(t, a, []byte("ping"), 500*time.Millisecond)
	}
	cancel()

	select {
	case err := <-returned:
		if err != nil {
			t.Errorf("Listen returned error on cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Listen did not return within 2s of ctx cancel")
	}
}
