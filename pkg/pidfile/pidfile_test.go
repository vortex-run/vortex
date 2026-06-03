package pidfile

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

func TestConcurrentWriteOnlyOneSucceeds(t *testing.T) {
	// Two concurrent Write calls to a fresh path: since the existing live PID
	// (this test process) occupies the file once written, the second must fail
	// with "already running".
	p := tmpPath(t)

	const n = 8
	var wg sync.WaitGroup
	var successes int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := Write(p); err == nil {
				atomic.AddInt32(&successes, 1)
			}
		}()
	}
	wg.Wait()

	// The first writer wins; subsequent writers see our own live PID and fail.
	if got := atomic.LoadInt32(&successes); got != 1 {
		t.Errorf("concurrent Write successes = %d, want exactly 1", got)
	}
}

func TestWriteLockedReturnsValidLock(t *testing.T) {
	p := tmpPath(t)
	lock, err := WriteLocked(p)
	if err != nil {
		t.Fatalf("WriteLocked: %v", err)
	}
	if lock == nil {
		t.Fatal("WriteLocked returned nil lock")
	}
	// The pidfile should now hold our PID.
	pid, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pid, os.Getpid())
	}
	if err := lock.Unlock(); err != nil {
		t.Errorf("Unlock: %v", err)
	}
}

func TestLockBlocksSecondAcquire(t *testing.T) {
	p := tmpPath(t)
	first, err := Lock(p)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	if _, err := Lock(p); err == nil {
		t.Error("second Lock should fail while first is held")
	}
	if err := first.Unlock(); err != nil {
		t.Errorf("Unlock: %v", err)
	}
	// After release, a new lock should be acquirable.
	second, err := Lock(p)
	if err != nil {
		t.Fatalf("Lock after release should succeed: %v", err)
	}
	_ = second.Unlock()
}

func TestUnlockIsSafeToCallOnce(t *testing.T) {
	p := tmpPath(t)
	lock, err := Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Errorf("Unlock: %v", err)
	}
	// Lock file should be gone after Unlock.
	if _, err := os.Stat(lockPath(p)); !os.IsNotExist(err) {
		t.Errorf("lock file should be removed after Unlock, stat err = %v", err)
	}
}

func TestStaleFileCleanedUpAndRewritten(t *testing.T) {
	p := tmpPath(t)
	// Seed a stale (dead) PID.
	if err := os.WriteFile(p, []byte("2147483600\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(p); err != nil {
		t.Fatalf("Write over stale file: %v", err)
	}
	pid, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Errorf("after stale cleanup pid = %d, want %d", pid, os.Getpid())
	}
}
