//go:build integration

package integration

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/testutil"
	vtls "github.com/vortex-run/vortex/internal/tls"
)

// mtlsConfig renders a vortex.cue with a single mtls:true tcp route.
func mtlsConfig(listen, bePort int, cluster string) string {
	return `cluster: { name: "` + cluster + `" }
tls: { acme_email: "a@b.com", provider: "internal", min_version: "TLS1.2" }
routes: [{name: "db", protocol: "tcp", listen: ` + strconv.Itoa(listen) +
		`, backends: [{host: "127.0.0.1", port: ` + strconv.Itoa(bePort) + `}], mtls: true}]
security: {}
secrets: {}
observability: { log_level: "info", log_sink: "stderr" }
`
}

// startMTLSVortex starts vortex with an mtls:true route, pointing its mTLS store
// at a shared temp dir (so a test client can obtain a cert from the same cluster
// CA). It returns the listen address.
func startMTLSVortex(t *testing.T, cluster string) (addr, storeDir string) {
	t.Helper()
	storeDir = t.TempDir()
	t.Setenv("VORTEX_MTLS_STORE", storeDir)

	bin := getNetBinary(t)
	bePort := tcpEcho(t)
	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, mtlsConfig(listen, bePort, cluster))

	p := testutil.StartVortex(t, bin, cfg)
	t.Cleanup(func() { p.Stop(t) })

	addr = "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)
	return addr, storeDir
}

// clientTLS builds a client mTLS config from a RotationManager over storeDir
// using cluster — when storeDir matches vortex's store, the issued cert chains
// to the same cluster CA.
func clientTLS(t *testing.T, storeDir, cluster string) *tls.Config {
	t.Helper()
	store, err := vtls.NewStore(storeDir, []byte(cluster+"-mtls-key"))
	if err != nil {
		t.Fatal(err)
	}
	rm, err := vtls.NewRotationManager(vtls.RotationConfig{
		ClusterName: cluster, Store: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	mc, err := vtls.NewMTLSConfig(vtls.MTLSConfig{
		RotationMgr: rm, TrustDomain: rm.Identity().TrustDomain,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return mc.ClientTLSConfig()
}

func TestMTLS_PlainClientRejected(t *testing.T) {
	addr, _ := startMTLSVortex(t, "prod-cluster-1")

	// A plain TCP client (no TLS) must not get an echo: the mTLS handshake
	// fails and the connection is closed before any data reaches the backend.
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, _ = c.Write([]byte("plaintext attack"))
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 16)
	if _, rerr := c.Read(buf); rerr == nil {
		t.Error("plain client should be rejected by mTLS listener (got a response)")
	}
	_ = c.Close()

	// vortex stays healthy.
	if h := healthStatus(t); h != "ok" {
		t.Errorf("vortex health = %q after rejecting plain client, want ok", h)
	}
}

func TestMTLS_CertBearingClientAccepted(t *testing.T) {
	addr, storeDir := startMTLSVortex(t, "prod-cluster-1")

	// Client uses the SAME store + cluster, so its cert chains to vortex's CA.
	conn, err := tls.Dial("tcp", addr, clientTLS(t, storeDir, "prod-cluster-1"))
	if err != nil {
		t.Fatalf("mTLS dial should succeed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	want := []byte("hello mtls")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("echo = %q, want %q", got, want)
	}

	if h := healthStatus(t); h != "ok" {
		t.Errorf("vortex health = %q, want ok", h)
	}
}

func TestMTLS_WrongClusterRejected(t *testing.T) {
	addr, _ := startMTLSVortex(t, "prod-cluster-1")

	// Client cert comes from a DIFFERENT cluster (its own store + CA + trust
	// domain), so the handshake must fail.
	wrongStore := t.TempDir()
	conn, err := tls.Dial("tcp", addr, clientTLS(t, wrongStore, "wrong-cluster"))
	if err == nil {
		_ = conn.Close()
		t.Error("mTLS dial with wrong-cluster cert should fail")
	}

	if h := healthStatus(t); h != "ok" {
		t.Errorf("vortex health = %q after rejecting wrong cluster, want ok", h)
	}
}

// healthStatus returns the status field from vortex's /health.
func healthStatus(t *testing.T) string {
	t.Helper()
	p := &testutil.VortexProcess{APIAddr: "http://127.0.0.1:9090"}
	h := p.Health(t)
	s, _ := h["status"].(string)
	return s
}
