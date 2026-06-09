package forge

import (
	"context"
	"testing"
	"time"
)

// newJobManagerWithStubs builds a JobManager over a Forge driven by stub steps.
func newJobManagerWithStubs(t *testing.T, intent BuildIntent, q qaStep) *JobManager {
	t.Helper()
	f, err := NewForge(ForgeConfig{
		SandboxBase: t.TempDir(),
		Intent:      stubIntent{intent: intent},
		Deps:        stubDeps{},
		Codegen:     func(string) codegenStep { return &stubCodegen{} },
		Builder:     func(string, StackChoice) buildStep { return &stubBuilder{} },
		QA:          func(string) qaStep { return q },
		Delivery2:   &stubDeliver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewJobManager(f)
}

func waitState(t *testing.T, m *JobManager, id string, want JobState) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := m.Get(id); ok && j.State == want {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, _ := m.Get(id)
	t.Fatalf("job %s did not reach %q (final: %+v)", id, want, j)
	return Job{}
}

func TestJobs_SubmitAndComplete(t *testing.T) {
	intent := BuildIntent{DeliveryTargets: []string{"script"}, Stack: StackChoice{Backend: "go"}}
	m := newJobManagerWithStubs(t, intent, &stubQA{})

	id := m.Submit(context.Background(), "build a hello world", "s1", 42)
	if id == "" {
		t.Fatal("Submit returned empty job id")
	}
	job := waitState(t, m, id, JobComplete)
	if job.Message != "build a hello world" {
		t.Errorf("job message = %q", job.Message)
	}
}

func TestJobs_FailedBuildRecorded(t *testing.T) {
	intent := BuildIntent{DeliveryTargets: []string{"script"}, Stack: StackChoice{Backend: "go"}}
	m := newJobManagerWithStubs(t, intent, &alwaysFail{}) // QA never passes → failed

	id := m.Submit(context.Background(), "x", "s", 1)
	job := waitState(t, m, id, JobFailed)
	if job.Error == "" {
		t.Error("failed job should record an error")
	}
}

func TestJobs_GetUnknown(t *testing.T) {
	m := newJobManagerWithStubs(t, BuildIntent{}, &stubQA{})
	if _, ok := m.Get("nope"); ok {
		t.Error("Get of unknown id should return false")
	}
}

func TestJobs_List(t *testing.T) {
	intent := BuildIntent{DeliveryTargets: []string{"script"}, Stack: StackChoice{Backend: "go"}}
	m := newJobManagerWithStubs(t, intent, &stubQA{})
	id1 := m.Submit(context.Background(), "first", "s", 1)
	id2 := m.Submit(context.Background(), "second", "s", 1)
	waitState(t, m, id1, JobComplete)
	waitState(t, m, id2, JobComplete)

	jobs := m.List()
	if len(jobs) != 2 {
		t.Fatalf("List returned %d jobs, want 2", len(jobs))
	}
}

func TestJobs_SessionPendingAndClarifying(t *testing.T) {
	intent := BuildIntent{DeliveryTargets: []string{"script"}, Stack: StackChoice{Backend: "go"}}
	m := newJobManagerWithStubs(t, intent, &stubQA{})

	// No job for the session yet.
	if m.SessionPending("sx") || m.SessionClarifying("sx") {
		t.Error("unknown session should be neither pending nor clarifying")
	}

	id := m.Submit(context.Background(), "build a hello world", "sx", 0)
	// Once complete, the session is no longer pending.
	_ = waitState(t, m, id, JobComplete)
	if m.SessionPending("sx") {
		t.Error("a completed build should not be pending")
	}
}

func TestJobs_SessionPendingClarifyState(t *testing.T) {
	// A build that asks a clarifying question → JobClarify → pending + clarifying.
	intent := BuildIntent{ClarifyingQs: []string{"web or mobile?"}}
	m := newJobManagerWithStubs(t, intent, &stubQA{})
	id := m.Submit(context.Background(), "make something", "sc", 0)
	_ = waitState(t, m, id, JobClarify)
	if !m.SessionClarifying("sc") {
		t.Error("a clarifying build should report SessionClarifying")
	}
	if !m.SessionPending("sc") {
		t.Error("a clarifying build is non-terminal → should be pending")
	}
}
