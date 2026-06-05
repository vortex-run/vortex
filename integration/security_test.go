//go:build integration

package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// httpRouteRL renders an http route with a rate_limit block.
func httpRouteRL(name string, listen int, beHost string, bePort, rpm, burst int) string {
	return fmt.Sprintf(`{name: %q, protocol: "http", listen: %d, backends: [{host: %q, port: %d}], rate_limit: {rpm: %d, burst: %d}}`,
		name, listen, beHost, bePort, rpm, burst)
}

// secConfig builds a config with the given routes and security block.
func secConfig(routes, security string) string {
	return `cluster: { name: "sec-test" }
tls: { acme_email: "a@b.com", provider: "internal", min_version: "TLS1.2" }
routes: [` + routes + `]
security: ` + security + `
secrets: {}
observability: { log_level: "info", log_sink: "stderr" }
`
}

func TestSecurity_RateLimitEnforced(t *testing.T) {
	bin := getNetBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	listen := testutil.FreePort(t)
	routes := httpRouteRL("web", listen, beHost, bePort, 5, 5) // rpm=5, burst=5
	cfg := testutil.WriteTestConfig(t, secConfig(routes, "{}"))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	// Fire 10 rapid requests; with burst=5 at least some must be 429.
	var ok, limited int
	for i := 0; i < 10; i++ {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if limited == 0 {
		t.Errorf("expected some 429s with burst=5 and 10 requests; ok=%d limited=%d", ok, limited)
	}
}

func TestSecurity_IPAllowlist(t *testing.T) {
	bin := getNetBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "reached")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	listen := testutil.FreePort(t)
	routes := httpRoute("web", listen, beHost, bePort)
	// Allow only loopback — the test client is 127.0.0.1, so it must reach the route.
	cfg := testutil.WriteTestConfig(t, secConfig(routes, `{ ip_allowlist: ["127.0.0.1"] }`))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "reached" {
		t.Errorf("loopback in allowlist: status=%d body=%q, want 200 'reached'", resp.StatusCode, body)
	}
	// (Blocking of non-allowlisted IPs is covered by the blocklist unit tests;
	// exercising it here would require spoofing a non-loopback source.)

	if logs := p.Logs(); !strings.Contains(logs, "security edge enabled") {
		t.Errorf("startup log should report security edge enabled:\n%s", logs)
	}
}

func TestSecurity_BlockTorFalse(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, secConfig("", `{ block_tor: false }`))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	if h := p.Health(t); h["status"] != "ok" {
		t.Errorf("health status = %v, want ok", h["status"])
	}
	logs := p.Logs()
	if strings.Contains(logs, "Tor exit") || strings.Contains(logs, "tor blocking") {
		t.Errorf("with block_tor=false, logs should not mention Tor:\n%s", logs)
	}
}
