package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vortex-run/vortex/internal/agents"
)

// GeneratedFile is one file produced by the codegen agent.
type GeneratedFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int    `json:"size"`
}

// CodegenConfig configures the code-generation agent.
type CodegenConfig struct {
	SandboxDir string
	AIGateway  agents.AIGateway
	MaxRetries int // default 3
}

// CodegenAgent generates application code via the AI gateway and writes it into
// the sandbox.
type CodegenAgent struct {
	cfg CodegenConfig
}

// NewCodegenAgent constructs the agent.
func NewCodegenAgent(cfg CodegenConfig) *CodegenAgent {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	return &CodegenAgent{cfg: cfg}
}

const codegenSystemPrompt = `You are VORTEX Forge's code generator. Generate complete, working application code. Respond with ONLY a JSON object: {"files":[{"path":"relative/path","content":"file contents"}]}. Paths must be relative (no leading / or ..). Return only the JSON.`

// genResponse is the AI's JSON response shape.
type genResponse struct {
	Files []GeneratedFile `json:"files"`
}

// Generate produces application code for the given intent and writes each file
// into the sandbox. Malformed JSON is retried up to MaxRetries.
func (c *CodegenAgent) Generate(ctx context.Context, intent BuildIntent, spec string) ([]GeneratedFile, error) {
	prompt := fmt.Sprintf(
		"Generate a %s application.\nDescription: %s\nRequirements: %s\nSpec: %s",
		stackSummary(intent.Stack), intent.Description,
		strings.Join(intent.Requirements, "; "), spec)
	return c.generateWithRetry(ctx, prompt)
}

// Fix asks the AI to repair the given files to resolve a build error, then
// overwrites them in the sandbox.
func (c *CodegenAgent) Fix(ctx context.Context, files []GeneratedFile, errorOutput string) ([]GeneratedFile, error) {
	current, err := json.Marshal(genResponse{Files: files})
	if err != nil {
		return nil, err
	}
	prompt := fmt.Sprintf(
		"Fix these files to resolve the build error.\n\nError:\n%s\n\nCurrent files:\n%s\n\nReturn the same JSON format with corrected content.",
		errorOutput, current)
	return c.generateWithRetry(ctx, prompt)
}

// generateWithRetry calls the AI gateway, parsing its JSON response and writing
// the files. It retries on a malformed/unparseable response.
func (c *CodegenAgent) generateWithRetry(ctx context.Context, prompt string) ([]GeneratedFile, error) {
	var lastErr error
	for attempt := 0; attempt < c.cfg.MaxRetries; attempt++ {
		reply, err := c.cfg.AIGateway.Complete(ctx, prompt, codegenSystemPrompt)
		if err != nil {
			lastErr = fmt.Errorf("forge: codegen completion: %w", err)
			continue
		}
		var resp genResponse
		if err := json.Unmarshal([]byte(extractJSON(reply)), &resp); err != nil {
			lastErr = fmt.Errorf("forge: parsing codegen JSON: %w", err)
			continue
		}
		if len(resp.Files) == 0 {
			lastErr = fmt.Errorf("forge: codegen returned no files")
			continue
		}
		written, err := c.writeFiles(resp.Files)
		if err != nil {
			return nil, err // a write error (e.g. path escape) is not retryable
		}
		return written, nil
	}
	return nil, fmt.Errorf("forge: codegen failed after %d attempts: %w", c.cfg.MaxRetries, lastErr)
}

// writeFiles writes each generated file into the sandbox, rejecting any path
// that escapes it. It returns the files with their byte sizes filled in.
func (c *CodegenAgent) writeFiles(files []GeneratedFile) ([]GeneratedFile, error) {
	out := make([]GeneratedFile, 0, len(files))
	for _, f := range files {
		full, err := c.resolve(f.Path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, []byte(f.Content), 0o600); err != nil {
			return nil, err
		}
		f.Size = len(f.Content)
		out = append(out, f)
	}
	return out, nil
}

// resolve joins rel onto the sandbox dir and rejects paths that escape it.
func (c *CodegenAgent) resolve(rel string) (string, error) {
	if c.cfg.SandboxDir == "" {
		return "", fmt.Errorf("forge: no sandbox configured")
	}
	base, err := filepath.Abs(c.cfg.SandboxDir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, rel)
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("forge: generated path %q escapes sandbox", rel)
	}
	return full, nil
}

// stackSummary renders a short description of the stack for the prompt.
func stackSummary(s StackChoice) string {
	parts := []string{}
	if s.Backend != "" && s.Backend != "none" {
		parts = append(parts, s.Backend+" backend")
	}
	if s.Frontend != "" && s.Frontend != "none" {
		parts = append(parts, s.Frontend+" frontend")
	}
	if s.ML != "" && s.ML != "none" {
		parts = append(parts, s.ML+" ML")
	}
	if s.Database != "" && s.Database != "none" {
		parts = append(parts, s.Database+" database")
	}
	if len(parts) == 0 {
		return "minimal"
	}
	return strings.Join(parts, " + ")
}
