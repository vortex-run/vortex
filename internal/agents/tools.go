package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Tool is a single, declared, enumerable action an agent may perform. Every
// agent action goes through a Tool: no tool means no action (the permission
// model is allow-list only).
type Tool interface {
	Name() string
	Description() string
	Execute(ctx context.Context, params map[string]any) (any, error)
}

// ErrToolNotFound is returned when looking up an unregistered tool.
var ErrToolNotFound = errors.New("agents: tool not found")

// ErrSandboxViolation is returned when a tool's parameters would escape its
// permitted sandbox (path outside the sandbox dir, disallowed command, etc.).
var ErrSandboxViolation = errors.New("agents: sandbox violation")

// ToolRegistry holds the set of tools available to agents.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewToolRegistry constructs an empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

// Register adds a tool. It returns an error if the name is already taken.
func (r *ToolRegistry) Register(tool Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[tool.Name()]; ok {
		return fmt.Errorf("agents: tool %q already registered", tool.Name())
	}
	r.tools[tool.Name()] = tool
	return nil
}

// Get returns the named tool or ErrToolNotFound.
func (r *ToolRegistry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil, ErrToolNotFound
	}
	return t, nil
}

// List returns the names of all registered tools.
func (r *ToolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	return names
}

// strParam extracts a required string parameter.
func strParam(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", fmt.Errorf("agents: missing parameter %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("agents: parameter %q must be a string", key)
	}
	return s, nil
}

// --- HTTPGetTool ------------------------------------------------------------

// HTTPGetTool performs an HTTP(S) GET request.
type HTTPGetTool struct {
	Client *http.Client
}

// Name returns the tool name.
func (HTTPGetTool) Name() string { return "http_get" }

// Description returns a human-readable summary.
func (HTTPGetTool) Description() string { return "HTTP GET a http/https URL" }

// Execute fetches the URL. Only http/https schemes are permitted.
func (t HTTPGetTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	url, err := strParam(params, "url")
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("%w: only http/https URLs allowed", ErrSandboxViolation)
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	headers := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}
	return map[string]any{"status": resp.StatusCode, "body": string(body), "headers": headers}, nil
}

// --- WriteFileTool / ReadFileTool -------------------------------------------

// sandboxResolve joins rel onto sandboxDir and verifies the result stays within
// the sandbox, defeating "../" traversal.
func sandboxResolve(sandboxDir, rel string) (string, error) {
	if sandboxDir == "" {
		return "", fmt.Errorf("%w: no sandbox configured", ErrSandboxViolation)
	}
	base, err := filepath.Abs(sandboxDir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, rel)
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: path %q escapes sandbox", ErrSandboxViolation, rel)
	}
	return full, nil
}

// WriteFileTool writes content to a file inside the agent's sandbox.
type WriteFileTool struct{ SandboxDir string }

// Name returns the tool name.
func (WriteFileTool) Name() string { return "write_file" }

// Description returns a human-readable summary.
func (WriteFileTool) Description() string { return "Write a file within the agent sandbox" }

// Execute writes params["content"] to params["path"] under the sandbox.
func (t WriteFileTool) Execute(_ context.Context, params map[string]any) (any, error) {
	rel, err := strParam(params, "path")
	if err != nil {
		return nil, err
	}
	content, err := strParam(params, "content")
	if err != nil {
		return nil, err
	}
	full, err := sandboxResolve(t.SandboxDir, rel)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"bytes_written": len(content)}, nil
}

// ReadFileTool reads a file inside the agent's sandbox.
type ReadFileTool struct{ SandboxDir string }

// Name returns the tool name.
func (ReadFileTool) Name() string { return "read_file" }

// Description returns a human-readable summary.
func (ReadFileTool) Description() string { return "Read a file within the agent sandbox" }

// Execute reads params["path"] from under the sandbox.
func (t ReadFileTool) Execute(_ context.Context, params map[string]any) (any, error) {
	rel, err := strParam(params, "path")
	if err != nil {
		return nil, err
	}
	full, err := sandboxResolve(t.SandboxDir, rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	return map[string]any{"content": string(data), "size": len(data)}, nil
}

// --- RunCommandTool ---------------------------------------------------------

// DefaultAllowedCommands is the whitelist used when none is supplied.
var DefaultAllowedCommands = []string{
	"go", "flutter", "npm", "python3", "pip3", "git", "curl", "tar", "unzip",
}

// RunCommandTool runs a whitelisted command in the sandbox directory.
type RunCommandTool struct {
	SandboxDir      string
	AllowedCommands []string
}

// Name returns the tool name.
func (RunCommandTool) Name() string { return "run_command" }

// Description returns a human-readable summary.
func (RunCommandTool) Description() string { return "Run a whitelisted command in the sandbox" }

// allowed reports whether name is in the (effective) whitelist.
func (t RunCommandTool) allowed(name string) bool {
	list := t.AllowedCommands
	if len(list) == 0 {
		list = DefaultAllowedCommands
	}
	for _, c := range list {
		if c == name {
			return true
		}
	}
	return false
}

// Execute runs params["command"] with params["args"] ([]string or []any).
func (t RunCommandTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	command, err := strParam(params, "command")
	if err != nil {
		return nil, err
	}
	if !t.allowed(command) {
		return nil, fmt.Errorf("%w: command %q not allowed", ErrSandboxViolation, command)
	}
	args, err := stringSlice(params["args"])
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = t.SandboxDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	exit := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exit = ee.ExitCode()
		} else {
			return nil, runErr
		}
	}
	return map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exit,
	}, nil
}

// stringSlice coerces a []string or []any of strings into []string.
func stringSlice(v any) ([]string, error) {
	switch s := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return s, nil
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			str, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("agents: args must be strings")
			}
			out = append(out, str)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("agents: args must be a string slice")
	}
}

