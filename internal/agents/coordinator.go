package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AIGateway is the interface the coordinator uses to reach a language model. It
// is implemented by the messaging AI gateway (M11); for M10 a stub suffices.
type AIGateway interface {
	Complete(ctx context.Context, prompt string, systemPrompt string) (string, error)
}

// StubAIGateway is a fixed-response AIGateway used until the real gateway is
// wired in (M11). It echoes a canned reply and, for intent classification,
// returns GENERAL_QUESTION so the coordinator answers directly.
type StubAIGateway struct {
	// IntentReply, if set, is returned verbatim for intent-classification
	// prompts (tests use this to drive routing).
	IntentReply string
	// AnswerReply is returned for direct-answer completions.
	AnswerReply string
}

// Complete returns a canned response. When the system prompt asks for intent
// classification, it returns IntentReply (default GENERAL_QUESTION).
func (s StubAIGateway) Complete(_ context.Context, _ string, systemPrompt string) (string, error) {
	if strings.Contains(strings.ToLower(systemPrompt), "classify") {
		if s.IntentReply != "" {
			return s.IntentReply, nil
		}
		return string(IntentGeneralQuestion), nil
	}
	if s.AnswerReply != "" {
		return s.AnswerReply, nil
	}
	return "stub response", nil
}

// Intent is the classification of a user message.
type Intent string

// Intent classifications produced by the coordinator.
const (
	IntentBuildApp        Intent = "BUILD_APP"
	IntentLocalFile       Intent = "LOCAL_FILE"
	IntentResearch        Intent = "RESEARCH"
	IntentDevOpsCheck     Intent = "DEVOPS_CHECK"
	IntentDataPipeline    Intent = "DATA_PIPELINE"
	IntentGeneralQuestion Intent = "GENERAL_QUESTION"
	IntentUnknown         Intent = "UNKNOWN"
)

// handleMessageTimeout bounds full message handling (intent parse + dispatch).
// Two minutes covers a slow provider intent parse for a real build.
const handleMessageTimeout = 120 * time.Second

const intentSystemPrompt = `You are the VORTEX coordinator. Classify the user's request into exactly one of: BUILD_APP, RESEARCH, DEVOPS_CHECK, DATA_PIPELINE, GENERAL_QUESTION, UNKNOWN. Respond with only the classification keyword.`

const answerSystemPrompt = `You are the VORTEX assistant. Answer the user's question concisely.`

// ApprovalFunc requests human approval for an action (e.g. a run_command).
// It returns true if approved, false if rejected or timed out. The messaging
// layer supplies the real implementation (Telegram approve/reject buttons);
// when nil, approval-gated actions are denied by default (fail safe).
type ApprovalFunc func(ctx context.Context, req ApprovalRequest) bool

// BuildAppFunc handles a BUILD_APP request, returning a user-facing reply (e.g.
// a job id). VORTEX Forge (M13) supplies the implementation via start.go; the
// coordinator stays decoupled from the forge package (which imports agents) to
// avoid an import cycle. When nil, BUILD_APP returns a not-implemented stub.
type BuildAppFunc func(ctx context.Context, userMsg, sessionID string) (string, error)

// CoordinatorConfig configures the user-facing coordinator agent.
type CoordinatorConfig struct {
	Bus        *Bus
	Tools      *SandboxedToolRegistry
	LocalTools *ToolRegistry // local FS + terminal tools (real machine access)
	AIGateway  AIGateway
	MaxAgents  int          // concurrent sub-agent limit (default 8)
	Approval   ApprovalFunc // human-in-the-loop approval; nil = deny gated actions
	BuildApp   BuildAppFunc // BUILD_APP handler (VORTEX Forge); nil = stub
	WorkingDir string       // directory relative paths resolve against
}

// pendingApproval holds a tool action awaiting the user's approve/reject via
// ApproveAction (e.g. the TUI's [Y]/[N]).
type pendingApproval struct {
	tool   string
	params map[string]any
	result chan bool
}

// Coordinator is the single user-facing agent. It classifies user messages,
// routes them to the appropriate mode handler, and supervises spawned
// sub-agents. It implements Agent via an embedded BaseAgent.
type Coordinator struct {
	*BaseAgent
	cfg CoordinatorConfig

	mu      sync.Mutex
	active  map[string]Agent
	pending map[string]*pendingApproval // session → action awaiting approval
}

