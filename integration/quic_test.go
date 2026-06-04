//go:build integration

package integration

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
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
	proxyquic "github.com/vortex-run/vortex/internal/proxy/quic"
)

// quicTLS builds a self-signed server TLS config plus a client config that
// trusts it, both negotiating h3.
func quicTLS(t *testing.T) (server, client *tls.Config) {
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
	parsed, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(parsed)

	server = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{http3.NextProtoH3}}
	client = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13, NextProtos: []string{http3.NextProtoH3}}
	return server, client
}

func quicRouter(t *testing.T, body string) *proxyhttp.Router {
	t.Helper()
	r := proxyhttp.NewRouter()
	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	return r
}

func reserveAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestQUIC_HTTP3Request(t *testing.T) {
	srvTLS, cliTLS := quicTLS(t)
	addr := reserveAddr(t)

	tr, err := proxyquic.NewTransport(proxyquic.QUICConfig{
		Addr:       addr,
		TLSConfig:  srvTLS,
		Router:     quicRouter(t, "quic ok"),
		Enable0RTT: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.ListenAndServe(ctx) }()

	rt := &http3.Transport{TLSClientConfig: cliTLS, QUICConfig: &quic.Config{}}
	defer func() { _ = rt.Close() }()
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	var resp *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r, e := client.Get("https://" + addr + "/"); e == nil {
			resp = r
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if resp == nil {
		t.Fatal("no HTTP/3 response within 5s")
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

func TestQUIC_AltSvcHeader(t *testing.T) {
	srvTLS, _ := quicTLS(t)
	addr := reserveAddr(t)

	d, err := proxyquic.NewDualStack(proxyquic.DualStackConfig{
		Addr:      addr,
		TLSConfig: srvTLS,
		Router:    quicRouter(t, "ok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.ListenAndServe(ctx) }()
	waitTCPUp(t, addr)

	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // self-signed test cert
	}}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if alt := resp.Header.Get("Alt-Svc"); !strings.Contains(alt, "h3") {
		t.Errorf("Alt-Svc = %q, want it to advertise h3", alt)
	}
}

func TestQUIC_TCPFallback(t *testing.T) {
	srvTLS, _ := quicTLS(t)
	addr := reserveAddr(t)

	// Occupy the UDP port so QUIC cannot bind.
	_, portStr, _ := net.SplitHostPort(addr)
	ua, _ := net.ResolveUDPAddr("udp", "127.0.0.1:"+portStr)
	blocker, err := net.ListenUDP("udp", ua)
	if err != nil {
		t.Skipf("could not occupy UDP port: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	d, err := proxyquic.NewDualStack(proxyquic.DualStackConfig{
		Addr:      addr,
		TLSConfig: srvTLS,
		Router:    quicRouter(t, "ok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.ListenAndServe(ctx) }()
	waitTCPUp(t, addr)

	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // self-signed test cert
	}}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("TCP GET should still work: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !d.Stats().QUICAvailable {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if d.Stats().QUICAvailable {
		t.Error("QUICAvailable should be false when UDP port is occupied")
	}
}

func waitTCPUp(t *testing.T, addr string) {
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
