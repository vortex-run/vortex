package proxyhttp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// waitListening blocks until the server has bound a real port and accepts a TCP
// connection, or the deadline elapses. It re-reads s.Addr() each iteration
// because ListenAndServe runs in a goroutine and binds asynchronously.
func waitListening(t *testing.T, s *Server) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		addr := s.Addr()
		// Skip the unbound config address (host:0) until the real port is set.
		if !endsWithZeroPort(addr) {
			if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
				_ = c.Close()
				return addr
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never started listening (addr=%s)", s.Addr())
	return ""
}

func endsWithZeroPort(addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	return err != nil || port == "0"
}

func okRouter(body string) *Router {
	r := NewRouter()
	r.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	return r
}

func TestServer_StartsAndResponds(t *testing.T) {
	s := NewServer(ServerConfig{Addr: "127.0.0.1:0", Router: okRouter("hello")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.ListenAndServe(ctx) }()
	addr := waitListening(t, s)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello" {
		t.Errorf("body = %q, want hello", body)
	}
}

func TestServer_GracefulShutdown(t *testing.T) {
	// Handler sleeps so a request is in flight when we cancel.
	r := NewRouter()
	r.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "done")
	}))
	s := NewServer(ServerConfig{Addr: "127.0.0.1:0", Router: r})
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- s.ListenAndServe(ctx) }()
	addr := waitListening(t, s)

	// Start an in-flight request, then cancel mid-flight.
	type result struct {
		body string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		resCh <- result{body: string(b)}
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	// The in-flight request should complete (graceful drain).
	select {
	case res := <-resCh:
		if res.err != nil {
			t.Errorf("in-flight request failed during graceful shutdown: %v", res.err)
		} else if res.body != "done" {
			t.Errorf("in-flight body = %q, want done", res.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	select {
	case err := <-returned:
		if err != nil {
			t.Errorf("ListenAndServe returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ListenAndServe did not return after ctx cancel")
	}
}

func TestServer_ActiveConnsTracked(t *testing.T) {
	s := NewServer(ServerConfig{Addr: "127.0.0.1:0", Router: okRouter("x")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.ListenAndServe(ctx) }()
	addr := waitListening(t, s)

	// Open a raw connection and hold it open.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	// Send a request to trigger StateActive/New accounting.
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	time.Sleep(200 * time.Millisecond)
	if a := s.Stats().ActiveConns; a < 1 {
		t.Errorf("ActiveConns = %d, want >= 1 while a conn is open", a)
	}
	_ = conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.Stats().ActiveConns == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if a := s.Stats().ActiveConns; a != 0 {
		t.Errorf("ActiveConns = %d after close, want 0", a)
	}
}

func TestServer_TLS(t *testing.T) {
	cert := selfSignedCert(t)
	s := NewServer(ServerConfig{
		Addr:      "127.0.0.1:0",
		Router:    okRouter("secure"),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.ListenAndServe(ctx) }()
	addr := waitListening(t, s)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // self-signed test cert
	}}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("HTTPS GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "secure" {
		t.Errorf("body = %q, want secure", body)
	}
}

func TestServer_PolicyMiddlewareCalled(t *testing.T) {
	var called atomic.Bool
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called.Store(true)
			next.ServeHTTP(w, r)
		})
	}
	s := NewServer(ServerConfig{
		Addr: "127.0.0.1:0", Router: okRouter("ok"), PolicyMiddleware: mw,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.ListenAndServe(ctx) }()
	addr := waitListening(t, s)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if !called.Load() {
		t.Error("PolicyMiddleware was not invoked")
	}
}

func TestServer_NilPolicyMiddlewarePassesThrough(t *testing.T) {
	// With no PolicyMiddleware the request must reach the router unchanged.
	s := NewServer(ServerConfig{Addr: "127.0.0.1:0", Router: okRouter("through")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.ListenAndServe(ctx) }()
	addr := waitListening(t, s)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "through" {
		t.Errorf("body = %q, want through", body)
	}
}

// selfSignedCert generates an in-memory self-signed cert for the TLS test.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