// WorkingDir returns the directory the agent resolves relative paths against.
func (c *Coordinator) WorkingDir() string { return c.cfg.WorkingDir }

// coordinatorName is the reserved name of the coordinator on the bus.
const coordinatorName = "coordinator"

// defaultMaxAgents bounds concurrent sub-agents when none is configured.
const defaultMaxAgents = 8

// NewCoordinator constructs a coordinator. It requires a Bus and an AIGateway.
func NewCoordinator(cfg CoordinatorConfig) (*Coordinator, error) {
	if cfg.Bus == nil {
		return nil, fmt.Errorf("agents: coordinator requires a bus")
	}
	if cfg.AIGateway == nil {
		return nil, fmt.Errorf("agents: coordinator requires an AI gateway")
	}
	if cfg.MaxAgents <= 0 {
		cfg.MaxAgents = defaultMaxAgents
	}
	c := &Coordinator{
		BaseAgent: NewBaseAgent(AgentConfig{
			Name: coordinatorName, Type: TypePersistent,
			Description: "user-facing coordinator",
		}),
		cfg:     cfg,
		active:  make(map[string]Agent),
		pending: make(map[string]*pendingApproval),
	}
	if c.cfg.WorkingDir == "" {
		c.cfg.WorkingDir, _ = os.Getwd()
	}
	return c, nil
}

// RunTool executes a named tool through the sandboxed registry, handling the
// human-in-the-loop approval flow: if the tool returns an *ApprovalError, the
// coordinator asks the configured ApprovalFunc; on approval it re-runs the
// action with approval granted, and on rejection (or a nil ApprovalFunc) it
// returns an error without executing. Tools that don't require approval run
// directly.
func (c *Coordinator) RunTool(ctx context.Context, name string, params map[string]any) (any, error) {
	if c.cfg.Tools == nil {
		return nil, fmt.Errorf("agents: coordinator has no tool registry")
	}
	result, err := c.cfg.Tools.Execute(ctx, name, params)

	var ae *ApprovalError
	if !errors.As(err, &ae) {
		return result, err // success, or a non-approval error
	}

	// The action needs human sign-off.
	if c.cfg.Approval == nil || !c.cfg.Approval(ctx, ae.Request) {
		return nil, fmt.Errorf("agents: action rejected by user")
	}
	// Approved: re-run the command without the approval gate.
	return c.cfg.Tools.ExecuteApproved(ctx, name, params)
}

// localApprovalTimeout bounds how long a local-tool action waits for the user's
// approve/reject before it is treated as rejected (fail-safe).
const localApprovalTimeout = 10 * time.Minute

// ExecuteLocalTool runs a local FS/terminal tool, streaming human-readable
// progress lines via emit (each becomes a chat message). Read-only tools run
// immediately; mutating tools that return an *ApprovalError are routed for
// approval — via the Approval callback if set, otherwise via a session-keyed
// pending request resolved by ApproveAction (the TUI [Y]/[N]). If NEITHER an
// approver nor a pending resolver can grant approval, the action is DENIED
// (fail-safe — never auto-executed).
func (c *Coordinator) ExecuteLocalTool(ctx context.Context, session, name string, params map[string]any, emit func(string)) (any, error) {
	if c.cfg.LocalTools == nil {
		return nil, fmt.Errorf("agents: no local tools configured")
	}
	tool, err := c.cfg.LocalTools.Get(name)
	if err != nil {
		return nil, err
	}
	emit(toolStartLine(name, params))

	result, err := tool.Execute(ctx, params)

	var ae *ApprovalError
	if !errors.As(err, &ae) {
		if err != nil {
			emit("⚠ " + err.Error())
			return nil, err
		}
		emit(toolDoneLine(name, result))
		return result, nil
	}

	// Approval required — surface the preview, then obtain a decision.
	emit("[APPROVAL_REQUIRED] " + ae.Request.Description)
	if ae.Request.Preview != "" {
		emit(ae.Request.Preview)
	}
	approved := c.awaitApproval(ctx, session, ae.Request)
	if !approved {
		emit("✗ Action rejected by user")
		return nil, fmt.Errorf("agents: action rejected by user")
	}

	// Re-run with the approval gate cleared.
	approvedResult, aerr := executeApprovedLocal(ctx, tool, params)
	if aerr != nil {
		emit("⚠ " + aerr.Error())
		return nil, aerr
	}
	emit(toolDoneLine(name, approvedResult))
	return approvedResult, nil
}