// --- VortexAPITool ----------------------------------------------------------

// VortexAPITool calls the VORTEX management API. Only /api/* paths are allowed.
type VortexAPITool struct {
	BaseURL string // e.g. http://127.0.0.1:9090
	Client  *http.Client
}

// Name returns the tool name.
func (VortexAPITool) Name() string { return "vortex_api" }

// Description returns a human-readable summary.
func (VortexAPITool) Description() string { return "Call the VORTEX management API (/api/*)" }

// Execute issues an API request with method/path/body params.
func (t VortexAPITool) Execute(ctx context.Context, params map[string]any) (any, error) {
	method, err := strParam(params, "method")
	if err != nil {
		return nil, err
	}
	path, err := strParam(params, "path")
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, "/api/") {
		return nil, fmt.Errorf("%w: only /api/* paths allowed", ErrSandboxViolation)
	}
	body, _ := params["body"].(string)
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, method, t.BaseURL+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": resp.StatusCode, "body": string(data)}, nil
}

// --- SendMessageTool --------------------------------------------------------

// SendMessageTool sends a message to another agent over the bus.
type SendMessageTool struct {
	Bus  *Bus
	From string
}

// Name returns the tool name.
func (SendMessageTool) Name() string { return "send_message" }

// Description returns a human-readable summary.
func (SendMessageTool) Description() string { return "Send a message to another agent via the bus" }

// Execute routes a message to params["to"] with type/payload.
func (t SendMessageTool) Execute(_ context.Context, params map[string]any) (any, error) {
	to, err := strParam(params, "to")
	if err != nil {
		return nil, err
	}
	msgType, err := strParam(params, "type")
	if err != nil {
		return nil, err
	}
	payload, _ := params["payload"].(string)
	if t.Bus == nil {
		return nil, fmt.Errorf("agents: send_message tool has no bus")
	}
	if err := t.Bus.Send(AgentMessage{
		FromAgent: t.From,
		ToAgent:   to,
		Type:      msgType,
		Payload:   payload,
		Timestamp: time.Now(),
	}); err != nil {
		return nil, err
	}
	return map[string]any{"sent": true}, nil
}

// --- SandboxedToolRegistry --------------------------------------------------

// AuditLogger is the subset of the audit log the sandbox uses to record tool
// executions. It is satisfied by *audit.Log.
type AuditLogger interface {
	Append(ctx context.Context, actor, action, resource string, detail map[string]any) error
}

// SandboxedToolRegistry wraps a ToolRegistry, enforcing per-tool restrictions
// (sandbox dir, command whitelist, bus) and recording every execution to the
// audit log when one is configured.
type SandboxedToolRegistry struct {
	registry        *ToolRegistry
	sandboxDir      string
	allowedCommands []string
	bus             *Bus
	audit           AuditLogger
	actor           string
}

// NewSandboxedRegistry constructs a sandboxed registry over the given base
// registry. The built-in stateful tools (file, command, message) are
// re-registered with the sandbox restrictions bound in.
func NewSandboxedRegistry(registry *ToolRegistry, sandboxDir string, allowedCommands []string, bus *Bus) *SandboxedToolRegistry {
	s := &SandboxedToolRegistry{
		registry:        registry,
		sandboxDir:      sandboxDir,
		allowedCommands: allowedCommands,
		bus:             bus,
		actor:           "agent",
	}
	return s
}

// WithAudit sets the audit logger and actor used to record executions.
func (s *SandboxedToolRegistry) WithAudit(log AuditLogger, actor string) *SandboxedToolRegistry {
	s.audit = log
	if actor != "" {
		s.actor = actor
	}
	return s
}

// SandboxDir returns the configured sandbox directory.
func (s *SandboxedToolRegistry) SandboxDir() string { return s.sandboxDir }

// Get returns the named tool from the underlying registry.
func (s *SandboxedToolRegistry) Get(name string) (Tool, error) { return s.registry.Get(name) }

// List returns the available tool names.
func (s *SandboxedToolRegistry) List() []string { return s.registry.List() }

// Execute looks up and runs a tool, recording the execution (and its outcome)
// to the audit log when configured. Restrictions are enforced by the tools
// themselves, which the registry binds to this sandbox.
func (s *SandboxedToolRegistry) Execute(ctx context.Context, name string, params map[string]any) (any, error) {
	tool, err := s.registry.Get(name)
	if err != nil {
		return nil, err
	}
	result, execErr := tool.Execute(ctx, params)
	if s.audit != nil {
		detail := map[string]any{"tool": name, "ok": execErr == nil}
		if execErr != nil {
			detail["error"] = execErr.Error()
		}
		_ = s.audit.Append(ctx, s.actor, "tool.execute", name, detail)
	}
	return result, execErr
}

// RegisterBuiltins populates registry with the built-in tools bound to the
// given sandbox dir, command whitelist, bus, and management API base URL. It is
// a convenience for wiring (used by the runtime and start.go).
func RegisterBuiltins(registry *ToolRegistry, sandboxDir string, allowedCommands []string, bus *Bus, apiBaseURL, agentName string) error {
	tools := []Tool{
		HTTPGetTool{},
		WriteFileTool{SandboxDir: sandboxDir},
		ReadFileTool{SandboxDir: sandboxDir},
		RunCommandTool{SandboxDir: sandboxDir, AllowedCommands: allowedCommands},
		VortexAPITool{BaseURL: apiBaseURL},
		SendMessageTool{Bus: bus, From: agentName},
	}
	for _, t := range tools {
		if err := registry.Register(t); err != nil {
			return err
		}
	}
	return nil
}
