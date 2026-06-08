package forge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/vortex-run/vortex/internal/agents"
)

// Step interfaces let the orchestrator be driven by either the concrete agents
// or test stubs. The concrete *Agent types satisfy these.

type intentStep interface {
	Parse(ctx context.Context, userMsg string) (BuildIntent, error)
}
type depStep interface {
	Install(ctx context.Context, stack StackChoice) error
}
type codegenStep interface {
	Generate(ctx context.Context, intent BuildIntent, spec string) ([]GeneratedFile, error)
	Fix(ctx context.Context, files []GeneratedFile, errorOutput string) ([]GeneratedFile, error)
}
type buildStep interface {
	Build(ctx context.Context) (BuildOutput, error)
}
type qaStep interface {
	Run(ctx context.Context, output BuildOutput) (QAResult, error)
}
type deliverStep interface {
	Deliver(ctx context.Context, result BuildOutput, intent BuildIntent, chatID int64, cost float64) error
}

// ForgeConfig configures the orchestrator. The step fields are optional; when
// nil, Forge builds the concrete agents from the AI gateway and sandbox.
//
//nolint:revive // ForgeConfig name is mandated by the M13 spec
type ForgeConfig struct {
	AIGateway   agents.AIGateway
	Bus         *agents.Bus
	SandboxBase string
	CacheDir    string
	Delivery    DeliveryConfig

	// Optional injected steps (tests provide stubs; production leaves nil).
	Intent    intentStep
	Deps      depStep
	Codegen   func(sandbox string) codegenStep
	Builder   func(sandbox string, stack StackChoice) buildStep
	QA        func(sandbox string) qaStep
	Delivery2 deliverStep
}

// Forge orchestrates the full autonomous build pipeline.
type Forge struct {
	cfg ForgeConfig

	mu      sync.Mutex
	active  bool
	current string
	prog    string
}

// NewForge constructs the orchestrator.
func NewForge(cfg ForgeConfig) (*Forge, error) {
	if cfg.SandboxBase == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		cfg.SandboxBase = filepath.Join(base, "vortex", "agents", "forge")
	}
	return &Forge{cfg: cfg}, nil
}

// ForgeStatus is a snapshot of the orchestrator's current activity.
//
//nolint:revive // ForgeStatus name is mandated by the M13 spec
type ForgeStatus struct {
	Active       bool   `json:"active"`
	CurrentBuild string `json:"current_build"`
	Progress     string `json:"progress"`
}

// Status returns a snapshot of the current build state.
func (f *Forge) Status() ForgeStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ForgeStatus{Active: f.active, CurrentBuild: f.current, Progress: f.prog}
}

// setProgress updates the progress field and invokes progressFn.
func (f *Forge) setProgress(progressFn func(string), msg string) {
	f.mu.Lock()
	f.prog = msg
	f.mu.Unlock()
	if progressFn != nil {
		progressFn(msg)
	}
}

// maxBuildCycles bounds the QA fix→rebuild loop.
const maxBuildCycles = 2

