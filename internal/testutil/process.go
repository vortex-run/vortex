//go:build integration

// Package testutil provides helpers for VORTEX's integration tests: building
// the binary, starting and stopping a real vortex process, and talking to its
// management API. It is compiled only under the `integration` build tag so it
// never bloats normal builds.
//
// The management API currently binds a fixed port (api.DefaultAddr, :9090), so
// integration tests that start a server run serially rather than in parallel.
package testutil

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// apiBase is the management API address the started server listens on. It
// matches api.DefaultAddr; the server does not yet take the address from config.
const apiBase = "http://127.0.0.1:9090"

// BuildBinary compiles the vortex binary into a temp directory and returns its
// path. The build runs from the repository root (two levels up from this
// package). The binary is removed when the test finishes.
func BuildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "vortex"
	if runtime.GOOS == "windows" {
		name = "vortex.exe"
	}
	bin := filepath.Join(dir, name)

	// repoRoot: this file lives at <root>/internal/testutil, so the module root
	// is two directories up.
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/vortex")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building vortex binary: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(bin) })
	return bin
}

// VortexProcess is a running vortex server under test.
type VortexProcess struct {
	Cmd        *exec.Cmd
	ConfigPath string
	APIAddr    string
	BinaryPath string
	stopped    bool
}

// StartVortex starts `<bin> start --config <configPath> --log-level debug`,
// waits for /health to answer 200 (up to 5s), and returns the process. The
// process is stopped on test cleanup.
func StartVortex(t *testing.T, bin, configPath string) *VortexProcess {
	t.Helper()
	cmd := exec.Command(bin, "start", "--config", configPath, "--log-level", "debug")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting vortex: %v", err)
	}
	p := &VortexProcess{
		Cmd:        cmd,
		ConfigPath: configPath,
		APIAddr:    apiBase,
		BinaryPath: bin,
	}
	t.Cleanup(func() {
		if !p.stopped {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(apiBase + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("vortex did not become healthy within 5s")
	return nil
}

// Stop stops the running process and waits up to 10s for it to exit cleanly.
func (p *VortexProcess) Stop(t *testing.T) {
	t.Helper()
	if p.stopped {
		return
	}
	stopProcess(t, p)
	p.stopped = true
}

// Health fetches and parses GET /health, returning the JSON body as a map.
func (p *VortexProcess) Health(t *testing.T) map[string]any {
	t.Helper()
	resp, err := http.Get(p.APIAddr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading /health body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("parsing /health JSON: %v\n%s", err, body)
	}
	return m
}

// Run executes the vortex binary with args and returns combined output and the
// exit code. It does not fail the test on a non-zero exit — the caller decides.
func (p *VortexProcess) Run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	return RunBinary(t, p.BinaryPath, args...)
}

// RunBinary runs bin with args, returning combined stdout+stderr and the exit
// code. A 30s context guards against hangs.
func RunBinary(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return string(out), code
}

// WriteTestConfig writes content to a temp file and returns its path; the file
// is removed on cleanup.
func WriteTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "vortex.cue")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return p
}
