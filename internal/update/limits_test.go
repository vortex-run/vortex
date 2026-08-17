package update

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLimited_AcceptsUpToTheLimit(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 100)
	got, err := readLimited(bytes.NewReader(body), 100, "thing")
	if err != nil {
		t.Fatalf("a body exactly at the limit was rejected: %v", err)
	}
	if len(got) != 100 {
		t.Errorf("read %d bytes, want 100", len(got))
	}
}

// TestReadLimited_RejectsRatherThanTruncates matters for correctness, not just
// safety: a silently truncated checksums.txt or signature would fail
// verification later with a confusing mismatch, hiding the real cause.
func TestReadLimited_RejectsRatherThanTruncates(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 101)
	got, err := readLimited(bytes.NewReader(body), 100, "checksums.txt")
	if err == nil {
		t.Fatalf("oversized body accepted, returning %d bytes", len(got))
	}
	if !strings.Contains(err.Error(), "checksums.txt") {
		t.Errorf("error should name the artifact: %v", err)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error should name the limit so an operator knows what to raise: %v", err)
	}
}

func TestCopyLimited_RejectsOversizedSource(t *testing.T) {
	var dst bytes.Buffer
	_, err := copyLimited(&dst, bytes.NewReader(bytes.Repeat([]byte("y"), 50)), 10, "artifact")
	if err == nil {
		t.Fatal("copyLimited accepted a source over its limit")
	}
}

func TestCopyLimited_PassesThroughUnderLimit(t *testing.T) {
	var dst bytes.Buffer
	n, err := copyLimited(&dst, strings.NewReader("hello"), 1000, "artifact")
	if err != nil {
		t.Fatalf("under-limit copy failed: %v", err)
	}
	if n != 5 || dst.String() != "hello" {
		t.Errorf("copied %d bytes = %q, want 5 = hello", n, dst.String())
	}
}

// TestExtract_RejectsDecompressionBomb is the regression that matters. The
// extraction path carried a comment asserting the size was "bounded by release
// archive"; it is not. A zip entry expands to whatever its author chose, so a
// tiny download could fill the disk of every node that self-updates — with the
// release host being exactly what the signed-update work assumes is hostile.
func TestExtract_RejectsDecompressionBomb(t *testing.T) {
	// Build a zip whose entry expands far beyond the archive itself. Kept
	// modest so the test is quick; the ratio is what matters, and it is the
	// same mechanism at any size.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("vortex")
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20) // 1 MiB of zeros compresses to almost nothing
	for i := 0; i < 8; i++ {
		if _, werr := w.Write(chunk); werr != nil {
			t.Fatal(werr)
		}
	}
	if cerr := zw.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()

	// Extract with a deliberately small ceiling: the real limit is 512 MiB,
	// which would make this test enormous, so this exercises the same guard at
	// a testable size.
	dest := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if _, err := copyLimited(f, rc, 1<<20, "extracted file"); err == nil {
		t.Fatal("an 8 MiB expansion was written under a 1 MiB ceiling; " +
			"the archive size does not bound what a zip entry expands to")
	}
}

// TestExtract_AllowsALegitimateBinary guards the other direction: the limits
// must not reject a real release. The vortex binary is ~71 MB against a 512 MiB
// ceiling, so a file several times larger than today's must still pass.
func TestExtract_AllowsALegitimateBinary(t *testing.T) {
	const realistic = 80 << 20 // comfortably larger than the current binary
	if maxArtifactBytes <= realistic {
		t.Fatalf("maxArtifactBytes (%d) leaves no headroom over a realistic %d-byte binary",
			maxArtifactBytes, realistic)
	}
	// And the metadata ceiling must dwarf a real checksums file.
	if maxMetadataBytes < 64<<10 {
		t.Errorf("maxMetadataBytes (%d) is too tight for a full platform matrix", maxMetadataBytes)
	}
}

// TestReadLimited_EmptyBodyIsFine covers the boundary — an empty response is
// an error for the caller to interpret, not something the limit should reject.
func TestReadLimited_EmptyBodyIsFine(t *testing.T) {
	got, err := readLimited(strings.NewReader(""), maxMetadataBytes, "signature")
	if err != nil {
		t.Fatalf("empty body rejected by the size guard: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d bytes from an empty body", len(got))
	}
}
