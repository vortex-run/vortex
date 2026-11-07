package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sandeepbollavaram/vortex/internal/config"
	"github.com/sandeepbollavaram/vortex/internal/proxy/tcp"
	vtls "github.com/sandeepbollavaram/vortex/internal/tls"
)

// Audit F-10: mtls:true on an https route must require a cluster client
// certificate. Before the fix it was ignored and an anonymous client reached
// the backend.
func TestManager_HTTPSRouteEnforcesMTLS(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "backend-reached")
	}))
	defer be.Close()
	tm, err := vtls.NewManager(vtls.ManagerConfig{Provider: "internal",
		StorePath: filepath.Join(t.TempDir(), "tls"), StoreKey: []byte("mtls-l7-test-key")})
	if err != nil {
		t.Fatal(err)
	}
	mc := testMTLSConfig(t, "prod")
	port := freePort(t)
	m, err := NewManager(ManagerConfig{
		Config: &config.Config{Routes: []config.Route{
			{Name: "web", Protocol: "https", Listen: port, Backends: []config.Backend{backendOf(t, be)}, MTLS: true},
		}},
		TLS: tm, TCPPool: tcp.NewPool(tcp.PoolConfig{}), BindHost: "127.0.0.1",
		MTLSConfig: mc, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Start(ctx) }()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	waitTCP(t, addr)

	get := func(tc *tls.Config) (int, error) {
		c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tc}}
		resp, err := c.Get("https://" + addr + "/")
		if err != nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	if code, err := get(&tls.Config{InsecureSkipVerify: true, ServerName: "localhost"}); err == nil { //nolint:gosec // test-only
		t.Fatalf("client without a certificate reached the backend: status %d", code)
	}
	withCert := &tls.Config{InsecureSkipVerify: true, ServerName: "localhost", //nolint:gosec // test-only
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return mc.RotationMgr.Current(), nil }}
	if code, err := get(withCert); err != nil || code != http.StatusOK {
		t.Fatalf("cluster client cert: status %d err %v, want 200", code, err)
	}
}

func TestManager_L7MTLSMisconfigurationFailsBuild(t *testing.T) {
	tm, err := vtls.NewManager(vtls.ManagerConfig{Provider: "internal",
		StorePath: filepath.Join(t.TempDir(), "tls"), StoreKey: []byte("mtls-l7-test-key")})
	if err != nil {
		t.Fatal(err)
	}
	for _, proto := range []string{"https", "h3", "http", "udp"} {
		_, err := NewManager(ManagerConfig{
			Config: &config.Config{Routes: []config.Route{
				{Name: "web", Protocol: proto, Listen: freePort(t),
					Backends: []config.Backend{{Host: "127.0.0.1", Port: freePort(t), Weight: 1}}, MTLS: true},
			}},
			TLS: tm, TCPPool: tcp.NewPool(tcp.PoolConfig{}), BindHost: "127.0.0.1", Logger: discardLogger(),
		})
		if err == nil {
			t.Errorf("%s route with mtls:true and no mTLS config must fail to build", proto)
		}
	}
}
