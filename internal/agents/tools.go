package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vortex-run/vortex/pkg/safedial"
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

// ErrSandboxEscape is returned specifically when a filesystem path escapes the
// sandbox boundary, either lexically (../) or via a symlink pointing outside.
// It wraps ErrSandboxViolation so existing errors.Is(err, ErrSandboxViolation)
// checks continue to hold.
var ErrSandboxEscape = fmt.Errorf("%w: path escapes sandbox boundary", ErrSandboxViolation)

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

// ErrSSRFBlocked is returned when http_get is asked to reach a private,
// loopback, link-local, or cloud-metadata address (SSRF prevention).
var ErrSSRFBlocked = errors.New("agents: SSRF target blocked")

// metadataIPs are well-known cloud instance-metadata service addresses.
var metadataIPs = map[string]bool{
	"169.254.169.254": true, // AWS / GCP / Azure (IMDS)
	"fd00:ec2::254":   true, // AWS IPv6 metadata
}

// HTTPGetTool performs an HTTP(S) GET request with SSRF protection.
type HTTPGetTool struct {
	Client *http.Client
	// AllowedHosts, when non-empty, restricts requests to URLs whose hostname
	// exactly matches one of these entries (in addition to the IP checks).
	AllowedHosts []string
}

// Name returns the tool name.
func (HTTPGetTool) Name() string { return "http_get" }

// Description returns a human-readable summary.
func (HTTPGetTool) Description() string { return "HTTP GET a http/https URL (SSRF-protected)" }

// blockedIP reports whether ip is a loopback, link-local, private, or
// metadata address that http_get must never reach.
func blockedIP(ip net.IP) bool {
	if ip == nil {
		return true // unparseable → block
	}
	if metadataIPs[ip.String()] {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || // RFC1918 + IPv6 ULA (fc00::/7)
		ip.IsUnspecified()
}

// isSSRFTarget resolves rawURL's host and returns ErrSSRFBlocked if the scheme
// is not http/https, the host is in no allow-list, or any resolved IP is a
// blocked (internal) address. This is the pre-flight check; the safedial
// client used by Execute re-resolves, validates, and dials the pinned IP, so
// a DNS rebind between this check and the connection cannot slip through.
func (t HTTPGetTool) isSSRFTarget(rawURL string) error {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: invalid URL: %v", ErrSandboxViolation, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: only http/https URLs allowed", ErrSandboxViolation)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrSandboxViolation)
	}
	if len(t.AllowedHosts) > 0 {
		if !t.hostAllowed(host) {
			return fmt.Errorf("%w: host %q not in allow-list", ErrSSRFBlocked, host)
		}
		// An explicit allow-list is an operator opt-in: it may legitimately name
		// an internal host (e.g. 127.0.0.1 for a co-located service), so once a
		// host matches we skip the internal-IP block for it.
		return nil
	}
	// If the host is a literal IP, check it directly; otherwise resolve.
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return fmt.Errorf("%w: %s", ErrSSRFBlocked, host)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: resolve %q: %v", ErrSSRFBlocked, host, err)
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return fmt.Errorf("%w: %s resolves to %s", ErrSSRFBlocked, host, ip)
		}
	}
	return nil
}

