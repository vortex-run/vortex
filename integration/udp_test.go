//go:build integration

package integration

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	proxyudp "github.com/vortex-run/vortex/internal/proxy/udp"
)

// udpEchoServer starts a UDP echo server and returns its address.
func udpEchoServer(t *testing.T) string {
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

// startUDP starts a proxyudp listener forwarding to backend and returns its
// bound address plus the listener (for stats) and a cancel func.
func startUDP(t *testing.T, cfg proxyudp.UDPListenerConfig) (string, *proxyudp.UDPListener, context.CancelFunc) {
	t.Helper()
	cfg.ListenAddr = "127.0.0.1:0"
	l, err := proxyudp.NewListener(cfg)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = l.Listen(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a := l.LocalAddr(); a != "" {
			t.Cleanup(cancel)
			return a, l, cancel
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("UDP listener did not bind")
	return "", nil, cancel
}

func udpRoundTrip(t *testing.T, addr string, payload []byte, timeout time.Duration) ([]byte, error) {
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

func TestUDP_ForwardsDatagrams(t *testing.T) {
	be := udpEchoServer(t)
	addr, _, cancel := startUDP(t, proxyudp.UDPListenerConfig{BackendAddr: be})

	got, err := udpRoundTrip(t, addr, []byte("hello udp vortex"), 2*time.Second)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if string(got) != "hello udp vortex" {
		t.Errorf("got %q, want 'hello udp vortex'", got)
	}
	cancel() // listener should stop without hanging (cleanup verifies)
}

func TestUDP_MultipleClients(t *testing.T) {
	be := udpEchoServer(t)
	addr, _, _ := startUDP(t, proxyudp.UDPListenerConfig{BackendAddr: be})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			payload := []byte{byte('a' + id), byte('0' + id), byte('z')}
			got, err := udpRoundTrip(t, addr, payload, 2*time.Second)
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

func TestUDP_SessionExpiry(t *testing.T) {
	be := udpEchoServer(t)
	addr, l, _ := startUDP(t, proxyudp.UDPListenerConfig{
		BackendAddr: be,
		SessionTTL:  500 * time.Millisecond,
	})

	// First datagram → session created.
	if _, err := udpRoundTrip(t, addr, []byte("one"), 2*time.Second); err != nil {
		t.Fatal(err)
	}

	// Wait past the TTL so the sweep reaps the idle session.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l.Stats().Sessions.Cleaned >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if c := l.Stats().Sessions.Cleaned; c < 1 {
		t.Errorf("Cleaned = %d, want >= 1 after TTL expiry", c)
	}

	// A new datagram creates a fresh session.
	if _, err := udpRoundTrip(t, addr, []byte("two"), 2*time.Second); err != nil {
		t.Fatalf("second round trip: %v", err)
	}
}
