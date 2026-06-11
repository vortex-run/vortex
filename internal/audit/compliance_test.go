package audit

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func complianceLog(t *testing.T) *Log {
	t.Helper()
	l, err := NewLog(filepath.Join(t.TempDir(), "audit.log"), []byte("compliance-key"))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func appendEvents(t *testing.T, l *Log) {
	t.Helper()
	ctx := context.Background()
	events := []struct{ actor, action, resource string }{
		{"admin", "config.reload", "vortex.cue"},
		{"admin", "secret.set", "db_password"},
		{"cli", "secret.set", "jwt_secret"},
		{"10.0.0.9", "auth.failure", "/api/keys"},
		{"10.0.0.9", "policy.deny", "route-admin"},
		{"admin", "apikey.create", "key-123"},
	}
	for _, e := range events {
		if err := l.Append(ctx, e.actor, e.action, e.resource, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestComplianceReport_CountsEvents(t *testing.T) {
	l := complianceLog(t)
	appendEvents(t, l)

	r, err := GenerateComplianceReport(l, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if r.TotalEvents != 6 {
		t.Errorf("TotalEvents = %d, want 6", r.TotalEvents)
	}
	if r.EventsByActor["admin"] != 3 {
		t.Errorf("EventsByActor[admin] = %d, want 3", r.EventsByActor["admin"])
	}
	if r.EventsByAction["secret.set"] != 2 {
		t.Errorf("EventsByAction[secret.set] = %d, want 2", r.EventsByAction["secret.set"])
	}
	if len(r.SecurityEvents) != 2 {
		t.Errorf("SecurityEvents = %d, want 2 (auth.failure + policy.deny)", len(r.SecurityEvents))
	}
	if len(r.AdminActions) != 4 {
		t.Errorf("AdminActions = %d, want 4 (config/secret×2/apikey)", len(r.AdminActions))
	}
}

func TestComplianceReport_PeriodFilter(t *testing.T) {
	l := complianceLog(t)
	appendEvents(t, l)

	// A window entirely in the past matches nothing.
	r, err := GenerateComplianceReport(l,
		time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if r.TotalEvents != 0 {
		t.Errorf("TotalEvents = %d, want 0 for past window", r.TotalEvents)
	}
	if r.Period == "" {
		t.Error("Period should be rendered")
	}
}

func TestComplianceReport_ChainValidUntampered(t *testing.T) {
	l := complianceLog(t)
	appendEvents(t, l)

	r, err := GenerateComplianceReport(l, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.ChainValid {
		t.Error("ChainValid should be true for an untampered log")
	}
}

func TestComplianceReport_ChainInvalidTampered(t *testing.T) {
	l := complianceLog(t)
	appendEvents(t, l)

	// Tamper: rewrite an actor in place on disk.
	b, err := os.ReadFile(l.path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(b, []byte(`"actor":"admin"`), []byte(`"actor":"mallory"`), 1)
	if bytes.Equal(b, tampered) {
		t.Fatal("tamper substitution did not apply")
	}
	if err := os.WriteFile(l.path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := GenerateComplianceReport(l, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if r.ChainValid {
		t.Error("ChainValid should be false for a tampered log")
	}
}

func TestComplianceReport_WriteMarkdown(t *testing.T) {
	l := complianceLog(t)
	appendEvents(t, l)
	r, err := GenerateComplianceReport(l, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := r.WriteMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"# VORTEX Compliance Report",
		"**Total events:** 6",
		"## Events by actor",
		"## Security events",
		"auth.failure",
		"## Administrative actions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown report missing %q", want)
		}
	}
}

func TestComplianceReport_WriteJSON(t *testing.T) {
	l := complianceLog(t)
	appendEvents(t, l)
	r, err := GenerateComplianceReport(l, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var decoded ComplianceReport
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON report does not round-trip: %v", err)
	}
	if decoded.TotalEvents != 6 || !decoded.ChainValid {
		t.Errorf("decoded report = %+v", decoded)
	}
}

func TestComplianceReport_WriteCSV(t *testing.T) {
	l := complianceLog(t)
	appendEvents(t, l)
	r, err := GenerateComplianceReport(l, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := r.WriteCSV(&buf); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("CSV report does not parse: %v", err)
	}
	if len(rows) < 7 {
		t.Fatalf("CSV report too short: %d rows", len(rows))
	}
	if got := rows[0]; got[0] != "section" || got[1] != "key" || got[2] != "value" {
		t.Errorf("CSV header = %v", got)
	}
}

func TestArchive_RotatesAndRestartsChain(t *testing.T) {
	l := complianceLog(t)
	appendEvents(t, l)

	dest, err := l.Archive()
	if err != nil {
		t.Fatal(err)
	}
	if dest == "" {
		t.Fatal("expected an archive path")
	}
	if !strings.HasPrefix(filepath.Base(dest), "audit-") || !strings.HasSuffix(dest, ".log.gz") {
		t.Errorf("archive name = %s, want audit-<year>-<month>.log.gz", filepath.Base(dest))
	}

	// The archive holds the six original entries, gzipped NDJSON.
	f, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := os.ReadFile(l.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 0 {
		t.Errorf("live log should be empty after archive, has %d bytes", len(archived))
	}
	var content bytes.Buffer
	if _, err := content.ReadFrom(gz); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(content.String(), "\n"); lines != 6 {
		t.Errorf("archive holds %d entries, want 6", lines)
	}

	// New entries restart the chain at seq 1 and verify cleanly.
	if err := l.Append(context.Background(), "cli", "secret.set", "fresh", nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(); err != nil {
		t.Errorf("fresh log after archive should verify: %v", err)
	}
	entries, err := l.Query(QueryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Seq != 1 {
		t.Errorf("fresh log entries = %+v, want one entry with seq 1", entries)
	}
}

func TestArchive_EmptyLogIsNoOp(t *testing.T) {
	l := complianceLog(t)
	dest, err := l.Archive()
	if err != nil {
		t.Fatal(err)
	}
	if dest != "" {
		t.Errorf("empty log should not archive, got %s", dest)
	}
}

func TestArchiveIfOver_Threshold(t *testing.T) {
	l := complianceLog(t)
	appendEvents(t, l)

	// Far above the current size: no rotation.
	if _, rotated, err := l.ArchiveIfOver(1 << 30); err != nil || rotated {
		t.Errorf("rotated=%v err=%v, want no rotation under threshold", rotated, err)
	}
	// One byte: must rotate.
	dest, rotated, err := l.ArchiveIfOver(1)
	if err != nil {
		t.Fatal(err)
	}
	if !rotated || dest == "" {
		t.Errorf("rotated=%v dest=%q, want rotation over threshold", rotated, dest)
	}
}

func TestAppend_AutoArchivesOverThreshold(t *testing.T) {
	// Drop the effective threshold by calling ArchiveIfOver from Append's
	// behaviour: simulate by writing entries until over a tiny limit via
	// ArchiveIfOver, then confirm Append continues a valid chain afterwards.
	l := complianceLog(t)
	appendEvents(t, l)
	if _, _, err := l.ArchiveIfOver(1); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(context.Background(), "cli", "config.reload", "vortex.cue", nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(); err != nil {
		t.Errorf("log should verify after auto-archive + append: %v", err)
	}
}

func TestRekey_RecomputesChainUnderNewKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	old, err := NewLog(path, []byte("old-cluster-audit-key"))
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, old)
	if !old.Verifies() {
		t.Fatal("log should verify under its original key")
	}

	if err := old.Rekey([]byte("new-master-audit-key")); err != nil {
		t.Fatal(err)
	}

	// New key verifies; old key does not.
	fresh, _ := NewLog(path, []byte("new-master-audit-key"))
	if !fresh.Verifies() {
		t.Error("log should verify under the new key after rekey")
	}
	stale, _ := NewLog(path, []byte("old-cluster-audit-key"))
	if stale.Verifies() {
		t.Error("log should NOT verify under the old key after rekey")
	}

	// Entries (seq/actor/action) are preserved; only hashes changed.
	entries, err := fresh.Query(QueryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Errorf("rekey changed entry count: %d, want 6", len(entries))
	}

	// Appending after rekey continues the new chain.
	if err := fresh.Append(context.Background(), "cli", "config.reload", "x", nil); err != nil {
		t.Fatal(err)
	}
	if !fresh.Verifies() {
		t.Error("appends after rekey should verify")
	}
}

func TestRekey_RefusesTamperedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := NewLog(path, []byte("k"))
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, l)

	// Tamper on disk.
	b, _ := os.ReadFile(path)
	tampered := bytes.Replace(b, []byte(`"actor":"admin"`), []byte(`"actor":"evil"`), 1)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := l.Rekey([]byte("new")); err == nil {
		t.Error("Rekey should refuse a tampered log")
	}
}

func TestRekey_EmptyLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, _ := NewLog(path, []byte("k"))
	if err := l.Rekey([]byte("new")); err != nil {
		t.Errorf("rekey of empty log should succeed: %v", err)
	}
}
