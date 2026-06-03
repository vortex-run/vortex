package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunVersionExitsZero(t *testing.T) {
	if code := run([]string{"--version"}); code != 0 {
		t.Errorf("run --version exit code = %d, want 0", code)
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	if code := run([]string{"--nope"}); code != 2 {
		t.Errorf("run with unknown flag exit code = %d, want 2", code)
	}
}

func TestRunCheckValidConfigExitsZero(t *testing.T) {
	cfg := writeTempConfig(t, validConfig)
	if code := run([]string{"--check", "--config", cfg}); code != 0 {
		t.Errorf("run --check on valid config exit code = %d, want 0", code)
	}
}

func TestRunCheckInvalidConfigExitsOne(t *testing.T) {
	cfg := writeTempConfig(t, `cluster: {name: 123}`)
	if code := run([]string{"--check", "--config", cfg}); code != 1 {
		t.Errorf("run --check on invalid config exit code = %d, want 1", code)
	}
}

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "vortex.cue")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return p
}

const validConfig = `
cluster: {name: "test-cluster", nodes: ["10.0.0.1"]}
tls: {acme_email: "a@b.com"}
routes: [{name: "web", protocol: "https", host: "x.com", backends: [{host: "127.0.0.1", port: 3000}]}]
security: {}
secrets: {keys: ["jwt"]}
observability: {}
`