// awaitApproval resolves a pending action: it prefers the synchronous Approval
// callback (Telegram/inline); otherwise it registers a session-keyed pending
// request that ApproveAction resolves, blocking up to localApprovalTimeout. With
// neither available it returns false (deny).
func (c *Coordinator) awaitApproval(ctx context.Context, session string, req ApprovalRequest) bool {
	if c.cfg.Approval != nil {
		return c.cfg.Approval(ctx, req)
	}
	p := &pendingApproval{tool: req.Tool, params: req.Params, result: make(chan bool, 1)}
	c.mu.Lock()
	c.pending[session] = p
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, session)
		c.mu.Unlock()
	}()

	timer := time.NewTimer(localApprovalTimeout)
	defer timer.Stop()
	select {
	case ok := <-p.result:
		return ok
	case <-timer.C:
		return false // timeout → deny
	case <-ctx.Done():
		return false
	}
}

// ApproveAction resolves a pending approval for a session (called by the
// /api/agents/approve handler). It returns true when a pending action matched.
func (c *Coordinator) ApproveAction(session string, approved bool) bool {
	c.mu.Lock()
	p, ok := c.pending[session]
	c.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case p.result <- approved:
	default:
	}
	return true
}

// HasPendingApproval reports whether a session has an action awaiting approval.
func (c *Coordinator) HasPendingApproval(session string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.pending[session]
	return ok
}

// executeApprovedLocal re-runs a local mutating tool with its RequireApproval
// flag cleared.
func executeApprovedLocal(ctx context.Context, tool Tool, params map[string]any) (any, error) {
	switch tl := tool.(type) {
	case WriteLocalFileTool:
		tl.RequireApproval = false
		return tl.Execute(ctx, params)
	case EditFileTool:
		tl.RequireApproval = false
		return tl.Execute(ctx, params)
	case RunTerminalTool:
		tl.RequireApproval = false
		return tl.Execute(ctx, params)
	case CreateProjectTool:
		tl.RequireApproval = false
		return tl.Execute(ctx, params)
	default:
		return tool.Execute(ctx, params) // read-only tool; no gate
	}
}

// toolStartLine renders the "starting" chat line for a tool action.
func toolStartLine(name string, params map[string]any) string {
	switch name {
	case "list_directory":
		return "📂 Listing " + strParamOr(params, "path", ".")
	case "read_file":
		return "📁 Reading file: " + strParamOr(params, "path", "")
	case "write_file":
		return "📝 Writing file: " + strParamOr(params, "path", "")
	case "edit_file":
		return "✏️ Editing file: " + strParamOr(params, "path", "")
	case "run_terminal":
		return "$ " + strParamOr(params, "command", "")
	case "create_project":
		return "📦 Creating project: " + strParamOr(params, "name", "")
	default:
		return "→ " + name
	}
}

// toolDoneLine renders the "done" chat line for a tool action.
func toolDoneLine(name string, result any) string {
	m, _ := result.(map[string]any)
	switch name {
	case "read_file":
		if sz, ok := m["size"].(int); ok {
			return fmt.Sprintf("✓ File read (%d bytes)", sz)
		}
	case "write_file":
		if n, ok := m["bytes_written"].(int); ok {
			path, _ := m["path"].(string)
			return fmt.Sprintf("✓ File created: %s (%d bytes)", path, n)
		}
	case "edit_file":
		return "✓ File edited"
	case "run_terminal":
		if code, ok := m["exit_code"].(int); ok {
			out, _ := m["stdout"].(string)
			return strings.TrimRight(out, "\n") + fmt.Sprintf("\n✓ Command completed (exit %d)", code)
		}
	case "create_project":
		if files, ok := m["files"].([]string); ok {
			return "✓ Project created: " + strings.Join(files, ", ")
		}
	case "list_directory":
		return "✓ Directory listed"
	}
	return "✓ Done"
}

