package audit

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Archival policy (build plan M19): the live log is rotated into a gzipped
// archive once it exceeds ArchiveThreshold, and archives older than
// ArchiveRetention are pruned.
const (
	ArchiveThreshold = 100 * 1024 * 1024 // 100MB
	ArchiveRetention = 2 * 365 * 24 * time.Hour
)

// ComplianceReport summarises a period of audit activity for compliance
// review: event volumes, chain integrity, and the security-relevant and
// administrative entries themselves.
type ComplianceReport struct {
	Period         string         `json:"period"`
	TotalEvents    int            `json:"total_events"`
	ChainValid     bool           `json:"chain_valid"`
	EventsByActor  map[string]int `json:"events_by_actor"`
	EventsByAction map[string]int `json:"events_by_action"`
	SecurityEvents []Entry        `json:"security_events"` // auth failures, policy denials
	AdminActions   []Entry        `json:"admin_actions"`   // config changes, secret/key/namespace ops
	GeneratedAt    time.Time      `json:"generated_at"`
}

// GenerateComplianceReport builds a ComplianceReport over entries with
// timestamps in [since, until]. Zero bounds are open-ended. ChainValid
// reflects Verify over the whole live log, not just the reported window —
// a tampered entry outside the window still invalidates the log.
func GenerateComplianceReport(log *Log, since, until time.Time) (*ComplianceReport, error) {
	entries, err := log.Query(QueryFilter{Since: since, Until: until})
	if err != nil {
		return nil, err
	}

	r := &ComplianceReport{
		Period:         formatPeriod(since, until),
		TotalEvents:    len(entries),
		ChainValid:     log.Verify() == nil,
		EventsByActor:  make(map[string]int),
		EventsByAction: make(map[string]int),
		GeneratedAt:    time.Now().UTC(),
	}
	for _, e := range entries {
		r.EventsByActor[e.Actor]++
		r.EventsByAction[e.Action]++
		if isSecurityEvent(e) {
			r.SecurityEvents = append(r.SecurityEvents, e)
		}
		if isAdminAction(e) {
			r.AdminActions = append(r.AdminActions, e)
		}
	}
	return r, nil
}

// formatPeriod renders the report window as "<since> to <until>" with
// open-ended bounds shown as "beginning"/"now".
func formatPeriod(since, until time.Time) string {
	from, to := "beginning", "now"
	if !since.IsZero() {
		from = since.UTC().Format("2006-01-02")
	}
	if !until.IsZero() {
		to = until.UTC().Format("2006-01-02")
	}
	return from + " to " + to
}

// isSecurityEvent reports whether an entry records a security signal: an
// authentication/authorization event or a denial/failure of any kind.
func isSecurityEvent(e Entry) bool {
	return strings.HasPrefix(e.Action, "auth.") ||
		strings.HasPrefix(e.Action, "policy.") ||
		strings.Contains(e.Action, ".deny") ||
		strings.Contains(e.Action, ".fail")
}

// isAdminAction reports whether an entry records an administrative change:
// configuration, secret, API-key, namespace, cluster, or plugin operations.
func isAdminAction(e Entry) bool {
	for _, p := range []string{"config.", "secret.", "apikey.", "namespace.", "cluster.", "plugin."} {
		if strings.HasPrefix(e.Action, p) {
			return true
		}
	}
	return e.Action == "reload" || e.Action == "shutdown"
}

