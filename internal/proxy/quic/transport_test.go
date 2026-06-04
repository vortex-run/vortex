package proxyquic

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
)

// testTLSConfig returns a server TLS config with a self-signed cert for
// 127.0.0.1, plus a matching client config that trusts it.
func testTLSConfig(t *testing.T) (server, client *tls.Config) {
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
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}

	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)

	server = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{http3.NextProtoH3}}
	client = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13, NextProtos: []string{http3.NextProtoH3}}
	return server, client
}

// freeUDPPort reserves and releases a UDP port, returning "127.0.0.1:<port>".
func freeUDPPort(t *testing.T) string {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	addr := c.LocalAddr().String()
	_ = c.Close()
	return addr
}

func okRouter(t *testing.T, body string) *proxyhttp.Router {
	t.Helper()
	r := proxyhttp.NewRouter()
	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	return r
}

func TestNewTransport_NilTLSConfig(t *testing.T) {
	_, err := NewTransport(QUICConfig{Addr: ":443", Router: okRouter(t, "x")})
	if err == nil {
		t.Error("expected error when TLSConfig is nil")
	}
}

func TestNewTransport_NilRouter(t *testing.T) {
	srv, _ := testTLSConfig(t)
	_, err := NewTransport(QUICConfig{Addr: ":443", TLSConfig: srv})
	if err == nil {
		t.Error("expected error when Router is nil")
	}
}

func TestNewTransport_EmptyAddr(t *testing.T) {
	srv, _ := testTLSConfig(t)
	_, err := NewTransport(QUICConfig{TLSConfig: srv, Router: okRouter(t, "x")})
	if err == nil {
		t.Error("expected error when Addr is empty")
	}
}

func TestTransport_ServesHTTP3(t *testing.T) {
	srvTLS, cliTLS := testTLSConfig(t)
	addr := freeUDPPort(t)

	tr, err := NewTransport(QUICConfig{
		Addr:       addr,
		TLSConfig:  srvTLS,
		Router:     okRouter(t, "quic ok"),
		Enable0RTT: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.ListenAndServe(ctx) }()

	// Client over HTTP/3.
	roundTripper := &http3.Transport{TLSClientConfig: cliTLS, QUICConfig: &quic.Config{}}
	defer func() { _ = roundTripper.Close() }()
	client := &http.Client{Transport: roundTripper, Timeout: 5 * time.Second}

	// Retry briefly while the UDP listener comes up.
	var resp *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, e := client.Get(fmt.Sprintf("https://%s/", addr))
		if e == nil {
			resp = r
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if resp == nil {
		t.Fatal("no HTTP/3 response received within 5s")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "quic ok" {
		t.Errorf("body = %q, want 'quic ok'", body)
	}
}

func TestTransport_ShutsDownOnCancel(t *testing.T) {
	srvTLS, _ := testTLSConfig(t)
	tr, err := NewTransport(QUICConfig{
		Addr:      freeUDPPort(t),
		TLSConfig: srvTLS,
		Router:    okRouter(t, "x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- tr.ListenAndServe(ctx) }()

	time.Sleep(200 * time.Millisecond) // let it bind
	cancel()

	select {
	case err := <-returned:
		if err != nil {
			t.Errorf("ListenAndServe returned error on cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after ctx cancel")
	}
}