// strParamOr extracts a string param or returns def.
func strParamOr(params map[string]any, key, def string) string {
	if v, ok := params[key].(string); ok && v != "" {
		return v
	}
	return def
}

// HandleMessage is the main entry point for a user message. It classifies the
// intent via the AI gateway and routes to the matching handler. For M10, the
// BUILD_APP/RESEARCH/DEVOPS/DATA handlers are stubs; GENERAL_QUESTION is
// answered directly and UNKNOWN returns a clarifying question.
func (c *Coordinator) HandleMessage(_ context.Context, userMsg, sessionID string) (string, error) {
	if strings.TrimSpace(userMsg) == "" {
		return "", fmt.Errorf("agents: empty user message")
	}
	// Decouple from the caller's context with a 120s floor so a short-lived TUI
	// request can't cancel a slow intent parse / build dispatch mid-flight. The
	// caller's context is deliberately ignored (the TUI cancels it on nav).
	ctx, cancel := context.WithTimeout(context.Background(), handleMessageTimeout)
	defer cancel()

	// Rule-based fast path FIRST: simple file/terminal operations and slash
	// commands route to local tools directly — no AI call, instant. Only fall
	// back to the AI classifier when the rules don't match.
	if intent := ruleClassify(userMsg); intent == IntentLocalFile {
		return c.handleLocalFile(ctx, sessionID, userMsg)
	} else if intent == IntentBuildApp {
		if c.cfg.BuildApp != nil {
			return c.cfg.BuildApp(ctx, userMsg, sessionID)
		}
		return c.modeStub("BUILD_APP"), nil
	}

	intent := c.classify(ctx, userMsg)

	switch intent {
	case IntentGeneralQuestion:
		return c.cfg.AIGateway.Complete(ctx, userMsg, answerSystemPrompt)
	case IntentBuildApp:
		if c.cfg.BuildApp != nil {
			return c.cfg.BuildApp(ctx, userMsg, sessionID)
		}
		return c.modeStub("BUILD_APP"), nil
	case IntentResearch:
		return c.modeStub("RESEARCH"), nil
	case IntentDevOpsCheck:
		return c.modeStub("DEVOPS_CHECK"), nil
	case IntentDataPipeline:
		return c.modeStub("DATA_PIPELINE"), nil
	default:
		return "I'm not sure what you'd like me to do. Could you clarify whether you want me to build an app, research something, run a DevOps check, or answer a question?", nil
	}
}

// classify asks the AI gateway to classify the message, normalising the reply
// to a known Intent (UNKNOWN on anything unrecognised or on error).
func (c *Coordinator) classify(ctx context.Context, userMsg string) Intent {
	reply, err := c.cfg.AIGateway.Complete(ctx, userMsg, intentSystemPrompt)
	if err != nil {
		return IntentUnknown
	}
	reply = strings.ToUpper(strings.TrimSpace(reply))
	for _, known := range []Intent{
		IntentBuildApp, IntentResearch, IntentDevOpsCheck,
		IntentDataPipeline, IntentGeneralQuestion,
	} {
		if strings.Contains(reply, string(known)) {
			return known
		}
	}
	return IntentUnknown
}

// localFileKeywords route a message to local tools directly (no AI).
var localFileKeywords = []string{
	"create a file", "write a file", "save to", "save it to", "save it in",
	"read file", "read the file", "list files", "list the files",
	"edit file", "edit the file", "run command", "run the command",
}

// buildAppKeywords route a message to Forge (which needs AI intent parsing).
var buildAppKeywords = []string{
	"build me an app", "build an android", "build a web app", "build a mobile",
	"create a project", "build and deploy", "scaffold a project",
}

// ruleClassify is a fast, AI-free classifier. It returns IntentLocalFile for
// simple file/terminal operations and slash commands, IntentBuildApp for real
// build requests, or IntentUnknown when no rule matches (caller falls back to
// the AI classifier). Slash commands and the explicit local-file keywords take
// precedence over build keywords so "create a file" never reaches Forge.
func ruleClassify(userMsg string) Intent {
	msg := strings.ToLower(strings.TrimSpace(userMsg))
	if strings.HasPrefix(msg, "/") {
		return IntentLocalFile
	}
	for _, kw := range localFileKeywords {
		if strings.Contains(msg, kw) {
			return IntentLocalFile
		}
	}
	for _, kw := range buildAppKeywords {
		if strings.Contains(msg, kw) {
			return IntentBuildApp
		}
	}
	return IntentUnknown
}

