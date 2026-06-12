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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

	repoRoot := moduleRoot(t)

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/vortex")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building vortex binary: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(bin) })
	return bin
}

// BuildBinaryInto compiles the vortex binary into destDir and returns its path,
// WITHOUT registering any per-test cleanup. Callers own destDir's lifetime —
// use this from TestMain to build a binary once and share it across a whole
// suite (BuildBinary ties the binary to a single test via t.TempDir/t.Cleanup,
// which deletes it when that one test finishes).
func BuildBinaryInto(destDir string) (string, error) {
	name := "vortex"
	if runtime.GOOS == "windows" {
		name = "vortex.exe"
	}
	bin := filepath.Join(destDir, name)

	root, err := moduleRootFromWD()
	if err != nil {
		return "", err
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/vortex")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building vortex binary: %w\n%s", err, out)
	}
	return bin, nil
}

// moduleRootFromWD is the non-testing form of moduleRoot for use outside a test.
func moduleRootFromWD() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate go.mod above %s", dir)
		}
		dir = parent
	}
}

// moduleRoot walks up from the test's working directory until it finds the
// directory containing go.mod, returning its absolute path.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod above %s", dir)
		}
		dir = parent
	}
}

// VortexProcess is a running vortex server under test.
type VortexProcess struct {
	Cmd        *exec.Cmd
	ConfigPath string
	APIAddr    string
	BinaryPath string
	stopped    bool
	logBuf     syncBuffer
}

// Logs returns the child process's captured stdout+stderr so far.
func (p *VortexProcess) Logs() string { return p.logBuf.String() }

// syncBuffer is a goroutine-safe bytes buffer: the child process writes to it
// concurrently while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// StartVortex starts `<bin> start --config <configPath> --log-level debug`,
// waits for /health to answer 200 (up to 5s), and returns the process. The
// process is stopped on test cleanup.
func StartVortex(t *testing.T, bin, configPath string) *VortexProcess {
	t.Helper()
	// The management API binds a fixed port (:9090). Wait for any previous
	// test's process to release it, so this process — not a lingering stale one
	// — is the one that answers /health. Without this, back-to-back tests can
	// observe the wrong server's responses (e.g. one without Studio wired).
	waitPortFree(t, "127.0.0.1:9090", 10*time.Second)

	cmd := exec.Command(bin, "start", "--config", configPath, "--log-level", "debug")
	// Isolate the user config dir: the developer's real ai-provider.json (from
	// `vortex setup`) must not leak into the child, or gateway-dependent tests
	// (e.g. forge-disabled-without-AI) behave differently per machine.
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"AppData="+t.TempDir(),
		"APPDATA="+t.TempDir(),
	)
	p := &VortexProcess{
		ConfigPath: configPath,
		APIAddr:    apiBase,
		BinaryPath: bin,
	}
	// Tee child output to both os.Stderr (visible while debugging) and an
	// in-memory buffer so tests can assert on startup log lines.
	out := io.MultiWriter(os.Stderr, &p.logBuf)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting vortex: %v", err)
	}
	p.Cmd = cmd
	t.Cleanup(func() {
		if !p.stopped {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	// Readiness is tied to THIS child's own log output, not just a /health 200
	// (a stale predecessor still holding :9090 would also answer 200, yielding
	// the wrong server). The management API starts serving before the later
	// subsystems (studio, agents, webhooks) are wired onto it, so waiting for
	// "management API listening" alone races tests that hit those endpoints
	// immediately. "VORTEX started" is logged only after all wiring completes.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.logBuf.String(), "VORTEX started") {
			resp, err := http.Get(apiBase + "/health")
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return p
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("vortex did not become healthy within 15s:\n%s", p.logBuf.String())
	return nil
}

// waitPortFree blocks until addr can be bound (a prior server has fully released
// it, including any TIME_WAIT) or the timeout elapses. It actually listens on
// the port rather than dialing, so a predecessor in TIME_WAIT does not pass.
func waitPortFree(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			_ = ln.Close()
			// Briefly let the kernel release the just-closed listener before the
			// child binds it.
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Don't fail hard: the start readiness check surfaces a real problem.
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

// MarkStopped records that the process has already exited (e.g. via the
// /internal/shutdown endpoint) so the cleanup hook does not try to kill it.
func (p *VortexProcess) MarkStopped() {
	p.stopped = true
	// Reap the child so it does not linger as a zombie.
	go func() { _, _ = p.Cmd.Process.Wait() }()
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
