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

	"github.com/quic-go/quic-go/http3"
	"github.com/sandeepbollavaram/vortex/internal/config"
	"github.com/sandeepbollavaram/vortex/internal/policy"
	"github.com/sandeepbollavaram/vortex/internal/proxy/tcp"
	"github.com/sandeepbollavaram/vortex/internal/security"
	vtls "github.com/sandeepbollavaram/vortex/internal/tls"
)

// startH3Route boots one h3 route (plus nothing else) and returns its port.
func startH3Route(t *testing.T, edge *security.Edge, eng *policy.Engine) int {
	t.Helper()
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "h3-backend")
	}))
	t.Cleanup(be.Close)
	tm, err := vtls.NewManager(vtls.ManagerConfig{Provider: "internal",
		StorePath: filepath.Join(t.TempDir(), "tls"), StoreKey: []byte("h3-chain-test-store-key")})
	if err != nil {
		t.Fatalf("tls manager: %v", err)
	}
	port := freePort(t)
	m, err := NewManager(ManagerConfig{
		Config: &config.Config{Routes: []config.Route{
			{Name: "fast", Protocol: "h3", Listen: port, Backends: []config.Backend{backendOf(t, be)}},
		}},
		TLS: tm, TCPPool: tcp.NewPool(tcp.PoolConfig{}), BindHost: "127.0.0.1",
		Edge: edge, PolicyEngine: eng, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = m.Start(ctx) }()
	waitTCP(t, "127.0.0.1:"+strconv.Itoa(port))
	return port
}

// assertH3Rejected checks both transports of an h3 route. The TCP fallback
// must answer with want; the QUIC path must never reach the backend (a
// transport error — e.g. no UDP in the sandbox — is not a bypass).
func assertH3Rejected(t *testing.T, port, want int) {
	t.Helper()
	url := "https://127.0.0.1:" + strconv.Itoa(port) + "/"
	tcpc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"}}} //nolint:gosec // test-only self-signed CA
	resp, err := tcpc.Get(url)
	if err != nil {
		t.Fatalf("tcp fallback: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != want {
		t.Errorf("h3 TCP fallback: status=%d body=%q, want %d (L7 chain skipped)", resp.StatusCode, b, want)
	}

	h3 := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "localhost", NextProtos: []string{"h3"}}} //nolint:gosec // test-only
	defer h3.Close()
	qc := &http.Client{Timeout: 5 * time.Second, Transport: h3}
	resp, err = qc.Get(url)
	if err != nil {
		t.Logf("QUIC unavailable here (%v); TCP fallback still asserted", err)
		return
	}
	b, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != want {
		t.Errorf("h3 QUIC: proto=%s status=%d body=%q, want %d (L7 chain skipped)", resp.Proto, resp.StatusCode, b, want)
	}
}

// Audit F-02: an h3 route must enforce the edge IP allowlist on both
// transports. Before the fix it returned 200 from the backend.
func TestH3Route_EnforcesEdgeAllowlist(t *testing.T) {
	bl, err := security.NewBlocklist(security.BlocklistConfig{IPAllowlist: []string{"10.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	port := startH3Route(t, security.NewEdge(security.EdgeConfig{Blocklist: bl}), nil)
	assertH3Rejected(t, port, http.StatusForbidden)
}

// Audit F-02: an h3 route must enforce the OPA policy on both transports.
func TestH3Route_EnforcesPolicy(t *testing.T) {
	port := startH3Route(t, nil, denyAllEngine(t))
	assertH3Rejected(t, port, http.StatusForbidden)
}
