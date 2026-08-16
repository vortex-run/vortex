package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEntries appends n entries to a log at path under key.
func writeEntries(t *testing.T, path string, key []byte, n int) {
	t.Helper()
	l, err := NewLog(path, key)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := l.Append(context.Background(), "cli", "secret.set", "K", nil); err != nil {
			t.Fatal(err)
		}
	}
}

// TestVerify_WrongKeyIsNotReportedAsTampering is the core distinction: a log
// read with the wrong key fails at entry 1, which is exactly what a modified
// first entry looks like. Reporting that as "entry modified" accuses an
// operator of tampering when the usual cause is a key mismatch (renamed
// cluster, restored backup, re-key migration that could not run).
func TestVerify_WrongKeyIsNotReportedAsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writeEntries(t, path, []byte("original-key"), 3)

	wrong, err := NewLog(path, []byte("a-different-key"))
	if err != nil {
		t.Fatal(err)
	}
	verr := wrong.Verify()
	if verr == nil {
		t.Fatal("verification passed under the wrong key")
	}
	if !errors.Is(verr, ErrChainUnverified) {
		t.Errorf("error = %v, want it to wrap ErrChainUnverified", verr)
	}
	if strings.Contains(verr.Error(), "entry modified") {
		t.Errorf("wrong-key failure accuses tampering: %v", verr)
	}
	if !strings.Contains(verr.Error(), "different key") {
		t.Errorf("error should name the key-mismatch possibility: %v", verr)
	}
}

// TestVerify_RealTamperingPastEntryOneStillAccuses is the other half. Once
// entry 1 verifies, the key is proven correct, so a later mismatch really is
// modification and must still be reported as such — the softer wording must
// not weaken a genuine tampering signal.
func TestVerify_RealTamperingPastEntryOneStillAccuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	key := []byte("k")
	writeEntries(t, path, key, 3)

	// Corrupt the resource field of entry 2, leaving entry 1 intact.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 entries, got %d", len(lines))
	}
	lines[1] = strings.Replace(lines[1], `"resource":"K"`, `"resource":"HACKED"`, 1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := NewLog(path, key)
	if err != nil {
		t.Fatal(err)
	}
	verr := l.Verify()
	if verr == nil {
		t.Fatal("tampering at entry 2 was not detected")
	}
	if errors.Is(verr, ErrChainUnverified) {
		t.Errorf("real tampering past entry 1 must NOT be softened to unverified: %v", verr)
	}
	if !strings.Contains(verr.Error(), "entry modified") {
		t.Errorf("error = %v, want it to report modification", verr)
	}
}

// TestVerify_TamperedFirstEntryIsAmbiguous documents the honest limit: a
// modified entry 1 is indistinguishable from a wrong key by the hash alone, so
// it reports the ambiguity rather than guessing.
func TestVerify_TamperedFirstEntryIsAmbiguous(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	key := []byte("k")
	writeEntries(t, path, key, 2)

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	lines[0] = strings.Replace(lines[0], `"resource":"K"`, `"resource":"HACKED"`, 1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	l, _ := NewLog(path, key)
	verr := l.Verify()
	if verr == nil {
		t.Fatal("modified entry 1 was not detected")
	}
	// Still detected — only the attribution is hedged, never the detection.
	if !errors.Is(verr, ErrChainUnverified) {
		t.Errorf("entry-1 mismatch should be reported as unverified: %v", verr)
	}
}
