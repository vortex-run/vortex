package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestDownloadWritesContent(t *testing.T) {
	payload := []byte("vortex release payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := Download(context.Background(), srv.URL, dest, sha256Hex(payload), nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, payload) {
		t.Errorf("content mismatch: got %q", got)
	}
}

func TestDownloadRemovesOnChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	err := Download(context.Background(), srv.URL, dest, "deadbeef", nil)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("dest should be removed on mismatch, stat err = %v", statErr)
	}
}

func TestDownloadRemovesOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the request runs
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := Download(ctx, srv.URL, dest, "abc", nil); err == nil {
		t.Fatal("expected error on cancelled context")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("dest should be removed on cancel, stat err = %v", statErr)
	}
}

// makeTarGz builds a .tar.gz containing one entry name→content.
func makeTarGz(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, "archive.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))})
	_, _ = tw.Write(content)
	_ = tw.Close()
	_ = gz.Close()
	return path
}

func TestExtractTarGz(t *testing.T) {
	dir := t.TempDir()
	archive := makeTarGz(t, dir, "vortex", []byte("BINARY"))
	out, err := Extract(archive, dir, "vortex")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "BINARY" {
		t.Errorf("extracted content = %q", got)
	}
}

func TestExtractRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	archive := makeTarGz(t, dir, "../escape", []byte("x"))
	if _, err := Extract(archive, dir, "escape"); err == nil {
		t.Fatal("expected zip-slip rejection for '../escape'")
	}
}

func TestAtomicReplaceSwapsFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "vortex")
	newBin := filepath.Join(dir, "new")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newBin, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicReplace(newBin, target); err != nil {
		t.Fatalf("AtomicReplace: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "NEW" {
		t.Errorf("target content = %q, want NEW", got)
	}
	if _, err := os.Stat(target + ".bak"); err != nil {
		t.Errorf(".bak should exist after AtomicReplace: %v", err)
	}
}

func TestRollbackRestores(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "vortex")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(dir, "new")
	if err := os.WriteFile(newBin, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicReplace(newBin, target); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(target); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "OLD" {
		t.Errorf("after rollback target = %q, want OLD", got)
	}
}

func TestRollbackIdempotentNoBak(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "vortex")
	if err := os.WriteFile(target, []byte("CURRENT"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(target); err != nil {
		t.Errorf("Rollback with no .bak should be nil, got %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "CURRENT" {
		t.Errorf("target should be untouched, got %q", got)
	}
}
