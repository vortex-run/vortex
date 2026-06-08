package forge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// --- step stubs -------------------------------------------------------------

type stubIntent struct{ intent BuildIntent }

func (s stubIntent) Parse(context.Context, string) (BuildIntent, error) { return s.intent, nil }

type stubDeps struct{ err error }

func (s stubDeps) Install(context.Context, StackChoice) error { return s.err }

type stubCodegen struct {
	fixCalls int
	mu       sync.Mutex
}

func (c *stubCodegen) Generate(context.Context, BuildIntent, string) ([]GeneratedFile, error) {
	return []GeneratedFile{{Path: "main.go", Content: "package main"}}, nil
}
func (c *stubCodegen) Fix(context.Context, []GeneratedFile, string) ([]GeneratedFile, error) {
	c.mu.Lock()
	c.fixCalls++
	c.mu.Unlock()
	return []GeneratedFile{{Path: "main.go", Content: "package main // fixed"}}, nil
}

type stubBuilder struct {
	failFirst bool
	calls     int
	mu        sync.Mutex
}

func (b *stubBuilder) Build(context.Context) (BuildOutput, error) {
	b.mu.Lock()
	b.calls++
	n := b.calls
	b.mu.Unlock()
	if b.failFirst && n == 1 {
		return BuildOutput{Stderr: "compile error"}, fmt.Errorf("build failed")
	}
	return BuildOutput{Success: true, ArtifactType: "binary", DurationMs: 10}, nil
}

type stubQA struct {
	failFirst bool
	calls     int
	mu        sync.Mutex
}

func (q *stubQA) Run(context.Context, BuildOutput) (QAResult, error) {
	q.mu.Lock()
	q.calls++
	n := q.calls
	q.mu.Unlock()
	if q.failFirst && n == 1 {
		return QAResult{Passed: false, Checks: []QACheck{{Name: "binary", Passed: false, Message: "missing"}}}, nil
	}
	return QAResult{Passed: true, Checks: []QACheck{{Name: "binary", Passed: true}}}, nil
}

type stubDeliver struct {
	called bool
	mu     sync.Mutex
}

func (d *stubDeliver) Deliver(context.Context, BuildOutput, BuildIntent, int64, float64) error {
	d.mu.Lock()
	d.called = true
	d.mu.Unlock()
	return nil
}

func (d *stubDeliver) wasCalled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.called
}

