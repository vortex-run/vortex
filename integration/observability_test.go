//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

func TestObs_MetricsEndpoint(t *testing.T) {
	bin := getNetBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(httpRoute("web", listen, beHost, bePort)))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	// Drive a few requests through the route so metrics accumulate.
	for i := 0; i < 5; i++ {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	// Scrape /metrics from the management API (localhost-reachable).
	resp, err := http.Get(p.APIAddr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	out := string(body)
	if !strings.Contains(out, "vortex_requests_total") {
		t.Errorf("/metrics missing vortex_requests_total:\n%s", out)
	}
	if !strings.Contains(out, `route="web"`) {
		t.Errorf("/metrics missing route=web label:\n%s", out)
	}
}

func TestObs_HealthIncludesRoutes(t *testing.T) {
	bin := getNetBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(httpRoute("web", listen, beHost, bePort)))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	h := p.Health(t)
	routes, ok := h["routes"].([]any)
	if !ok || len(routes) != 1 {
		t.Fatalf("health routes = %v, want one route", h["routes"])
	}
}

func TestObs_TracingDisabledGraceful(t *testing.T) {
	bin := getNetBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	// No trace_endpoint configured → tracing stays disabled; requests must still
	// succeed.
	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(httpRoute("web", listen, beHost, bePort)))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET with tracing disabled: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (tracing disabled gracefully)", resp.StatusCode)
	}

	if logs := p.Logs(); !strings.Contains(logs, "observability started") {
		t.Errorf("startup log should report observability started:\n%s", logs)
	}
}
