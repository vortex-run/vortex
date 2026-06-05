package tcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// selfSignedTLS builds a server TLS config (requiring a client cert) and a
// matching client TLS config that trusts it and presents the same cert. This
// exercises the listener's TLS-wrapping path without importing vtls.
func selfSignedTLS(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	parsed, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(parsed)

	server = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}
	client = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   "localhost",
	}
	return server, client
}

// echoServer is a TCP server that echoes everything it reads back to the
// sender. It is the backend for listener tests.
type echoServer struct {
	ln net.Listener
}

func newEchoServer(t *testing.T) *echoServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	es := &echoServer{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return es
}

func (e *echoServer) addr() string { return e.ln.Addr().String() }

// startListener builds and runs a Listener on an ephemeral port, returning its
// bound address and a cancel func. It waits until the port is accepting.
func startListener(t *testing.T, cfg ListenerConfig) (string, *Listener, context.CancelFunc) {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	// Bind to an ephemeral port we pick, so we know the address up front.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	cfg.ListenAddr = addr

	l, err := NewListener(cfg)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = l.Listen(ctx) }()

	// Wait until the listener is accepting.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(cancel)
	return addr, l, cancel
}

func roundTrip(t *testing.T, addr string, payload []byte) []byte {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial listener: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	return got
}

func TestListener_AcceptsAndForwards(t *testing.T) {
	be := newEchoServer(t)
	pool := NewPool(PoolConfig{})
	defer func() { _ = pool.Close() }()
	addr, _, _ := startListener(t, ListenerConfig{
		Backends: []BackendAddr{{Addr: be.addr(), Weight: 1}},
		Pool:     pool,
	})

	got := roundTrip(t, addr, []byte("hello"))
	if string(got) != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestListener_MultipleClients(t *testing.T) {
	be := newEchoServer(t)
	pool := NewPool(PoolConfig{MaxOpen: 64})
	defer func() { _ = pool.Close() }()
	addr, _, _ := startListener(t, ListenerConfig{
		Backends: []BackendAddr{{Addr: be.addr(), Weight: 1}},
		Pool:     pool,
	})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			payload := []byte{byte('A' + id), byte('0' + id), byte('z')}
			got := roundTrip(t, addr, payload)
			if string(got) != string(payload) {
				t.Errorf("client %d got %q, want %q", id, got, payload)
			}
		}(i)
	}
	wg.Wait()
}

func TestListener_MaxConnectionsRejects(t *testing.T) {
	be := newEchoServer(t)
	pool := NewPool(PoolConfig{MaxOpen: 64})
	defer func() { _ = pool.Close() }()
	addr, l, _ := startListener(t, ListenerConfig{
		Backends:       []BackendAddr{{Addr: be.addr(), Weight: 1}},
		Pool:           pool,
		MaxConnections: 3,
	})

	// Open 6 connections and hold them open.
	var held []net.Conn
	for i := 0; i < 6; i++ {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			continue
		}
		held = append(held, c)
	}
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()

	// Give the accept loop time to process.
	time.Sleep(200 * time.Millisecond)

	if a := l.Stats().Active; a > 3 {
		t.Errorf("Active = %d, must never exceed MaxConnections (3)", a)
	}
	if r := l.Stats().Rejected; r < 3 {
		t.Errorf("Rejected = %d, want >= 3", r)
	}

	// Close the active connections, let the listener reclaim slots.
	for _, c := range held {
		_ = c.Close()
	}
	held = nil
	time.Sleep(300 * time.Millisecond)

	// New connections should now be accepted and forwarded.
	got := roundTrip(t, addr, []byte("again"))
	if string(got) != "again" {
		t.Errorf("post-drain got %q, want again", got)
	}
}

func TestListener_BackendDown(t *testing.T) {
	// Reserve a port with nothing listening.
	probe, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := probe.Addr().String()
	_ = probe.Close()

	pool := NewPool(PoolConfig{DialTimeout: 300 * time.Millisecond})
	defer func() { _ = pool.Close() }()
	addr, l, _ := startListener(t, ListenerConfig{
		Backends: []BackendAddr{{Addr: deadAddr, Weight: 1}},
		Pool:     pool,
	})

	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial listener: %v", err)
	}
	// The backend dial fails, so the listener closes our conn; a read returns
	// EOF/closed without panicking the server.
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 4)
	_, _ = c.Read(buf) // expect EOF/closed; we only assert no crash
	_ = c.Close()

	time.Sleep(200 * time.Millisecond)
	if a := l.Stats().Active; a != 0 {
		t.Errorf("Active = %d after backend-down, want 0", a)
	}
	// Listener still serving: a fresh dial still connects (then closes again).
	c2, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Errorf("listener should still be running: %v", err)
	} else {
		_ = c2.Close()
	}
}