// hostAllowed reports whether host matches an AllowedHosts entry.
func (t HTTPGetTool) hostAllowed(host string) bool {
	for _, h := range t.AllowedHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// Execute fetches the URL after SSRF validation. Only http/https schemes are
// permitted and internal/metadata addresses are blocked.
func (t HTTPGetTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	rawURL, err := strParam(params, "url")
	if err != nil {
		return nil, err
	}
	if serr := t.isSSRFTarget(rawURL); serr != nil {
		return nil, serr
	}
	client := t.Client
	if client == nil {
		// safedial resolves once, validates, and dials the pinned IP, and
		// re-validates each redirect hop — robust against DNS rebinding
		// (production audit H2). With an explicit host allow-list the operator
		// has opted in (and the host was validated above), so loopback/internal
		// targets are permitted; otherwise they are blocked at dial time too.
		if len(t.AllowedHosts) > 0 {
			client = safedial.Client(safedial.Config{AllowLoopback: true})
		} else {
			client = safedial.Client(safedial.Config{})
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
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
// the sandbox. It defeats both "../" traversal (lexical) AND symlink escape: a
// symlink placed inside the sandbox pointing outside would pass the lexical
// check, so after the join we resolve symlinks (on the path itself if it exists,
// otherwise on its parent directory for new-file writes) and re-verify the real
// path is contained.
func sandboxResolve(sandboxDir, rel string) (string, error) {
	if sandboxDir == "" {
		return "", fmt.Errorf("%w: no sandbox configured", ErrSandboxViolation)
	}
	base, err := filepath.Abs(sandboxDir)
	if err != nil {
		return "", err
	}

	// Step 1: lexical join (blocks ../ traversal).
	joined := filepath.Join(base, rel)
	if joined != base && !strings.HasPrefix(joined, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: path %q escapes sandbox", ErrSandboxEscape, rel)
	}

	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("agents: resolve sandbox base: %w", err)
	}

	// Step 2: if the path already exists, resolve symlinks and re-check. This
	// catches a symlink inside the sandbox that points outside it.
	if _, lerr := os.Lstat(joined); lerr == nil {
		resolved, rerr := filepath.EvalSymlinks(joined)
		if rerr != nil {
			return "", fmt.Errorf("agents: resolve symlink: %w", rerr)
		}
		if !containedIn(resolved, realBase) {
			return "", fmt.Errorf("%w: %q resolves outside sandbox", ErrSandboxEscape, rel)
		}
		return resolved, nil
	}

	// Step 3: the path does not exist yet (e.g. a new file to write). Resolve
	// the parent directory's real path and ensure it is within the sandbox, so
	// a symlinked parent cannot redirect the write outside.
	parent := filepath.Dir(joined)
	if _, lerr := os.Lstat(parent); lerr == nil {
		realParent, rerr := filepath.EvalSymlinks(parent)
		if rerr != nil {
			return "", fmt.Errorf("agents: resolve parent: %w", rerr)
		}
		if !containedIn(realParent, realBase) {
			return "", fmt.Errorf("%w: parent of %q resolves outside sandbox", ErrSandboxEscape, rel)
		}
	}
	return joined, nil
}

// containedIn reports whether path is realBase itself or lies beneath it.
func containedIn(path, realBase string) bool {
	return path == realBase || strings.HasPrefix(path, realBase+string(os.PathSeparator))
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
//
// Interpreters and package managers (python3, npm, pip3) are deliberately
// EXCLUDED: they execute arbitrary code from a single flag (python3 -c, npm run,
// pip3 install <pkg-with-setup.py>), so name-based whitelisting gives no real
// containment without an OS-level sandbox. The remaining binaries still need
// per-argument validation (see validateArgs) because even they have code-exec
// flags (git core.sshCommand, tar --to-command, go -exec).
var DefaultAllowedCommands = []string{
	"go", "flutter", "git", "tar", "unzip",
}

// ErrApprovalRequired is returned by RunCommandTool when RequireApproval is set.
// It carries the full command so a human-in-the-loop approver can review it.
var ErrApprovalRequired = errors.New("agents: command requires human approval")

// ApprovalRequest describes an action awaiting human approval. It is the value
// wrapped by an *ApprovalError so callers (the coordinator/approver) can render
// a preview and decide. Command/Args describe a run_command; Tool/Description/
// Preview describe the richer local-filesystem and terminal tools.
type ApprovalRequest struct {
	Command string
	Args    []string

	// Richer fields for the local FS / terminal tools (M-agent local access).
	Tool        string         // e.g. "write_file", "run_terminal"
	Description string         // one-line human summary
	Preview     string         // diff / content / command preview to show
	Params      map[string]any // the parameters to re-run with on approval
	RiskLevel   string         // brand risk level (see RiskFor); "" → derive from Tool
}

// Risk levels for tool approvals (brand redesign part 6). They mirror
// brand.Risk* without importing the TUI brand package into the agents core.
const (
	RiskLow      = "LOW RISK"
	RiskMedium   = "MEDIUM RISK"
	RiskHigh     = "HIGH RISK"
	RiskCritical = "CRITICAL — review carefully"
)

// toolRisk maps a tool name to its approval risk level. Read-only inspection
// is low; file mutation is medium; command/commit/delete/docker execution is
// high; remote (SSH) execution is critical (it touches another machine and is
// hard to undo).
var toolRisk = map[string]string{
	"list_directory": RiskLow,
	"read_file":      RiskLow,
	"read_local":     RiskLow,
	"search_files":   RiskLow,
	"find_files":     RiskLow,
	"git_status":     RiskLow,
	"git_diff":       RiskLow,

	"write_file":  RiskMedium,
	"write_local": RiskMedium,
	"edit_file":   RiskMedium,
	"git_add":     RiskMedium,

	"run_command":    RiskHigh,
	"run_terminal":   RiskHigh,
	"git_commit":     RiskHigh,
	"delete_file":    RiskHigh,
	"docker_run":     RiskHigh,
	"create_project": RiskHigh,

	"ssh_command": RiskCritical,
}

// RiskFor returns the approval risk level for a tool name, defaulting to
// medium for any unrecognised mutating tool (fail toward caution).
func RiskFor(toolName string) string {
	if r, ok := toolRisk[toolName]; ok {
		return r
	}
	return RiskMedium
}

// Risk returns the request's risk level, deriving it from the Tool name when
// not explicitly set.
func (r ApprovalRequest) Risk() string {
	if r.RiskLevel != "" {
		return r.RiskLevel
	}
	tool := r.Tool
	if tool == "" && r.Command != "" {
		tool = "run_command"
	}
	return RiskFor(tool)
}

// ApprovalError wraps ErrApprovalRequired with the command details.
type ApprovalError struct {
	Request ApprovalRequest
}

// Error implements error.
func (e *ApprovalError) Error() string {
	return fmt.Sprintf("%v: %s %v", ErrApprovalRequired, e.Request.Command, e.Request.Args)
}

// Unwrap lets errors.Is(err, ErrApprovalRequired) succeed.
func (e *ApprovalError) Unwrap() error { return ErrApprovalRequired }

// RunCommandTool runs a whitelisted command in the sandbox directory.
type RunCommandTool struct {
	SandboxDir      string
	AllowedCommands []string
	// RequireApproval, when true, makes Execute return an *ApprovalError
	// instead of running — the coordinator routes this to a human approver
	// (M10.7). It defaults to true via NewRunCommandTool until an OS-level
	// sandbox makes unattended execution safe.
	RequireApproval bool
}

// NewRunCommandTool builds a RunCommandTool with RequireApproval defaulted to
// true (safe by default). Callers that have an OS sandbox can clear it.
func NewRunCommandTool(sandboxDir string, allowed []string) RunCommandTool {
	return RunCommandTool{SandboxDir: sandboxDir, AllowedCommands: allowed, RequireApproval: true}
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

// dangerousArgPatterns maps a command to substrings that, if present in any
// argument, indicate a code-execution or sandbox-escape attempt.
var dangerousArgPatterns = map[string][]string{
	"curl": {"file://", "/etc", "/proc", ".aws", ".ssh", "169.254",
		"127.0.0.1", "localhost", "10.", "192.168.", "172.16.", "172.17.",
		"172.18.", "172.19.", "172.2", "172.30.", "172.31."},
	"git": {"core.sshCommand", "core.hookspath", "core.fsmonitor",
		"--upload-pack", "--receive-pack", "remote-ext", "ext::"},
	"go":  {"-exec", "run http", "run ftp"},
	"tar": {"--to-command", "-I", "--use-compress-program"},
}

// validateArgs rejects arguments matching known dangerous patterns for command.
func validateArgs(command string, args []string) error {
	patterns := dangerousArgPatterns[command]
	if len(patterns) == 0 {
		return nil
	}
	for _, a := range args {
		la := strings.ToLower(a)
		for _, p := range patterns {
			if strings.Contains(la, strings.ToLower(p)) {
				return fmt.Errorf("%w: %s argument %q contains disallowed pattern %q",
					ErrSandboxViolation, command, a, p)
			}
		}
	}
	return nil
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
	if verr := validateArgs(command, args); verr != nil {
		return nil, verr
	}
	if t.RequireApproval {
		return nil, &ApprovalError{Request: ApprovalRequest{Command: command, Args: args}}
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

// vortexAPIDeniedPaths are path prefixes the agent must never call: the agent
// endpoints (no recursive self-submission) and the control plane.
var vortexAPIDeniedPaths = []string{
	"/api/agents/",
	"/internal/shutdown",
	"/internal/reload",
}

// VortexAPITool calls the VORTEX management API. Only /api/* paths are allowed,
// excluding the agent and control-plane paths in vortexAPIDeniedPaths.
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
	// Deny self-referential / control-plane paths: an agent must not be able to
	// recursively drive the agent runtime or trigger reload/shutdown.
	for _, denied := range vortexAPIDeniedPaths {
		if strings.HasPrefix(path, denied) {
			return nil, fmt.Errorf("%w: path %q is not allowed", ErrSandboxViolation, path)
		}
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

// ExecuteApproved runs a tool with the human-approval gate disabled. It is
// called only after an approver has granted permission for an action that
// returned an *ApprovalError. For a RunCommandTool this re-runs the same
// command with RequireApproval cleared; other tools execute normally. The
// execution is audit-logged as an approved action.
func (s *SandboxedToolRegistry) ExecuteApproved(ctx context.Context, name string, params map[string]any) (any, error) {
	tool, err := s.registry.Get(name)
	if err != nil {
		return nil, err
	}
	if rc, ok := tool.(RunCommandTool); ok {
		rc.RequireApproval = false
		tool = rc
	}
	result, execErr := tool.Execute(ctx, params)
	if s.audit != nil {
		detail := map[string]any{"tool": name, "ok": execErr == nil, "approved": true}
		if execErr != nil {
			detail["error"] = execErr.Error()
		}
		_ = s.audit.Append(ctx, s.actor, "tool.execute.approved", name, detail)
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
		NewRunCommandTool(sandboxDir, allowedCommands),
		VortexAPITool{BaseURL: apiBaseURL},
		SendMessageTool{Bus: bus, From: agentName},
		// LSP diagnostics (M20): read-only code intelligence. The sandbox dir is
		// the workspace root; the tool degrades to an empty result when no
		// language server is installed.
		&LSPDiagnosticsTool{WorkDir: sandboxDir},
	}
	for _, t := range tools {
		if err := registry.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// RegisterLocalTools registers the local filesystem + terminal tools (real
// machine access, approval-gated) into registry, bound to cfg. These provide
// list_directory/read_file (read-only) and write_file/edit_file/run_terminal/
// create_project (approval-required). When mixed with RegisterBuiltins, register
// local tools into a separate registry to avoid read_file/write_file name
// collisions — the wiring (start.go) chooses one set.
func RegisterLocalTools(registry *ToolRegistry, cfg LocalFSConfig) error {
	for _, t := range NewLocalTools(cfg) {
		if err := registry.Register(t); err != nil {
			return err
		}
	}
	return nil
}