// handleLocalFile dispatches a LOCAL_FILE request to a local tool directly,
// streaming progress. Slash commands map to specific tools; otherwise the
// request is treated as a file write (path/content extracted heuristically).
// It returns a transcript of the streamed steps.
func (c *Coordinator) handleLocalFile(ctx context.Context, sessionID, userMsg string) (string, error) {
	if c.cfg.LocalTools == nil {
		return "", fmt.Errorf("agents: local tools are not enabled")
	}
	tool, params := parseLocalRequest(userMsg)
	if tool == "" {
		return "I can list, read, write, edit, or run things locally. Try `/ls`, `/read <file>`, `/run <cmd>`, or \"create a file <path>\".", nil
	}

	var steps []string
	emit := func(s string) { steps = append(steps, s) }

	// For a prose "create a file …" with no content, generate the file content
	// with the AI FIRST and WAIT for it, so the approval box shows the real code
	// (not a blank preview). Slash /write already carries inline content.
	if tool == "write_file" {
		if content, _ := params["content"].(string); strings.TrimSpace(content) == "" {
			emit("⠸ Generating code…")
			path, _ := params["path"].(string)
			generated, gerr := c.generateFileContent(ctx, userMsg, path)
			if gerr != nil {
				emit("⚠ Could not generate file content: " + gerr.Error())
				return strings.Join(steps, "\n"), nil
			}
			params["content"] = generated
		}
	}

	_, err := c.ExecuteLocalTool(ctx, sessionID, tool, params, emit)
	transcript := strings.Join(steps, "\n")
	if err != nil {
		return transcript, nil // the transcript already carries the failure line
	}
	return transcript, nil
}

// codegenSystemPrompt instructs the model to return raw code only.
const codegenSystemPrompt = "You are a code generator. Return ONLY the raw code, no markdown, no explanation, no code blocks. Just the code itself."

// generateFileContent asks the AI for the file body, given the user's request
// and the target path (the extension implies the language). It strips any
// stray markdown code fences the model may add.
func (c *Coordinator) generateFileContent(ctx context.Context, userMsg, path string) (string, error) {
	if c.cfg.AIGateway == nil {
		return "", fmt.Errorf("no AI gateway configured")
	}
	lang := languageForPath(path)
	filename := filepath.Base(path)
	prompt := fmt.Sprintf("Generate %s in %s.\nFile: %s", userMsg, lang, filename)
	reply, err := c.cfg.AIGateway.Complete(ctx, prompt, codegenSystemPrompt)
	if err != nil {
		return "", err
	}
	return stripCodeFences(reply), nil
}

// languageForPath maps a file extension to a language name for the prompt.
func languageForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py":
		return "Python"
	case ".go":
		return "Go"
	case ".js":
		return "JavaScript"
	case ".ts":
		return "TypeScript"
	case ".c":
		return "C"
	case ".cpp", ".cc":
		return "C++"
	case ".rs":
		return "Rust"
	case ".java":
		return "Java"
	case ".sh":
		return "Bash"
	default:
		return "the appropriate language"
	}
}

// stripCodeFences removes a leading/trailing ```lang fence if the model wrapped
// its output despite being told not to.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the first line (```lang) and a trailing ``` line.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "\n"), "```")
	return strings.TrimRight(s, "\n")
}

