package cmd

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestRemoveOwnPIDFile: shutdown removes the pidfile only while it still
// names this process, never another instance's.
func TestRemoveOwnPIDFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gubern.pid")

	if err := os.WriteFile(p, []byte("1\n"), 0o644); err != nil { // someone else's
		t.Fatal(err)
	}
	if err := removeOwnPIDFile(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("removed a pidfile that names another process")
	}

	if err := os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnPIDFile(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("own pidfile not removed")
	}
	if err := removeOwnPIDFile(p); err != nil { // already gone
		t.Errorf("missing pidfile: %v", err)
	}
}

// TestWritePIDFile_RefusesLiveOwner: a second start must not overwrite a
// running instance's pidfile.
func TestWritePIDFile_RefusesLiveOwner(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gubern.pid")
	if err := os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writePIDFile(p); err == nil {
		t.Fatal("writePIDFile succeeded over a live owner's pidfile")
	}
}