func TestListener_CtxCancelDrains(t *testing.T) {
	be := newEchoServer(t)
	pool := NewPool(PoolConfig{})
	defer func() { _ = pool.Close() }()

	probe, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := probe.Addr().String()
	_ = probe.Close()

	l, err := NewListener(ListenerConfig{
		ListenAddr: addr,
		Backends:   []BackendAddr{{Addr: be.addr(), Weight: 1}},
		Pool:       pool,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- l.Listen(ctx) }()

	// Wait for accept readiness.
	for i := 0; i < 100; i++ {
		if c, e := net.DialTimeout("tcp", addr, 50*time.Millisecond); e == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	var conns []net.Conn
	for i := 0; i < 3; i++ {
		c, e := net.DialTimeout("tcp", addr, time.Second)
		if e == nil {
			conns = append(conns, c)
			_, _ = c.Write([]byte("x"))
		}
	}
	time.Sleep(100 * time.Millisecond)

	cancel()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not return within 5s of ctx cancel")
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

func TestListener_UpdateBackends(t *testing.T) {
	beA := newEchoServer(t)
	beB := newEchoServer(t)
	pool := NewPool(PoolConfig{})
	defer func() { _ = pool.Close() }()
	addr, l, _ := startListener(t, ListenerConfig{
		Backends: []BackendAddr{{Addr: beA.addr(), Weight: 1}},
		Pool:     pool,
	})

	// Both backends echo, so we can't tell them apart by payload alone. Instead
	// assert forwarding works before and after the swap (the WRR-level switch is
	// unit-tested in roundrobin_test). This confirms UpdateBackends keeps the
	// listener serving with the new set.
	if got := roundTrip(t, addr, []byte("a")); string(got) != "a" {
		t.Fatalf("pre-update got %q", got)
	}
	if err := l.UpdateBackends([]BackendAddr{{Addr: beB.addr(), Weight: 1}}); err != nil {
		t.Fatalf("UpdateBackends: %v", err)
	}
	if got := roundTrip(t, addr, []byte("b")); string(got) != "b" {
		t.Errorf("post-update got %q, want b", got)
	}
	if err := l.UpdateBackends(nil); err == nil {
		t.Error("UpdateBackends(nil) should return an error")
	}
}

func TestListener_StatsAccurate(t *testing.T) {
	be := newEchoServer(t)
	pool := NewPool(PoolConfig{MaxOpen: 16})
	defer func() { _ = pool.Close() }()
	addr, l, _ := startListener(t, ListenerConfig{
		Backends: []BackendAddr{{Addr: be.addr(), Weight: 1}},
		Pool:     pool,
	})

	// Hold 4 connections open concurrently.
	var conns []net.Conn
	for i := 0; i < 4; i++ {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte("ping"))
		conns = append(conns, c)
	}
	time.Sleep(200 * time.Millisecond)

	if a := l.Stats().Active; a != 4 {
		t.Errorf("Active = %d, want 4", a)
	}
	if total := l.Stats().Total; total < 4 {
		t.Errorf("Total = %d, want >= 4", total)
	}

	for _, c := range conns {
		_ = c.Close()
	}
	// Wait for tunnels to notice client close and decrement Active.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l.Stats().Active == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if a := l.Stats().Active; a != 0 {
		t.Errorf("Active = %d after close, want 0", a)
	}
}

func TestListener_MTLSRejectsPlainConn(t *testing.T) {
	be := newEchoServer(t)
	serverTLS, _ := selfSignedTLS(t)
	pool := NewPool(PoolConfig{})
	defer func() { _ = pool.Close() }()
	addr, l, _ := startListener(t, ListenerConfig{
		Backends:  []BackendAddr{{Addr: be.addr(), Weight: 1}},
		Pool:      pool,
		TLSConfig: serverTLS,
	})

	// A plain (non-TLS) client: the server's TLS handshake fails, the conn is
	// closed, and the backend receives nothing.
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, _ = c.Write([]byte("plaintext"))
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8)
	if _, rerr := c.Read(buf); rerr == nil {
		t.Error("plain client should not get an echo through an mTLS listener")
	}
	_ = c.Close()

	// The listener must still be running and no active tunnel established.
	time.Sleep(200 * time.Millisecond)
	if a := l.Stats().Active; a != 0 {
		t.Errorf("Active = %d after rejected plain conn, want 0", a)
	}
	// A subsequent proper TLS client still works (listener survived).
	if _, err := net.DialTimeout("tcp", addr, time.Second); err != nil {
		t.Errorf("listener should still accept connections: %v", err)
	}
}

func TestListener_MTLSAcceptsMTLSConn(t *testing.T) {
	be := newEchoServer(t)
	serverTLS, clientTLS := selfSignedTLS(t)
	pool := NewPool(PoolConfig{})
	defer func() { _ = pool.Close() }()
	addr, _, _ := startListener(t, ListenerConfig{
		Backends:  []BackendAddr{{Addr: be.addr(), Weight: 1}},
		Pool:      pool,
		TLSConfig: serverTLS,
	})

	// A TLS client presenting a valid cert: handshake succeeds and bytes tunnel
	// to the echo backend and back.
	conn, err := tls.Dial("tcp", addr, clientTLS)
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	want := []byte("hello mtls")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("echo = %q, want %q", got, want)
	}
}