// Build runs the full pipeline for a user request. progressFn (optional)
// receives a message at each stage. It returns nil on a delivered build, or an
// error (the QA gate is never skipped — a failing QA after the fix cycles
// blocks delivery).
func (f *Forge) Build(ctx context.Context, userMsg string, chatID int64, progressFn func(string)) error {
	f.mu.Lock()
	if f.active {
		f.mu.Unlock()
		return fmt.Errorf("forge: a build is already in progress")
	}
	f.active = true
	f.current = userMsg
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active = false
		f.current = ""
		f.mu.Unlock()
	}()

	// 1. Parse intent.
	intent, err := f.intent().Parse(ctx, userMsg)
	if err != nil {
		return fmt.Errorf("forge: parse intent: %w", err)
	}
	// 2. Clarifying questions short-circuit (caller handles the Q&A loop).
	if len(intent.ClarifyingQs) > 0 {
		for _, q := range intent.ClarifyingQs {
			f.setProgress(progressFn, "❓ "+q)
		}
		return nil
	}

	// 3. Per-build sandbox.
	sandbox, err := f.makeSandbox()
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(sandbox) }() // 9. cleanup

	// 4. Generate code first — dependency setup (e.g. `go mod tidy`) operates on
	// the generated sources, so it must run after codegen.
	f.setProgress(progressFn, "Generating code…")
	codegen := f.codegen(sandbox)
	files, err := codegen.Generate(ctx, intent, userMsg)
	if err != nil {
		return fmt.Errorf("forge: codegen: %w", err)
	}

	// 5. Dependencies.
	f.setProgress(progressFn, "Installing dependencies…")
	if derr := f.deps(sandbox).Install(ctx, intent.Stack); derr != nil {
		return fmt.Errorf("forge: dependencies: %w", derr)
	}

	builder := f.builder(sandbox, intent.Stack)
	qa := f.qa(sandbox)

	var output BuildOutput
	var qaResult QAResult
	delivered := false

	for cycle := 0; cycle < maxBuildCycles; cycle++ {
		// 6. Build (with fix-on-failure).
		f.setProgress(progressFn, "Building…")
		output, err = builder.Build(ctx)
		if err != nil {
			f.setProgress(progressFn, "Build failed, attempting fix…")
			files, err = codegen.Fix(ctx, files, output.Stderr+err.Error())
			if err != nil {
				return fmt.Errorf("forge: fix after build failure: %w", err)
			}
			continue // rebuild
		}

		// 7. QA gate — NEVER skipped. A failing QA triggers a fix+rebuild.
		f.setProgress(progressFn, "Running quality checks…")
		qaResult, err = qa.Run(ctx, output)
		if err != nil {
			return fmt.Errorf("forge: qa: %w", err)
		}
		if !qaResult.Passed {
			f.setProgress(progressFn, "QA failed, fixing…")
			files, err = codegen.Fix(ctx, files, qaFailureSummary(qaResult))
			if err != nil {
				return fmt.Errorf("forge: fix after qa failure: %w", err)
			}
			continue // rebuild + re-QA
		}

		// 8. Deliver — only reached when QA passed.
		f.setProgress(progressFn, "Delivering…")
		if derr := f.deliver().Deliver(ctx, output, intent, chatID, f.cost()); derr != nil {
			return fmt.Errorf("forge: deliver: %w", derr)
		}
		delivered = true
		break
	}

	if !delivered {
		return fmt.Errorf("forge: build did not pass QA after %d cycles (nothing delivered)", maxBuildCycles)
	}
	f.setProgress(progressFn, "✅ Done")
	return nil
}

// makeSandbox creates a unique per-build sandbox directory.
func (f *Forge) makeSandbox() (string, error) {
	if err := os.MkdirAll(f.cfg.SandboxBase, 0o700); err != nil {
		return "", fmt.Errorf("forge: create sandbox base: %w", err)
	}
	dir, err := os.MkdirTemp(f.cfg.SandboxBase, "build-")
	if err != nil {
		return "", fmt.Errorf("forge: create build sandbox: %w", err)
	}
	return dir, nil
}

// cost returns the AI spend for the build when the gateway exposes it.
func (f *Forge) cost() float64 {
	if c, ok := f.cfg.AIGateway.(interface{ Cost() float64 }); ok {
		return c.Cost()
	}
	return 0
}

// --- step resolution (injected stub or concrete agent) ----------------------

func (f *Forge) intent() intentStep {
	if f.cfg.Intent != nil {
		return f.cfg.Intent
	}
	if f.cfg.AIGateway != nil {
		return NewAIIntentParser(f.cfg.AIGateway)
	}
	return NewRuleIntentParser()
}

func (f *Forge) deps(sandbox string) depStep {
	if f.cfg.Deps != nil {
		return f.cfg.Deps
	}
	return NewDependencyAgent(DepConfig{SandboxDir: sandbox, CacheDir: f.cfg.CacheDir})
}

func (f *Forge) codegen(sandbox string) codegenStep {
	if f.cfg.Codegen != nil {
		return f.cfg.Codegen(sandbox)
	}
	return NewCodegenAgent(CodegenConfig{SandboxDir: sandbox, AIGateway: f.cfg.AIGateway})
}

func (f *Forge) builder(sandbox string, stack StackChoice) buildStep {
	if f.cfg.Builder != nil {
		return f.cfg.Builder(sandbox, stack)
	}
	return NewBuildAgent(BuildConfig{SandboxDir: sandbox, Stack: stack, CacheDir: f.cfg.CacheDir})
}

func (f *Forge) qa(sandbox string) qaStep {
	if f.cfg.QA != nil {
		return f.cfg.QA(sandbox)
	}
	return NewQAAgent(QAConfig{SandboxDir: sandbox})
}

func (f *Forge) deliver() deliverStep {
	if f.cfg.Delivery2 != nil {
		return f.cfg.Delivery2
	}
	return NewDeliveryAgent(f.cfg.Delivery)
}

// qaFailureSummary renders the failed checks for a fix prompt.
func qaFailureSummary(res QAResult) string {
	var b []string
	for _, c := range res.Checks {
		if !c.Passed {
			b = append(b, c.Name+": "+c.Message)
		}
	}
	return "QA failed: " + fmt.Sprint(b)
}
