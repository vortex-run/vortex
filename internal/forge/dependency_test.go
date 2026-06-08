package forge

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDependency_CheckInstalledGoPresent(t *testing.T) {
	d := NewDependencyAgent(DepConfig{SandboxDir: t.TempDir()})
	ok, ver, err := d.CheckInstalled("go")
	if err != nil {
		t.Fatalf("CheckInstalled(go): %v", err)
	}
	if !ok || ver == "" {
		t.Errorf("go should be installed with a version, got ok=%v ver=%q", ok, ver)
	}
}

func TestDependency_CheckInstalledMissing(t *testing.T) {
	d := NewDependencyAgent(DepConfig{SandboxDir: t.TempDir()})
	ok, _, err := d.CheckInstalled("definitely-not-a-real-tool-xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("a missing tool should report installed=false")
	}
}

func TestDependency_EmptyStackNoOp(t *testing.T) {
	d := NewDependencyAgent(DepConfig{SandboxDir: t.TempDir()})
	if err := d.Install(context.Background(), StackChoice{}); err != nil {
		t.Errorf("empty stack install should be a no-op, got: %v", err)
	}
}

func TestDependency_GoStackInstalls(t *testing.T) {
	dir := t.TempDir()
	// A minimal module so `go mod tidy` succeeds.
	writeFile(t, dir, "go.mod", "module forgetest\n\ngo 1.26\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	d := NewDependencyAgent(DepConfig{SandboxDir: dir})
	if err := d.Install(context.Background(), StackChoice{Backend: "go"}); err != nil {
		t.Fatalf("go stack install: %v", err)
	}
}

func TestDependency_PipStackNeedsApproval(t *testing.T) {
	d := NewDependencyAgent(DepConfig{SandboxDir: t.TempDir()})
	// fastapi → pip, react → npm, sklearn → pip: all require approval.
	for _, stack := range []StackChoice{
		{Backend: "fastapi"},
		{Frontend: "react"},
		{ML: "sklearn"},
	} {
		if err := d.Install(context.Background(), stack); !errors.Is(err, ErrApprovalNeeded) {
			t.Errorf("stack %+v: err = %v, want ErrApprovalNeeded", stack, err)
		}
	}
}

func TestDependency_RunRejectsUnsafeCommand(t *testing.T) {
	d := NewDependencyAgent(DepConfig{SandboxDir: t.TempDir()})
	// Directly invoking a non-safe command must be refused (approval-gated).
	if err := d.run(context.Background(), "python3", "-c", "print(1)"); !errors.Is(err, ErrApprovalNeeded) {
		t.Errorf("run(python3) err = %v, want ErrApprovalNeeded", err)
	}
}

func TestDependency_Timeout(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module forgetest\n\ngo 1.26\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	// A 1ns timeout forces a deadline-exceeded on any real command.
	d := NewDependencyAgent(DepConfig{SandboxDir: dir, Timeout: time.Nanosecond})
	err := d.Install(context.Background(), StackChoice{Backend: "go"})
	if err == nil {
		t.Error("expected a timeout error with a 1ns timeout")
	}
}
