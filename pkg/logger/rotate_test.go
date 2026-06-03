package logger

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestRotator(t *testing.T, cfg RotateWriter) *RotateWriter {
	t.Helper()
	if cfg.Path == "" {
		cfg.Path = filepath.Join(t.TempDir(), "vortex.log")
	}
	r, err := NewRotateWriter(cfg)
	if err != nil {
		t.Fatalf("NewRotateWriter: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestNewRotateWriterCreatesFile(t *testing.T) {
	r := newTestRotator(t, RotateWriter{})
	if _, err := os.Stat(r.Path); err != nil {
		t.Errorf("log file not created: %v", err)
	}
}

func TestRotateWriterWriteAppends(t *testing.T) {
	r := newTestRotator(t, RotateWriter{})
	if _, err := r.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "first\nsecond\n" {
		t.Errorf("content = %q, want %q", string(b), "first\nsecond\n")
	}
}

func TestRotateRenamesAndCreatesFresh(t *testing.T) {
	r := newTestRotator(t, RotateWriter{Compress: false})
	if _, err := r.Write([]byte("data-before-rotate\n")); err != nil {
		t.Fatal(err)
	}
	if err := r.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Fresh active file exists and is empty.
	info, err := os.Stat(r.Path)
	if err != nil {
		t.Fatalf("fresh log file missing: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("fresh file should be empty, size = %d", info.Size())
	}

	// Exactly one backup exists with the original content.
	backups := r.backups()
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %d: %v", len(backups), backups)
	}
	b, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "data-before-rotate\n" {
		t.Errorf("backup content = %q, want original", string(b))
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	r := newTestRotator(t, RotateWriter{})
	if err := r.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close should be nil, got: %v", err)
	}
}

func TestMaxBackupsEnforced(t *testing.T) {
	r := newTestRotator(t, RotateWriter{Compress: false, MaxBackups: 2})
	// Three rotations → three backups created, but only 2 kept.
	for i := 0; i < 3; i++ {
		if _, err := r.Write([]byte("x\n")); err != nil {
			t.Fatal(err)
		}
		if err := r.Rotate(); err != nil {
			t.Fatalf("Rotate %d: %v", i, err)
		}
	}
	if got := len(r.backups()); got > 2 {
		t.Errorf("backups = %d, want <= MaxBackups (2)", got)
	}
}

func TestSizeTriggeredRotation(t *testing.T) {
	r := newTestRotator(t, RotateWriter{Compress: false, MaxSizeMB: 1})
	// Write just over 1 MiB to trip the size threshold.
	chunk := make([]byte, 256*1024)
	for i := range chunk {
		chunk[i] = 'a'
	}
	for i := 0; i < 5; i++ {
		if _, err := r.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.backups()) == 0 {
		t.Error("expected at least one size-triggered rotation")
	}
}