// WriteMarkdown renders the report as a human-readable Markdown document.
func (r *ComplianceReport) WriteMarkdown(w io.Writer) error {
	chain := "✓ VALID — no tampering detected"
	if !r.ChainValid {
		chain = "✗ BROKEN — the log has been tampered with"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# VORTEX Compliance Report\n\n")
	fmt.Fprintf(&b, "- **Period:** %s\n", r.Period)
	fmt.Fprintf(&b, "- **Generated:** %s\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- **Total events:** %d\n", r.TotalEvents)
	fmt.Fprintf(&b, "- **Hash chain:** %s\n", chain)

	writeCountTable(&b, "Events by actor", r.EventsByActor)
	writeCountTable(&b, "Events by action", r.EventsByAction)
	writeEntryTable(&b, "Security events", r.SecurityEvents)
	writeEntryTable(&b, "Administrative actions", r.AdminActions)

	_, err := io.WriteString(w, b.String())
	return err
}

// writeCountTable appends a two-column Markdown table of counts, sorted by
// count descending then name for deterministic output.
func writeCountTable(b *strings.Builder, title string, counts map[string]int) {
	fmt.Fprintf(b, "\n## %s\n\n", title)
	if len(counts) == 0 {
		fmt.Fprintf(b, "_none_\n")
		return
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	fmt.Fprintf(b, "| Name | Count |\n|---|---|\n")
	for _, name := range names {
		fmt.Fprintf(b, "| %s | %d |\n", name, counts[name])
	}
}

// writeEntryTable appends a Markdown table of audit entries.
func writeEntryTable(b *strings.Builder, title string, entries []Entry) {
	fmt.Fprintf(b, "\n## %s\n\n", title)
	if len(entries) == 0 {
		fmt.Fprintf(b, "_none_\n")
		return
	}
	fmt.Fprintf(b, "| Seq | Timestamp | Actor | Action | Resource |\n|---|---|---|---|---|\n")
	for _, e := range entries {
		fmt.Fprintf(b, "| %d | %s | %s | %s | %s |\n",
			e.Seq, e.Timestamp.UTC().Format(time.RFC3339), e.Actor, e.Action, e.Resource)
	}
}

// WriteJSON renders the report as indented JSON for SIEM ingestion.
func (r *ComplianceReport) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("audit: encoding compliance report: %w", err)
	}
	return nil
}

// WriteCSV renders the report as metric rows (section,key,value) for
// spreadsheet analysis.
func (r *ComplianceReport) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	rows := [][]string{
		{"section", "key", "value"},
		{"summary", "period", r.Period},
		{"summary", "generated_at", r.GeneratedAt.Format(time.RFC3339)},
		{"summary", "total_events", strconv.Itoa(r.TotalEvents)},
		{"summary", "chain_valid", strconv.FormatBool(r.ChainValid)},
		{"summary", "security_events", strconv.Itoa(len(r.SecurityEvents))},
		{"summary", "admin_actions", strconv.Itoa(len(r.AdminActions))},
	}
	for _, section := range []struct {
		name   string
		counts map[string]int
	}{{"actor", r.EventsByActor}, {"action", r.EventsByAction}} {
		keys := make([]string, 0, len(section.counts))
		for k := range section.counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			rows = append(rows, []string{section.name, k, strconv.Itoa(section.counts[k])})
		}
	}
	for _, row := range rows {
		if err := cw.Write(row); err != nil {
			return fmt.Errorf("audit: writing compliance CSV: %w", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("audit: flushing compliance CSV: %w", err)
	}
	return nil
}

// Archive rotates the live log into audit-<year>-<month>.log.gz beside it and
// starts a fresh log (the hash chain restarts at seq 1, so Verify stays valid
// for both the archive's snapshot and the new live log). Archives older than
// ArchiveRetention are pruned. It returns the archive path, or "" when the
// live log is empty.
func (l *Log) Archive() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.archiveLocked()
}

