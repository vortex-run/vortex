package forge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// safeTools are binaries the DependencyAgent may invoke directly: they have
// constrained, well-understood install/verify paths. Interpreters and package
// managers (python3, npm, pip3) are deliberately NOT here — the C2 hardening
// removed them from the agent allow-list because a single flag is arbitrary
// code execution. Installs that need them go through ErrApprovalNeeded so a
// human approves before they run.
var safeTools = map[string]bool{
	"go":      true,
	"flutter": true,
	"git":     true,
}

// ErrApprovalNeeded indicates a dependency step requires an interpreter/package
// manager (python3/npm/pip3) that the agent cannot run unattended; the caller
// must obtain human approval (the coordinator's ApprovalFunc) before proceeding.
var ErrApprovalNeeded = errors.New("forge: dependency step requires approval (interpreter/package manager)")

// DepConfig configures the dependency agent.
type DepConfig struct {
	SandboxDir string
	Timeout    time.Duration // per command; default 5m
	CacheDir   string        // for caching downloads (reserved)
}

// DependencyAgent installs the packages a build needs.
type DependencyAgent struct {
	cfg DepConfig
}

// NewDependencyAgent constructs the agent.
func NewDependencyAgent(cfg DepConfig) *DependencyAgent {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	return &DependencyAgent{cfg: cfg}
}

// Install installs the packages implied by stack. Safe toolchains (Go modules,
// Flutter) are set up directly; stacks that require pip/npm return
// ErrApprovalNeeded so the orchestrator can gate them behind human approval.
// An empty stack is a no-op.
func (d *DependencyAgent) Install(ctx context.Context, stack StackChoice) error {
	steps, needsApproval := d.plan(stack)
	if needsApproval {
		return ErrApprovalNeeded
	}
	for _, s := range steps {
		if err := d.run(ctx, s.cmd, s.args...); err != nil {
			return fmt.Errorf("forge: install %s: %w", s.cmd, err)
		}
		// Verify the tool is usable.
		if _, _, err := d.version(ctx, s.cmd); err != nil {
			return fmt.Errorf("forge: verify %s: %w", s.cmd, err)
		}
	}
	return nil
}

// installStep is one dependency command.
type installStep struct {
	cmd  string
	args []string
}

// plan returns the install steps for a stack and whether any step needs an
// interpreter/package manager (and thus human approval).
func (d *DependencyAgent) plan(stack StackChoice) (steps []installStep, needsApproval bool) {
	switch stack.Backend {
	case "go":
		steps = append(steps, installStep{"go", []string{"mod", "tidy"}})
	case "fastapi", "express":
		// fastapi → pip, express → npm: both require an approval-gated manager.
		needsApproval = true
	}
	switch stack.Frontend {
	case "flutter":
		// `flutter --version` both verifies the SDK and warms it; pub get runs
		// at build time within the project.
		steps = append(steps, installStep{"flutter", []string{"--version"}})
	case "react":
		needsApproval = true // npm install
	}
	switch stack.ML {
	case "sklearn", "pytorch":
		needsApproval = true // pip install
	}
	return steps, needsApproval
}

// run executes an allowed command in the sandbox.
func (d *DependencyAgent) run(ctx context.Context, command string, args ...string) error {
	if !safeTools[command] {
		return ErrApprovalNeeded
	}
	cctx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, command, args...)
	cmd.Dir = d.cfg.SandboxDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timed out after %s", d.cfg.Timeout)
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// version runs `<name> --version` (or `version` for go) and returns whether the
// tool is installed plus its version string.
func (d *DependencyAgent) version(ctx context.Context, name string) (string, bool, error) {
	verArg := "--version"
	if name == "go" {
		verArg = "version"
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, verArg)
	cmd.Dir = d.cfg.SandboxDir
	out, err := cmd.Output()
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(string(out)), true, nil
}

// CheckInstalled reports whether a tool is installed and its version. It never
// returns an error for a simply-missing tool (installed=false, err=nil); an
// error is returned only for unexpected failures.
func (d *DependencyAgent) CheckInstalled(name string) (bool, string, error) {
	if _, err := exec.LookPath(name); err != nil {
		return false, "", nil
	}
	ver, ok, err := d.version(context.Background(), name)
	if err != nil {
		return false, "", nil // present on PATH but version probe failed → treat as not usable
	}
	return ok, ver, nil
}
