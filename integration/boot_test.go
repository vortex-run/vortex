//go:build integration

// Package integration holds VORTEX's end-to-end tests: they build the real
// binary, start it as a child process, and exercise its CLI and HTTP API. They
// run only under the `integration` build tag (see the CI integration job and
// `task test:integration`).
package integration

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestBoot_StartsAndServesHealth(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)

	h := p.Health(t)
	if h["status"] != "ok" {
		t.Errorf("status = %v, want ok", h["status"])
	}
	if h["cluster_name"] != "test-cluster" {
		t.Errorf("cluster_name = %v, want test-cluster", h["cluster_name"])
	}
	if _, ok := h["version"]; !ok {
		t.Error("health response missing version field")
	}
	if _, ok := h["uptime"]; !ok {
		t.Error("health response missing uptime field")
	}

	p.Stop(t) // asserts clean exit (code 0) internally
}

func TestBoot_HealthHasCorrelationID(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	id := resp.Header.Get("X-Correlation-Id")
	if id == "" {
		t.Fatal("X-Correlation-Id header missing")
	}
	if !hex32.MatchString(id) {
		t.Errorf("X-Correlation-Id %q is not 32-char hex", id)
	}
}

func TestBoot_CorrelationIDPassThrough(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/health", nil)
	req.Header.Set("X-Correlation-ID", "my-trace-abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("X-Correlation-Id"); got != "my-trace-abc" {
		t.Errorf("correlation id = %q, want my-trace-abc", got)
	}
}

func TestBoot_RejectsInvalidConfig(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.InvalidConfig)

	out, code := testutil.RunBinary(t, bin, "start", "--config", cfg)
	if code != 1 {
		t.Errorf("exit code = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "name") {
		t.Errorf("output should mention invalid field 'name':\n%s", out)
	}

	// After a failed start, nothing should be listening on the API port.
	if resp, err := http.Get("http://127.0.0.1:9090/health"); err == nil {
		_ = resp.Body.Close()
		t.Error("server should not be listening after invalid-config failure")
	}
}

func TestBoot_CheckCommandValid(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	out, code := testutil.RunBinary(t, bin, "check", "--config", cfg)
	if code != 0 {
		t.Errorf("exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "is valid") {
		t.Errorf("output should contain 'is valid':\n%s", out)
	}
}

func TestBoot_CheckCommandInvalid(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.InvalidConfig)
	out, code := testutil.RunBinary(t, bin, "check", "--config", cfg)
	if code != 1 {
		t.Errorf("exit code = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "name") {
		t.Errorf("output should mention field 'name':\n%s", out)
	}
}

func TestBoot_VersionCommand(t *testing.T) {
	bin := testutil.BuildBinary(t)
	out, code := testutil.RunBinary(t, bin, "version")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	for _, want := range []string{"Version:", "Go version:", "OS/Arch:"} {
		if !strings.Contains(out, want) {
			t.Errorf("version output missing %q:\n%s", want, out)
		}
	}
}

func TestBoot_VersionShort(t *testing.T) {
	bin := testutil.BuildBinary(t)
	out, code := testutil.RunBinary(t, bin, "version", "--short")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	trimmed := strings.TrimRight(out, "\n")
	if strings.Contains(trimmed, "\n") {
		t.Errorf("--short should be a single line:\n%s", out)
	}
	if strings.Contains(trimmed, " ") {
		t.Errorf("--short output should have no spaces: %q", trimmed)
	}
}