// ArchiveIfOver rotates the live log only when it exceeds limit bytes (<= 0
// uses ArchiveThreshold), returning the archive path and whether it rotated.
func (l *Log) ArchiveIfOver(limit int64) (string, bool, error) {
	if limit <= 0 {
		limit = ArchiveThreshold
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	info, err := os.Stat(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("audit: checking log size: %w", err)
	}
	if info.Size() <= limit {
		return "", false, nil
	}
	path, err := l.archiveLocked()
	return path, err == nil && path != "", err
}

// archiveLocked does the rotation; the caller must hold l.mu.
func (l *Log) archiveLocked() (string, error) {
	src, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("audit: opening log for archive: %w", err)
	}
	defer func() { _ = src.Close() }()
	if info, serr := src.Stat(); serr == nil && info.Size() == 0 {
		return "", nil
	}

	dest := l.archivePath(time.Now().UTC())
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("audit: creating archive %s: %w", dest, err)
	}
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, src); err != nil {
		_ = gz.Close()
		_ = out.Close()
		_ = os.Remove(dest)
		return "", fmt.Errorf("audit: compressing archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(dest)
		return "", fmt.Errorf("audit: finalising archive: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return "", fmt.Errorf("audit: closing archive: %w", err)
	}

	// Start a fresh live log and restart the chain so Verify holds for it.
	if err := os.Truncate(l.path, 0); err != nil {
		return "", fmt.Errorf("audit: truncating live log after archive: %w", err)
	}
	l.lastSeq = 0
	l.lastHash = ""

	l.pruneArchivesLocked(ArchiveRetention)
	return dest, nil
}

// archivePath returns a unique archive filename beside the live log:
// audit-<year>-<month>.log.gz, suffixed with the day+time when that month's
// archive already exists (multiple rotations in one month).
func (l *Log) archivePath(now time.Time) string {
	dir := filepath.Dir(l.path)
	base := fmt.Sprintf("audit-%04d-%02d.log.gz", now.Year(), int(now.Month()))
	path := filepath.Join(dir, base)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	return filepath.Join(dir, fmt.Sprintf("audit-%04d-%02d-%s.log.gz",
		now.Year(), int(now.Month()), now.Format("02T150405")))
}

// Rekey rewrites the entire live log, recomputing the HMAC chain under
// newKey, used by the master-key migration (production audit C1) to move a
// legacy cluster-name-keyed log onto the master-derived key. The sequence
// numbers, timestamps, and payloads are preserved; only the chain hashes are
// recomputed. The pre-migration log verifies under the OLD key (this Log's
// current key); after Rekey it verifies under newKey. It returns an error if
// the existing chain does not verify under the current key (refusing to
// "launder" a tampered log onto a fresh key).
func (l *Log) Rekey(newKey []byte) error {
	if len(newKey) == 0 {
		return fmt.Errorf("audit: new key must not be empty")
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	entries, err := l.readAll()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		l.key = newKey
		return nil
	}
	if err := l.verifyLocked(entries); err != nil {
		return fmt.Errorf("audit: refusing to rekey a log that fails verification: %w", err)
	}

	next := &Log{path: l.path, key: newKey}
	var buf strings.Builder
	prevHash := ""
	for _, e := range entries {
		e.Hash = next.computeHash(prevHash, e.Seq, e.Timestamp, e.Actor, e.Action, e.Resource)
		line, merr := json.Marshal(e)
		if merr != nil {
			return fmt.Errorf("audit: rekey encoding entry %d: %w", e.Seq, merr)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		prevHash = e.Hash
	}
	if err := os.WriteFile(l.path, []byte(buf.String()), 0o600); err != nil {
		return fmt.Errorf("audit: rekey writing log: %w", err)
	}
	l.key = newKey
	l.lastHash = prevHash
	l.lastSeq = entries[len(entries)-1].Seq
	return nil
}

// Verifies reports whether the live log verifies under this Log's current
// key — a probe used by migration to detect which key a log is on.
func (l *Log) Verifies() bool {
	return l.Verify() == nil
}

// pruneArchivesLocked removes audit-*.log.gz files beside the live log whose
// modification time is older than retention. Best-effort: errors are ignored
// (a failed prune must never break logging).
func (l *Log) pruneArchivesLocked(retention time.Duration) {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(l.path), "audit-*.log.gz"))
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-retention)
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(m)
		}
	}
}