// newStubForge wires a Forge with all-stub steps.
func newStubForge(t *testing.T, intent BuildIntent, b *stubBuilder, q *stubQA, c *stubCodegen, d *stubDeliver) *Forge {
	t.Helper()
	f, err := NewForge(ForgeConfig{
		SandboxBase: t.TempDir(),
		Intent:      stubIntent{intent: intent},
		Deps:        stubDeps{},
		Codegen:     func(string) codegenStep { return c },
		Builder:     func(string, StackChoice) buildStep { return b },
		QA:          func(string) qaStep { return q },
		Delivery2:   d,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestForge_BuildEndToEnd(t *testing.T) {
	intent := BuildIntent{AppType: AppTypeScript, DeliveryTargets: []string{"script"}, Stack: StackChoice{Backend: "go"}}
	b, q, c, d := &stubBuilder{}, &stubQA{}, &stubCodegen{}, &stubDeliver{}
	f := newStubForge(t, intent, b, q, c, d)

	var stages []string
	err := f.Build(context.Background(), "write a hello world", 1, func(m string) {
		stages = append(stages, m)
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !d.wasCalled() {
		t.Error("delivery should have run on a passing build")
	}
	// progressFn should have been called for the main stages.
	joined := strings.Join(stages, " | ")
	for _, want := range []string{"dependencies", "Generating", "Building", "quality checks", "Delivering"} {
		if !strings.Contains(strings.ToLower(joined), strings.ToLower(want)) {
			t.Errorf("progress missing %q: %s", want, joined)
		}
	}
}

func TestForge_ClarifyingQuestionsShortCircuit(t *testing.T) {
	intent := BuildIntent{ClarifyingQs: []string{"web or mobile?"}}
	b, q, c, d := &stubBuilder{}, &stubQA{}, &stubCodegen{}, &stubDeliver{}
	f := newStubForge(t, intent, b, q, c, d)

	var stages []string
	err := f.Build(context.Background(), "make something", 1, func(m string) { stages = append(stages, m) })
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if d.wasCalled() {
		t.Error("delivery must NOT run when clarifying questions are pending")
	}
	if !strings.Contains(strings.Join(stages, " "), "web or mobile?") {
		t.Errorf("clarifying question should be surfaced via progress: %v", stages)
	}
}

func TestForge_BuildFailureRetried(t *testing.T) {
	intent := BuildIntent{DeliveryTargets: []string{"script"}, Stack: StackChoice{Backend: "go"}}
	b, q, c, d := &stubBuilder{failFirst: true}, &stubQA{}, &stubCodegen{}, &stubDeliver{}
	f := newStubForge(t, intent, b, q, c, d)

	if err := f.Build(context.Background(), "x", 1, nil); err != nil {
		t.Fatalf("Build should recover from a first-build failure: %v", err)
	}
	if c.fixCalls != 1 {
		t.Errorf("expected 1 fix after build failure, got %d", c.fixCalls)
	}
	if !d.wasCalled() {
		t.Error("delivery should run after a successful rebuild")
	}
}

func TestForge_QAFailureTriggersFixCycle(t *testing.T) {
	intent := BuildIntent{DeliveryTargets: []string{"script"}, Stack: StackChoice{Backend: "go"}}
	b, q, c, d := &stubBuilder{}, &stubQA{failFirst: true}, &stubCodegen{}, &stubDeliver{}
	f := newStubForge(t, intent, b, q, c, d)

	if err := f.Build(context.Background(), "x", 1, nil); err != nil {
		t.Fatalf("Build should recover from a first-QA failure: %v", err)
	}
	if c.fixCalls != 1 {
		t.Errorf("expected 1 fix after QA failure, got %d", c.fixCalls)
	}
	if !d.wasCalled() {
		t.Error("delivery should run after QA passes on rebuild")
	}
}

func TestForge_QANeverPassesBlocksDelivery(t *testing.T) {
	// QA fails on every cycle → nothing delivered, error returned.
	alwaysFailQA := &alwaysFail{}
	intent := BuildIntent{DeliveryTargets: []string{"script"}, Stack: StackChoice{Backend: "go"}}
	b, c, d := &stubBuilder{}, &stubCodegen{}, &stubDeliver{}
	f, _ := NewForge(ForgeConfig{
		SandboxBase: t.TempDir(),
		Intent:      stubIntent{intent: intent},
		Deps:        stubDeps{},
		Codegen:     func(string) codegenStep { return c },
		Builder:     func(string, StackChoice) buildStep { return b },
		QA:          func(string) qaStep { return alwaysFailQA },
		Delivery2:   d,
	})
	err := f.Build(context.Background(), "x", 1, nil)
	if err == nil {
		t.Fatal("a build that never passes QA must error")
	}
	if d.wasCalled() {
		t.Error("delivery must NEVER run when QA fails (the gate is never skipped)")
	}
}

type alwaysFail struct{}

func (alwaysFail) Run(context.Context, BuildOutput) (QAResult, error) {
	return QAResult{Passed: false, Checks: []QACheck{{Name: "x", Passed: false}}}, nil
}

func TestForge_SandboxCleanedUp(t *testing.T) {
	base := t.TempDir()
	intent := BuildIntent{DeliveryTargets: []string{"script"}, Stack: StackChoice{Backend: "go"}}
	b, q, c, d := &stubBuilder{}, &stubQA{}, &stubCodegen{}, &stubDeliver{}
	f, _ := NewForge(ForgeConfig{
		SandboxBase: base,
		Intent:      stubIntent{intent: intent},
		Deps:        stubDeps{},
		Codegen:     func(string) codegenStep { return c },
		Builder:     func(string, StackChoice) buildStep { return b },
		QA:          func(string) qaStep { return q },
		Delivery2:   d,
	})
	if err := f.Build(context.Background(), "x", 1, nil); err != nil {
		t.Fatal(err)
	}
	// After a build, the per-build sandbox (build-*) should be removed.
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "build-") {
			t.Errorf("sandbox %s was not cleaned up", e.Name())
		}
	}
}

func TestForge_Status(t *testing.T) {
	f, _ := NewForge(ForgeConfig{SandboxBase: t.TempDir()})
	st := f.Status()
	if st.Active {
		t.Error("a fresh Forge should not be active")
	}
}

// TestForge_RealGoScriptBuild runs the pipeline with the REAL concrete agents
// (dependency/codegen/build/qa/delivery), driven by a scripted AI gateway that
// returns valid Go. This actually compiles Go in CI (Go is always present),
// per the M13 requirement that Go script builds always run.
func TestForge_RealGoScriptBuild(t *testing.T) {
	gw := &scriptedGateway{replies: []string{
		`{"files":[{"path":"go.mod","content":"module hello\n\ngo 1.26\n"},{"path":"main.go","content":"package main\n\nimport \"fmt\"\n\nfunc main(){ fmt.Println(\"hello\") }\n"}]}`,
	}}
	sender := &fakeSender{}
	f, err := NewForge(ForgeConfig{
		SandboxBase: t.TempDir(),
		AIGateway:   gw,
		Intent: stubIntent{intent: BuildIntent{
			AppType: AppTypeScript, DeliveryTargets: []string{"script"},
			Stack: StackChoice{Backend: "go"},
		}},
		Delivery: DeliveryConfig{Sender: sender},
		// Deps/Codegen/Builder/QA/Delivery left nil → real concrete agents.
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Build(context.Background(), "write a go program that prints hello", 1, nil); err != nil {
		t.Fatalf("real Go build pipeline: %v", err)
	}
	// Delivery sent a summary.
	if len(sender.messages) == 0 {
		t.Error("a successful real build should deliver a summary")
	}
}
