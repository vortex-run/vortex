package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/agents"
)

// countingNotifier counts notifications so a test can prove a resumed run does
// not send a duplicate.
type countingNotifier struct{ sends int }

func (c *countingNotifier) Notify(context.Context, string, string) error { c.sends++; return nil }
func (c *countingNotifier) NotifyFile(context.Context, string, []byte, string) error {
	return nil
}

// fencedPipeline builds an agent writing under dir with a ledger over dbPath.
func fencedPipeline(t *testing.T, dir, dbPath string, notif Notifier) (*DataPipelineAgent, *agents.EffectLedger) {
	t.Helper()
	gw := stubGateway{reply: `{"steps":[],"chart":{"type":"bar","title":"S","label":"region","value":"sales"}}`}
	a := NewDataPipelineAgent(gw, notif, dir)
	ledger, err := agents.NewEffectLedger(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	a.SetEffectLedger(ledger)
	return a, ledger
}

// outputCount counts files written under the pipeline output directory.
func outputCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "pipeline"))
	if err != nil {
		return 0
	}
	return len(entries)
}

// TestPipeline_ResumedRunDoesNotDuplicateOutputs is the H3 scenario for the
// pipeline plane. Output filenames embed a timestamp, so a resumed task would
// otherwise write a SECOND set of files (not overwrite the first) and re-send
// the notification.
func TestPipeline_ResumedRunDoesNotDuplicateOutputs(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "effects.db")
	ctx := agents.WithEffectScope(context.Background(), "run-1/task-a")
	notif := &countingNotifier{}

	a1, _ := fencedPipeline(t, dir, db, notif)
	res1, err := a1.Analyze(ctx, "data.csv", []byte(salesCSV), "sales by region", nil)
	if err != nil {
		t.Fatal(err)
	}
	filesAfterFirst := outputCount(t, dir)
	if filesAfterFirst == 0 {
		t.Fatal("first run wrote no outputs")
	}
	if notif.sends != 1 {
		t.Fatalf("notifications after first run = %d, want 1", notif.sends)
	}

	// Crash-resume: fresh agent + fresh ledger handle over the same database.
	a2, _ := fencedPipeline(t, dir, db, notif)
	res2, err := a2.Analyze(ctx, "data.csv", []byte(salesCSV), "sales by region", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outputCount(t, dir); got != filesAfterFirst {
		t.Errorf("output files = %d after resume, want %d — a resumed run must not write duplicates", got, filesAfterFirst)
	}
	if notif.sends != 1 {
		t.Errorf("notifications = %d after resume, want 1 — the user must not be notified twice", notif.sends)
	}
	if res2.DataPath != res1.DataPath {
		t.Errorf("resumed DataPath = %q, want the original %q", res2.DataPath, res1.DataPath)
	}
}

func TestPipeline_UnscopedRunsAreNotFenced(t *testing.T) {
	// Interactive analyses carry no effect scope: running the same request
	// twice must produce two results, not replay the first.
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "effects.db")
	notif := &countingNotifier{}
	a, _ := fencedPipeline(t, dir, db, notif)

	for i := 0; i < 2; i++ {
		if _, err := a.Analyze(context.Background(), "data.csv", []byte(salesCSV), "sales by region", nil); err != nil {
			t.Fatal(err)
		}
	}
	if notif.sends != 2 {
		t.Errorf("notifications = %d, want 2 (no fencing without an effect scope)", notif.sends)
	}
}
