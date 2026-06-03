package pidfile

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func tmpPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "vortex.pid")
}

func TestWriteCreatesFileWithCurrentPID(t *testing.T) {
	p := tmpPath(t)
	if err := Write(p); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading written pidfile: %v", err)
	}
	got := strings.TrimSpace(string(b))
	if got != strconv.Itoa(os.Getpid()) {
		t.Errorf("pidfile content = %q, want %d", got, os.Getpid())
	}
}

func TestWriteCreatesParentDirs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "dir", "vortex.pid")
	if err := Write(p); err != nil {
		t.Fatalf("Write into nested dir: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("pidfile not created in nested dir: %v", err)
	}
}

func TestWriteErrorsWhenProcessAlreadyRunning(t *testing.T) {
	p := tmpPath(t)
	// Seed the file with our own (live) PID.
	if err := os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Write(p)
	if err == nil {
		t.Fatal("expected error when process is already running")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error = %q, want 'already running'", err.Error())
	}
}

func TestWriteSucceedsOnStaleLock(t *testing.T) {
	p := tmpPath(t)
	// Seed with a PID that is almost certainly not a live process.
	if err := os.WriteFile(p, []byte("2147483600"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(p); err != nil {
		t.Fatalf("Write should overwrite a stale lock: %v", err)
	}
	pid, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Errorf("after stale overwrite pid = %d, want %d", pid, os.Getpid())
	}
}

func TestReadReturnsErrNotExist(t *testing.T) {
	_, err := Read(tmpPath(t))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Read missing file err = %v, want os.ErrNotExist", err)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	p := tmpPath(t)
	if err := Remove(p); err != nil {
		t.Errorf("Remove of missing file should be nil, got %v", err)
	}
	if err := Write(p); err != nil {
		t.Fatal(err)
	}
	if err := Remove(p); err != nil {
		t.Errorf("Remove of existing file: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("file should be gone after Remove, stat err = %v", err)
	}
}

func TestIsRunningTrueForCurrentProcess(t *testing.T) {
	p := tmpPath(t)
	if err := Write(p); err != nil {
		t.Fatal(err)
	}
	alive, pid, err := IsRunning(p)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if !alive {
		t.Error("current process should be reported alive")
	}
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pid, os.Getpid())
	}
}

func TestIsRunningFalseForStalePID(t *testing.T) {
	p := tmpPath(t)
	if err := os.WriteFile(p, []byte("2147483600"), 0o644); err != nil {
		t.Fatal(err)
	}
	alive, _, err := IsRunning(p)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if alive {
		t.Error("stale PID should not be reported alive")
	}
}
