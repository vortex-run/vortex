package proxyquic

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// discardLogger returns a logger that drops output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// freePort reserves and releases a TCP+UDP port pair on the same number,
// returning "127.0.0.1:<port>" suitable for a shared dual-stack Addr.
func freePort(t *testing.T) string {
	t.Helper()
	// Reserve via TCP (gives us a concrete port), then release.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func newDualStack(t *testing.T, addr string) *DualStack {
	t.Helper()
	srvTLS, _ := testTLSConfig(t)
	d, err := NewDualStack(DualStackConfig{
		Addr:      addr,
		TLSConfig: srvTLS,
		Router:    okRouter(t, "dual ok"),
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewDualStack: %v", err)
	}
	return d
}

// waitTCP waits until addr accepts a TCP connection.
func waitTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("TCP never came up on %s", addr)
}

// httpsClient returns a client that trusts the test cert and forces HTTP/1.1
// (so we exercise the TCP path, not HTTP/3).
func httpsClient() *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // self-signed test cert
		},
	}
}

func TestDualStack_StartsBoth(t *testing.T) {
	addr := freePort(t)
	d := newDualStack(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.ListenAndServe(ctx) }()
	waitTCP(t, addr)

	resp, err := httpsClient().Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "dual ok" {
		t.Errorf("body = %q, want 'dual ok'", body)
	}
	if !d.Stats().QUICAvailable {
		t.Error("QUIC should be available when its port is free")
	}
}

func TestDualStack_AltSvcHeader(t *testing.T) {
	addr := freePort(t)
	d := newDualStack(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.ListenAndServe(ctx) }()
	waitTCP(t, addr)

	resp, err := httpsClient().Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	altSvc := resp.Header.Get("Alt-Svc")
	if !strings.Contains(altSvc, "h3") {
		t.Errorf("Alt-Svc = %q, want it to advertise h3", altSvc)
	}
}

func TestDualStack_QUICUnavailable(t *testing.T) {
	addr := freePort(t)

	// Occupy the UDP port so QUIC cannot bind it.
	_, portStr, _ := net.SplitHostPort(addr)
	udpAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:"+portStr)
	blocker, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Skipf("could not occupy UDP port to simulate QUIC-unavailable: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	d := newDualStack(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.ListenAndServe(ctx) }()
	waitTCP(t, addr)

	// TCP must still work.
	resp, err := httpsClient().Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("TCP GET should still work: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// Give the QUIC goroutine a moment to report its bind failure.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !d.Stats().QUICAvailable {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if d.Stats().QUICAvailable {
		t.Error("QUICAvailable should be false when the UDP port is occupied")
	}
}

func TestDualStack_ShutsDownOnCancel(t *testing.T) {
	addr := freePort(t)
	d := newDualStack(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- d.ListenAndServe(ctx) }()
	waitTCP(t, addr)

	cancel()
	select {
	case err := <-returned:
		if err != nil {
			t.Errorf("ListenAndServe returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after ctx cancel")
	}
}
