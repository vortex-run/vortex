package devops

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/agents"
)

// countingRunner counts how many commands were actually executed, so a test
// can assert that a fenced action did NOT re-run.
type countingRunner struct {
	writeRecorder
	runs []string
}

func (c *countingRunner) Run(ctx context.Context, cmd string) (string, string, int, error) {
	// Setup probes (os-release, uname) run at construction; only count the
	// commands the action under test issues.
	if !strings.Contains(cmd, "os-release") && cmd != "uname -m" {
		c.runs = append(c.runs, cmd)
	}
	return c.writeRecorder.Run(ctx, cmd)
}

// fencedAgent builds a DevOpsAgent backed by the counting runner and a ledger
// over dbPath.
func fencedAgent(t *testing.T, dbPath string) (*DevOpsAgent, *countingRunner, *agents.EffectLedger) {
	t.Helper()
	r := &countingRunner{}
	r.responses = map[string]string{
		`. /etc/os-release 2>/dev/null; echo "$ID"`: "ubuntu\n",
		"uname -m": "x86_64\n",
	}
	srv, err := newServerWithRunner(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetApprover(approveAll)
	a := NewDevOpsAgent(nil, nil, approveAll)
	a.server = srv
	a.docker = NewDockerManager(srv)
	a.nginx = NewNginxManager(srv)

	ledger, err := agents.NewEffectLedger(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	a.SetEffectLedger(ledger)
	return a, r, ledger
}

// TestDevOps_ResumedTaskDoesNotRerunRestart is the audit's H3 scenario for the
// devops plane: a crash-resumed orchestration task must not restart a service
// (or re-run any side effect) a second time.
func TestDevOps_ResumedTaskDoesNotRerunRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "effects.db")
	ctx := agents.WithEffectScope(context.Background(), "run-1/task-a")

	a1, r1, _ := fencedAgent(t, db)
	out1, err := a1.Handle(ctx, "restart nginx", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.runs) == 0 {
		t.Fatal("first attempt executed no command")
	}

	// Simulate a crash and resume: a fresh process (fresh agent, fresh ledger
	// handle over the same database) replays the same action.
	a2, r2, _ := fencedAgent(t, db)
	out2, err := a2.Handle(ctx, "restart nginx", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.runs) != 0 {
		t.Errorf("resumed task re-executed %d command(s): %v — the effect must be replayed, not re-run", len(r2.runs), r2.runs)
	}
	if out2 != out1 {
		t.Errorf("replayed output = %q, want the recorded %q", out2, out1)
	}
}

func TestDevOps_UnscopedActionsAlwaysRun(t *testing.T) {
	// Interactive (non-orchestrated) requests carry no effect scope and must
	// never be replayed — a user asking twice means run it twice.
	db := filepath.Join(t.TempDir(), "effects.db")
	a, r, _ := fencedAgent(t, db)

	for i := 0; i < 2; i++ {
		if _, err := a.Handle(context.Background(), "restart nginx", nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.runs) < 2 {
		t.Errorf("unscoped runs = %d, want 2 (no fencing without an effect scope)", len(r.runs))
	}
}

func TestDevOps_DistinctActionsAreFencedSeparately(t *testing.T) {
	db := filepath.Join(t.TempDir(), "effects.db")
	ctx := agents.WithEffectScope(context.Background(), "run-1/task-a")
	a, r, _ := fencedAgent(t, db)

	if _, err := a.Handle(ctx, "restart nginx", nil); err != nil {
		t.Fatal(err)
	}
	after := len(r.runs)
	// A different service must not be short-circuited by the first one's key.
	if _, err := a.Handle(ctx, "restart postgres", nil); err != nil {
		t.Fatal(err)
	}
	if len(r.runs) <= after {
		t.Error("a distinct action was incorrectly replayed from another action's journal entry")
	}
}

func TestDevOps_ReadOnlyActionsAreNotFenced(t *testing.T) {
	// Fencing must not cache reads: a resumed task asking for status should
	// see current data, not a stale snapshot.
	db := filepath.Join(t.TempDir(), "effects.db")
	ctx := agents.WithEffectScope(context.Background(), "run-1/task-a")
	a, r, _ := fencedAgent(t, db)
	r.responses["hostname"] = "host-1\n"
	r.responses["nproc"] = "4\n"

	if _, err := a.Handle(ctx, "server status", nil); err != nil {
		t.Fatal(err)
	}
	before := len(r.runs)
	if _, err := a.Handle(ctx, "server status", nil); err != nil {
		t.Fatal(err)
	}
	if len(r.runs) <= before {
		t.Error("read-only status was served from the effect journal; reads must not be fenced")
	}
}
