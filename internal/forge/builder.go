package forge

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// BuildConfig configures the build agent.
type BuildConfig struct {
	SandboxDir string
	Stack      StackChoice
	Timeout    time.Duration // default 15m
	CacheDir   string        // reserved for build caches
}

// BuildAgent runs headless builds for a generated project.
type BuildAgent struct {
	cfg BuildConfig
}

// NewBuildAgent constructs the agent.
func NewBuildAgent(cfg BuildConfig) *BuildAgent {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Minute
	}
	return &BuildAgent{cfg: cfg}
}

// BuildOutput is the result of a build.
type BuildOutput struct {
	Success      bool   `json:"success"`
	ArtifactPath string `json:"artifact_path"`
	ArtifactType string `json:"artifact_type"` // "apk"|"web-dist"|"binary"|"script"
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	DurationMs   int64  `json:"duration_ms"`
}

// Build selects and runs the build command for the configured stack, capturing
// output. It returns an error when the sandbox is empty or the build fails.
func (b *BuildAgent) Build(ctx context.Context) (BuildOutput, error) {
	if !dirHasFiles(b.cfg.SandboxDir) {
		return BuildOutput{}, fmt.Errorf("forge: empty sandbox, nothing to build")
	}
	command, args, artifactType := b.buildCommand()
	if command == "" {
		return BuildOutput{}, fmt.Errorf("forge: no build command for stack %+v", b.cfg.Stack)
	}

	cctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(cctx, command, args...)
	cmd.Dir = b.cfg.SandboxDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	out := BuildOutput{
		ArtifactType: artifactType,
		Stdout:       stdout.String(),
		Stderr:       stderr.String(),
		DurationMs:   time.Since(start).Milliseconds(),
	}
	if cctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("forge: build timed out after %s", b.cfg.Timeout)
	}
	if runErr != nil {
		return out, fmt.Errorf("forge: build failed: %w: %s", runErr, strings.TrimSpace(out.Stderr))
	}
	out.Success = true
	out.ArtifactPath = b.ArtifactPath()
	return out, nil
}

// buildCommand returns the command, args, and artifact type for the stack.
// Only commands on the agent safe-list (go, flutter) run unattended; React/
// Python stacks would need npm/python3 (approval-gated) and so are reported as
// unbuildable here unless explicitly handled by the orchestrator.
func (b *BuildAgent) buildCommand() (command string, args []string, artifactType string) {
	switch {
	case b.cfg.Stack.Frontend == "flutter":
		return "flutter", []string{"build", "apk", "--release"}, "apk"
	case b.cfg.Stack.Backend == "go":
		return "go", []string{"build", "./..."}, "binary"
	default:
		return "", nil, ""
	}
}

// ArtifactPath returns the expected path of the build output for the stack.
func (b *BuildAgent) ArtifactPath() string {
	base := b.cfg.SandboxDir
	switch {
	case b.cfg.Stack.Frontend == "flutter":
		return filepath.Join(base, "build", "app", "outputs", "apk", "release")
	case b.cfg.Stack.Frontend == "react":
		return filepath.Join(base, "dist")
	case b.cfg.Stack.Backend == "go":
		return filepath.Join(base, "bin")
	default:
		return base
	}
}

// dirHasFiles reports whether dir contains at least one regular file (any depth).
func dirHasFiles(dir string) bool {
	if dir == "" {
		return false
	}
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			found = true
		}
		return nil
	})
	return found
}
