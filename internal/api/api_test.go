package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/config"
)

const sampleConfig = `
cluster: {name: "live-test"}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestHealthReflectsConfigReload is the live equivalent of the M1.2 manual
// check: start the server, hit /health, reload config, and confirm /health
// reports the new config hash — proving the atomic swap is visible to live
// readers without a restart.
func TestHealthReflectsConfigReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vortex.cue")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	mgr, err := config.NewManager(path, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Bind an ephemeral port to avoid clashes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := New(addr, mgr.Holder(), "test", discardLogger())
	srv.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	waitReady(t, addr)

	first := getHealth(t, addr)
	if first.Status != "ok" || first.ConfigHash == "" {
		t.Fatalf("unexpected health: %+v", first)
	}

	// Reload with a changed config.
	newCfg := `cluster: {name: "live-test-2"}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`
	if err := os.WriteFile(path, []byte(newCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reload(); err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	second := getHealth(t, addr)
	if second.ConfigHash == first.ConfigHash {
		t.Error("config hash should change after reload")
	}
	if second.ClusterName != "live-test-2" {
		t.Errorf("cluster_name = %q, want live-test-2", second.ClusterName)
	}
}

func waitReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
}

func getHealth(t *testing.T, addr string) healthResponse {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var hr healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return hr
}

func TestAICost_Endpoint(t *testing.T) {
	s, secret := newAuthedAgentServer(t, stubRuntime{})
	s.SetAICostProvider(func() AICostInfo {
		return AICostInfo{Provider: "openai", TotalUSD: 0.05, RequestsToday: 3, DailyBudget: 1, RemainingBudget: 0.95}
	})
	req := newGetReq("/api/ai/cost", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cost = %d, want 200", rec.Code)
	}
	var c AICostInfo
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	if c.Provider != "openai" || c.TotalUSD != 0.05 || c.RequestsToday != 3 {
		t.Errorf("cost = %+v", c)
	}
}

func TestAICost_RequiresAuth(t *testing.T) {
	s, _ := newAuthedAgentServer(t, stubRuntime{})
	req := newGetReq("/api/ai/cost", "")
	if rec := serve(s, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("cost without key = %d, want 401", rec.Code)
	}
}

// newGetReq builds a GET request with optional X-API-Key, loopback remote.
func newGetReq(path, key string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:5555"
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	return req
}

func TestReady_AggregatesReadinessFunc(t *testing.T) {
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())

	// Default: no readiness func → ready.
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("default /ready = %d, want 200", rec.Code)
	}

	// Not-ready func → 503 with reason.
	s.SetReadinessFunc(func() error { return errors.New("queue saturated") })
	rec = serve(s, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("not-ready /ready = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "queue saturated") {
		t.Errorf("body should include reason: %s", rec.Body.String())
	}

	// Ready func → 200 again.
	s.SetReadinessFunc(func() error { return nil })
	rec = serve(s, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("ready /ready = %d, want 200", rec.Code)
	}
}

func TestManagementServerHasTimeouts(t *testing.T) {
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	if s.srv.ReadTimeout == 0 || s.srv.WriteTimeout == 0 || s.srv.IdleTimeout == 0 {
		t.Errorf("management server missing timeouts: read=%v write=%v idle=%v",
			s.srv.ReadTimeout, s.srv.WriteTimeout, s.srv.IdleTimeout)
	}
}
