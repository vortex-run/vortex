//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/testutil"
)

// writePolicyDir writes a single policy.rego with the given contents into a
// fresh temp dir and returns the dir path.
func writePolicyDir(t *testing.T, rego string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.rego"), []byte(rego), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestPolicy_DefaultAllowAll starts vortex with no VORTEX_POLICY_DIR; the
// built-in allow-all policy must let traffic through and the startup log must
// report the default policy.
func TestPolicy_DefaultAllowAll(t *testing.T) {
	bin := getNetBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "backend ok")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(httpRoute("web", listen, beHost, bePort)))
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
	if resp.StatusCode != http.StatusOK || string(body) != "backend ok" {
		t.Errorf("status=%d body=%q, want 200 'backend ok'", resp.StatusCode, body)
	}

	if logs := p.Logs(); !strings.Contains(logs, "policy engine loaded") ||
		!strings.Contains(logs, "default=true") {
		t.Errorf("startup log should report default allow-all policy:\n%s", logs)
	}
}

// TestPolicy_CustomPolicyDenies starts vortex with a deny-all policy directory;
// every request through the proxy must get a 403 with a JSON body.
func TestPolicy_CustomPolicyDenies(t *testing.T) {
	bin := getNetBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "should not reach backend")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	policyDir := writePolicyDir(t, "package vortex\n\ndefault allow = false\n")
	t.Setenv("VORTEX_POLICY_DIR", policyDir)

	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(httpRoute("web", listen, beHost, bePort)))
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
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (deny-all policy)", resp.StatusCode)
	}
	if !strings.Contains(string(body), "policy denied") {
		t.Errorf("403 body should contain 'policy denied': %q", body)
	}
}

// TestPolicy_ReloadPolicy starts with allow-all, then overwrites the policy with
// deny-all and reloads; requests must flip from 200 to 403.
func TestPolicy_ReloadPolicy(t *testing.T) {
	bin := getNetBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "allowed")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	policyDir := writePolicyDir(t, "package vortex\n\ndefault allow = true\n")
	t.Setenv("VORTEX_POLICY_DIR", policyDir)

	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(httpRoute("web", listen, beHost, bePort)))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	// Initially allowed.
	if code := getStatus(t, "http://"+addr+"/"); code != http.StatusOK {
		t.Fatalf("before reload: status = %d, want 200", code)
	}

	// Overwrite with deny-all and reload (portable equivalent of SIGHUP).
	if werr := os.WriteFile(filepath.Join(policyDir, "policy.rego"),
		[]byte("package vortex\n\ndefault allow = false\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if out, code := p.Run(t, "reload", "--config", cfg); code != 0 {
		t.Fatalf("reload (%d): %s", code, out)
	}
	time.Sleep(200 * time.Millisecond)

	// Now denied.
	if code := getStatus(t, "http://"+addr+"/"); code != http.StatusForbidden {
		t.Errorf("after reload: status = %d, want 403", code)
	}
}

// getStatus issues a GET and returns the status code.
func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