// parseLocalRequest maps a LOCAL_FILE message to a tool name + params. It
// handles the slash commands precisely and a few common prose forms; an
// unrecognised request yields an empty tool name (the caller explains usage).
func parseLocalRequest(userMsg string) (string, map[string]any) {
	msg := strings.TrimSpace(userMsg)
	lower := strings.ToLower(msg)

	// Slash commands: "/cmd args".
	if strings.HasPrefix(msg, "/") {
		cmd, rest, _ := strings.Cut(strings.TrimPrefix(msg, "/"), " ")
		rest = strings.TrimSpace(rest)
		switch strings.ToLower(cmd) {
		case "ls":
			return "list_directory", map[string]any{"path": rest}
		case "read":
			return "read_file", map[string]any{"path": rest}
		case "run":
			return "run_terminal", map[string]any{"command": rest}
		case "write", "create":
			path, content, _ := strings.Cut(rest, " ")
			return "write_file", map[string]any{"path": path, "content": content, "create_dirs": true}
		case "edit":
			return "edit_file", map[string]any{"path": rest}
		default:
			return "", nil
		}
	}

	switch {
	case strings.Contains(lower, "list files") || strings.Contains(lower, "list the files"):
		return "list_directory", map[string]any{"path": extractPath(msg)}
	case strings.Contains(lower, "read file") || strings.Contains(lower, "read the file"):
		return "read_file", map[string]any{"path": extractPath(msg)}
	case strings.Contains(lower, "create a file") || strings.Contains(lower, "write a file") ||
		strings.Contains(lower, "save to") || strings.Contains(lower, "save it to") ||
		strings.Contains(lower, "save it in"):
		return "write_file", map[string]any{"path": extractPath(msg), "content": "", "create_dirs": true}
	}
	return "", nil
}

// extractPath pulls a filesystem path from a prose request. It recognises an
// absolute Windows (S:\…) or POSIX (/…) path token; otherwise returns "".
func extractPath(msg string) string {
	for _, tok := range strings.Fields(msg) {
		t := strings.Trim(tok, "\"'.,")
		if len(t) >= 3 && t[1] == ':' && (t[2] == '\\' || t[2] == '/') { // S:\ or S:/
			return t
		}
		if strings.HasPrefix(t, "/") && len(t) > 1 {
			return t
		}
	}
	return ""
}

// modeStub returns the placeholder response for modes filled in later (M13–M16).
func (c *Coordinator) modeStub(mode string) string {
	return "Mode not yet implemented: " + mode
}

// SpawnAgent creates a sub-agent restricted to the named tools, registers it on
// the bus, and tracks it as active. It returns an error if MaxAgents would be
// exceeded.
func (c *Coordinator) SpawnAgent(_ context.Context, cfg AgentConfig, tools []string) (Agent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.active) >= c.cfg.MaxAgents {
		return nil, fmt.Errorf("agents: max concurrent agents (%d) reached", c.cfg.MaxAgents)
	}
	if _, exists := c.active[cfg.Name]; exists {
		return nil, fmt.Errorf("agents: sub-agent %q already active", cfg.Name)
	}

	// Validate requested tools exist (no tool = no action).
	if c.cfg.Tools != nil {
		for _, name := range tools {
			if _, err := c.cfg.Tools.Get(name); err != nil {
				return nil, fmt.Errorf("agents: sub-agent %q requested unknown tool %q: %w", cfg.Name, name, err)
			}
		}
	}

	agent := newSubAgent(cfg, tools)
	if err := c.cfg.Bus.Register(agent); err != nil {
		return nil, err
	}
	c.active[cfg.Name] = agent
	return agent, nil
}

// Reap removes a sub-agent that has reached a terminal state from the active
// set and unregisters it from the bus.
func (c *Coordinator) Reap(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.active[name]; ok {
		if a.State() == StateComplete || a.State() == StateError {
			delete(c.active, name)
			c.cfg.Bus.Unregister(name)
		}
	}
}

// ActiveAgents returns the names of currently active sub-agents.
func (c *Coordinator) ActiveAgents() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.active))
	for n := range c.active {
		names = append(names, n)
	}
	return names
}

// subAgent is a tool-restricted worker agent spawned by the coordinator.
type subAgent struct {
	*BaseAgent
	tools []string
}

// newSubAgent builds a task sub-agent permitted to use only the named tools.
func newSubAgent(cfg AgentConfig, tools []string) *subAgent {
	if cfg.Type == "" {
		cfg.Type = TypeTask
	}
	return &subAgent{BaseAgent: NewBaseAgent(cfg), tools: tools}
}

// Tools returns the names of the tools this sub-agent may use.
func (s *subAgent) Tools() []string { return s.tools }
