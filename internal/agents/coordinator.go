package agents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
	IntentOrchestrate     Intent = "ORCHESTRATE"
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

// ResearchFunc handles a RESEARCH request, returning a user-facing reply. The
// M15 research agent supplies the implementation via start.go (keeping the
// coordinator decoupled from the research package). progressFn streams step
// updates. When nil, RESEARCH returns a not-implemented stub.
type ResearchFunc func(ctx context.Context, query string, progressFn func(string)) (string, error)

// DevOpsFunc handles a DEVOPS request (SSH/Docker/Nginx server management),
// returning a user-facing reply. The M16 devops agent supplies it via start.go.
// When nil, DEVOPS returns a not-implemented stub.
type DevOpsFunc func(ctx context.Context, msg string, progressFn func(string)) (string, error)

// PipelineFunc handles a DATA_PIPELINE request (analyze data → chart → report),
// returning a user-facing reply. The M17 pipeline agent supplies it via
// start.go. When nil, DATA_PIPELINE returns a not-implemented stub.
type PipelineFunc func(ctx context.Context, msg string, progressFn func(string)) (string, error)

// OrchestrateFunc handles a multi-agent goal (M18): decompose into tasks and
// run them across specialized agents, returning a summary. Supplied via
// start.go. When nil, orchestration returns a not-implemented stub.
type OrchestrateFunc func(ctx context.Context, goal string, progressFn func(string)) (string, error)

// CoordinatorConfig configures the user-facing coordinator agent.
type CoordinatorConfig struct {
	Bus         *Bus
	Tools       *SandboxedToolRegistry
	LocalTools  *ToolRegistry // local FS + terminal tools (real machine access)
	AIGateway   AIGateway
	MaxAgents   int             // concurrent sub-agent limit (default 8)
	Approval    ApprovalFunc    // human-in-the-loop approval; nil = deny gated actions
	BuildApp    BuildAppFunc    // BUILD_APP handler (VORTEX Forge); nil = stub
	Research    ResearchFunc    // RESEARCH handler (M15 research agent); nil = stub
	DevOps      DevOpsFunc      // DEVOPS handler (M16 devops agent); nil = stub
	Pipeline    PipelineFunc    // DATA_PIPELINE handler (M17 pipeline agent); nil = stub
	Orchestrate OrchestrateFunc // ORCHESTRATE handler (M18 multi-agent); nil = stub
	// SessionClarifying reports whether the most recent build for a session is
	// awaiting clarifying answers (forge JobClarify state). Optional.
	SessionClarifying func(sessionID string) bool
	// SessionPending reports whether the most recent build for a session is
	// non-terminal (queued/running/clarifying). Used so a follow-up message is
	// treated as an answer while the (async) build is still in flight — even
	// before it reaches needs_clarification. Optional.
	SessionPending func(sessionID string) bool
	// MemoryStore, when set, persists per-session conversation history under this
	// directory and passes recent context to the AI. Optional.
	MemoryStore string
	WorkingDir  string // directory relative paths resolve against
}

// pendingApproval holds a tool action awaiting the user's approve/reject via
// ApproveAction (e.g. the TUI's [Y]/[N]). For the synchronous (Telegram) path,
// result delivers the decision to a blocked awaitApproval. For the async (TUI)
// path, tool/params let ApproveAction execute the action directly when the
// approve/reject POST arrives — the original Submit has already returned the
// preview, so it cannot block waiting.
type pendingApproval struct {
	toolName string
	tool     Tool
	params   map[string]any
	result   chan bool
}

// SessionState tracks per-conversation context so a multi-turn build (with
// clarifying questions) continues the SAME job instead of restarting.
type SessionState struct {
	OriginalRequest       string    // the first build message in this session
	AwaitingClarification bool      // forge asked questions; next msg is an answer
	Answers               []string  // accumulated clarification answers
	LastActivity          time.Time // for idle cleanup
}

// sessionTTL clears idle session state after this long.
const sessionTTL = 10 * time.Minute

// Hard caps on the in-memory session/memory maps, a backstop against
// unbounded growth from a burst of distinct session IDs within one TTL window
// (production audit H5). Memories reload from disk on demand, so eviction is
// lossless; session state is transient clarification context.
const (
	maxCachedSessions = 10000
	maxCachedMemories = 10000
)

// Coordinator is the single user-facing agent. It classifies user messages,
// routes them to the appropriate mode handler, and supervises spawned
// sub-agents. It implements Agent via an embedded BaseAgent.
type Coordinator struct {
	*BaseAgent
	cfg CoordinatorConfig

	mu            sync.Mutex
	active        map[string]Agent
	pending       map[string]*pendingApproval // session → action awaiting approval
	sessions      map[string]*SessionState    // session → conversation state
	memories      map[string]*Memory          // session → conversation memory (JSON fallback)
	projectCtx    string                      // cached project description
	projectCtxSet bool

	// store, when set, is the SQLite conversation backend (M20). It supersedes
	// the per-session JSON memories map for persistence, listing, history, and
	// context; the JSON path remains for deployments without a DB configured.
	store *MemoryStore

	// skills, when set, is the learned-skill store (upgrade 1 — self-improving
	// agent): proven procedures are surfaced in the system prompt before a task
	// runs, and skillWriter distils new skills from completed multi-step tasks.
	skills      *SkillStore
	skillWriter *SkillWriter

	// episodic, when set, is the tier-2 cross-session memory (upgrade 2):
	// relevant episodes are recalled into the system prompt for each message,
	// and each completed exchange is mined for durable facts asynchronously.
	episodic *EpisodicStore
}

// SetMemoryStore wires the SQLite conversation store, making it the persistence
// backend for this coordinator. Pass nil to use the legacy JSON store.
func (c *Coordinator) SetMemoryStore(s *MemoryStore) {
	c.mu.Lock()
	c.store = s
	c.mu.Unlock()
}

// SetSkillStore wires the learned-skill store, enabling skill recall before
// tasks and skill learning after them. Pass nil to disable.
func (c *Coordinator) SetSkillStore(s *SkillStore) {
	c.mu.Lock()
	c.skills = s
	if s != nil {
		c.skillWriter = NewSkillWriter(c.cfg.AIGateway, s)
	} else {
		c.skillWriter = nil
	}
	c.mu.Unlock()
}

// SetEpisodicStore wires the cross-session episodic memory. Pass nil to
// disable.
func (c *Coordinator) SetEpisodicStore(s *EpisodicStore) {
	c.mu.Lock()
	c.episodic = s
	c.mu.Unlock()
}

// episodicStore returns the episodic memory under lock (nil when disabled).
func (c *Coordinator) episodicStore() *EpisodicStore {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.episodic
}

// recallMemoriesBlock returns a system-prompt addendum with episodes relevant
// to msg, or "" when episodic memory is disabled or nothing matches.
func (c *Coordinator) recallMemoriesBlock(msg string) string {
	store := c.episodicStore()
	if store == nil {
		return ""
	}
	episodes, err := store.Recall(msg, 5)
	if err != nil || len(episodes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nRelevant memories:\n")
	for _, ep := range episodes {
		b.WriteString("- ")
		if ep.Context != "" {
			b.WriteString("[" + ep.Context + "] ")
		}
		b.WriteString(ep.Content + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// rememberExchangeAsync mines a completed exchange for durable facts in the
// background (best effort — the user-facing reply never waits on it).
func (c *Coordinator) rememberExchangeAsync(sessionID, userMsg, reply string) {
	store := c.episodicStore()
	if store == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("episodic memory goroutine panic recovered", "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		exchange := "user: " + userMsg + "\nagent: " + reply
		if err := store.StoreImportant(ctx, c.cfg.AIGateway, sessionID, exchange); err != nil {
			slog.Debug("episodic memory extraction skipped", "error", err)
		}
	}()
}

// skillMinSuccessRate is the floor below which a stored skill is not trusted
// enough to steer the agent — it stays in the store (MaybeLearn won't relearn
// it) but is left out of the prompt until its record improves.
const skillMinSuccessRate = 0.8

// findSkill returns the best proven skill for a message, or nil when no skill
// store is configured or nothing relevant has a good enough track record.
func (c *Coordinator) findSkill(msg string) *Skill {
	c.mu.Lock()
	store := c.skills
	c.mu.Unlock()
	if store == nil {
		return nil
	}
	skills, err := store.Find(msg)
	if err != nil {
		return nil
	}
	for _, sk := range skills {
		if sk.SuccessRate > skillMinSuccessRate {
			return sk
		}
	}
	return nil
}

// skillPromptBlock renders a skill as a system-prompt addendum so the model
// follows the proven procedure instead of reasoning from scratch.
func skillPromptBlock(sk *Skill) string {
	var b strings.Builder
	b.WriteString("\n\nYou have a proven skill for this type of task:\n")
	b.WriteString(sk.Name + ": " + sk.Description + "\nSteps:\n")
	for i, st := range sk.Steps {
		fmt.Fprintf(&b, "%d. %s", i+1, st.Description)
		if st.ToolName != "" {
			b.WriteString(" (tool: " + st.ToolName + ")")
		}
		if st.IsOptional {
			b.WriteString(" [optional]")
		}
		b.WriteString("\n")
	}
	b.WriteString("Use this procedure unless the user asks for something different.")
	return b.String()
}

// markSkillUsed records the outcome of a skill-guided task (best effort).
func (c *Coordinator) markSkillUsed(id string, success bool) {
	c.mu.Lock()
	store := c.skills
	c.mu.Unlock()
	if store == nil {
		return
	}
	if err := store.MarkUsed(id, success); err != nil {
		slog.Warn("recording skill use failed", "skill", id, "error", err)
	}
}

// maybeLearn feeds a completed task to the skill writer (best effort — a
// learning failure never affects the user-facing reply).
func (c *Coordinator) maybeLearn(ctx context.Context, task string, steps []string, result string, success bool) {
	c.mu.Lock()
	w := c.skillWriter
	c.mu.Unlock()
	if w == nil {
		return
	}
	if err := w.MaybeLearn(ctx, task, steps, result, success); err != nil {
		slog.Warn("skill learning failed", "error", err)
	}
}

// memory returns (loading if needed) the conversation memory for a session, or
// nil when no MemoryStore is configured.
func (c *Coordinator) memory(sessionID string) *Memory {
	if c.cfg.MemoryStore == "" || sessionID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.memories[sessionID]; ok {
		return m
	}
	m := NewMemory(c.cfg.MemoryStore)
	if err := m.Load(sessionID); err != nil {
		m.SessionID = sessionID // new conversation
	}
	c.memories[sessionID] = m
	return m
}

// ListSessions returns stored conversation sessions (newest first), or nil when
// no memory backend is configured.
func (c *Coordinator) ListSessions() []SessionInfo {
	if s := c.memoryStore(); s != nil {
		out, err := s.ListSessions()
		if err != nil {
			return nil
		}
		return out
	}
	if c.cfg.MemoryStore == "" {
		return nil
	}
	return NewMemory(c.cfg.MemoryStore).List()
}

// SessionHistory returns the persisted messages for a session.
func (c *Coordinator) SessionHistory(sessionID string) []MemoryMessage {
	if s := c.memoryStore(); s != nil {
		out, err := s.Recent(sessionID, 0)
		if err != nil {
			return nil
		}
		return out
	}
	m := c.memory(sessionID)
	if m == nil {
		return nil
	}
	return m.Recent(0)
}

// recordExchange appends the user message + agent reply to memory and persists.
func (c *Coordinator) recordExchange(sessionID, userMsg, reply string) {
	if s := c.memoryStore(); s != nil {
		_ = s.AppendMessage(sessionID, "user", userMsg, nil)
		_ = s.AppendMessage(sessionID, "agent", reply, nil)
		return
	}
	m := c.memory(sessionID)
	if m == nil {
		return
	}
	m.Append("user", userMsg)
	m.Append("agent", reply)
	_ = m.Save()
}

// memoryStore returns the SQLite store under lock (nil when JSON-backed).
func (c *Coordinator) memoryStore() *MemoryStore {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store
}

// contextPrompt builds an AI prompt that includes the last few turns of history
// for continuity, followed by the current message.
func (c *Coordinator) contextPrompt(sessionID, userMsg string) string {
	var recent []MemoryMessage
	if s := c.memoryStore(); s != nil {
		msgs, err := s.Recent(sessionID, 10)
		if err != nil {
			return userMsg
		}
		recent = msgs
	} else {
		m := c.memory(sessionID)
		if m == nil {
			return userMsg
		}
		recent = m.Recent(10)
	}
	if len(recent) == 0 {
		return userMsg
	}
	var b strings.Builder
	b.WriteString("Conversation so far:\n")
	for _, msg := range recent {
		b.WriteString(msg.Role + ": " + msg.Content + "\n")
	}
	b.WriteString("\nCurrent message: " + userMsg)
	return b.String()
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
		cfg:      cfg,
		active:   make(map[string]Agent),
		pending:  make(map[string]*pendingApproval),
		sessions: make(map[string]*SessionState),
		memories: make(map[string]*Memory),
	}
	if c.cfg.WorkingDir == "" {
		c.cfg.WorkingDir, _ = os.Getwd()
	}
	// Detect the project context once, up front (read-only thereafter).
	c.projectCtx = describeProject(c.cfg.WorkingDir)
	c.projectCtxSet = true
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
	// Scope session-aware tools (write backups, undo) to this session.
	if params == nil {
		params = map[string]any{}
	}
	if _, ok := params["session_id"]; !ok {
		params["session_id"] = session
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

	// Approval required. If a synchronous approver (Telegram) is configured,
	// block on it as before. Otherwise (the TUI), register the pending action
	// and RETURN immediately with the preview — the caller's HTTP response must
	// not block, because the TUI can only render the approval box after Submit
	// returns. ApproveAction then executes the action when the user's
	// approve/reject POST arrives.
	emit("[APPROVAL_REQUIRED] " + ae.Request.Description)
	if ae.Request.Preview != "" {
		emit(ae.Request.Preview)
	}

	if c.cfg.Approval != nil {
		approved := c.cfg.Approval(ctx, ae.Request)
		if !approved {
			emit("✗ Action rejected by user")
			return nil, fmt.Errorf("agents: action rejected by user")
		}
		approvedResult, aerr := executeApprovedLocal(ctx, tool, params)
		if aerr != nil {
			emit("⚠ " + aerr.Error())
			return nil, aerr
		}
		emit(toolDoneLine(name, approvedResult))
		return approvedResult, nil
	}

	// Async (TUI) path: stash the action and return the preview now.
	c.mu.Lock()
	c.pending[session] = &pendingApproval{
		toolName: name, tool: tool, params: params,
		result: make(chan bool, 1),
	}
	c.mu.Unlock()
	return nil, nil
}

// ExecuteLocalToolSync runs a read-only local tool and returns its transcript
// as a string (for non-streaming callers like the Telegram /ls command). It is
// only intended for tools that don't require approval.
func (c *Coordinator) ExecuteLocalToolSync(name string, params map[string]any) (string, error) {
	var lines []string
	_, err := c.ExecuteLocalTool(context.Background(), "telegram", name, params, func(s string) {
		lines = append(lines, s)
	})
	if err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

// ApproveAction resolves a pending TUI approval for a session and, on approval,
// EXECUTES the stashed action — returning a transcript of the result (the
// original Submit already returned the preview, so the work happens here).
// matched is false when no action was pending for the session.
func (c *Coordinator) ApproveAction(session string, approved bool) (transcript string, matched bool) {
	c.mu.Lock()
	p, ok := c.pending[session]
	if ok {
		delete(c.pending, session)
	}
	c.mu.Unlock()
	if !ok {
		return "", false
	}

	// Legacy synchronous (Telegram) waiter, if any, still gets the decision.
	select {
	case p.result <- approved:
	default:
	}

	if !approved {
		slog.Default().Info("approval rejected", "tool", p.toolName, "session", session)
		return "✗ Action rejected by user", true
	}
	if p.tool == nil {
		return "✓ Action approved", true
	}

	slog.Default().Info("approval received, executing tool", "tool", p.toolName, "session", session)
	ctx, cancel := context.WithTimeout(context.Background(), handleMessageTimeout)
	defer cancel()
	result, err := executeApprovedLocal(ctx, p.tool, p.params)
	if err != nil {
		slog.Default().Error("tool execution after approval failed", "tool", p.toolName, "err", err)
		return "⚠ " + err.Error(), true
	}
	slog.Default().Info("tool executed after approval", "tool", p.toolName, "result", result)
	return toolDoneLine(p.toolName, result), true
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
	case GitAddTool:
		tl.RequireApproval = false
		return tl.Execute(ctx, params)
	case GitCommitTool:
		tl.RequireApproval = false
		return tl.Execute(ctx, params)
	case UndoTool:
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
	case "git_status":
		return "🔱 git status"
	case "git_diff":
		return "🔱 git diff"
	case "git_add":
		return "🔱 git add"
	case "git_commit":
		return "🔱 git commit: " + strParamOr(params, "message", "")
	case "search_files":
		return "🔎 Searching for: " + strParamOr(params, "pattern", "")
	case "find_files":
		return "🔎 Finding files: " + strParamOr(params, "name_pattern", "")
	case "undo":
		return "↩ Undo last write"
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
			var b strings.Builder
			if out := strings.TrimRight(stringOr(m["stdout"]), "\n"); out != "" {
				b.WriteString(out + "\n")
			}
			if errOut := strings.TrimRight(stringOr(m["stderr"]), "\n"); errOut != "" {
				b.WriteString("⚠ " + errOut + "\n")
			}
			b.WriteString(fmt.Sprintf("✓ Completed (exit %d)", code))
			return b.String()
		}
	case "create_project":
		if files, ok := m["files"].([]string); ok {
			return "✓ Project created: " + strings.Join(files, ", ")
		}
	case "list_directory":
		return "✓ Directory listed"
	case "git_status":
		branch, _ := m["branch"].(string)
		clean, _ := m["clean"].(bool)
		if clean {
			return "✓ branch " + branch + " — working tree clean"
		}
		return "✓ branch " + branch + " — changes present"
	case "git_diff":
		if d, _ := m["diff"].(string); d != "" {
			return d
		}
		return "✓ No changes"
	case "git_add":
		return "✓ Staged"
	case "git_commit":
		return "✓ Committed"
	case "search_files":
		if matches, ok := m["matches"].([]map[string]any); ok {
			if len(matches) == 0 {
				return "✓ No matches"
			}
			var b strings.Builder
			for _, mm := range matches {
				b.WriteString(fmt.Sprintf("%v:%v: %v\n", mm["file"], mm["line_number"], mm["line_content"]))
			}
			return strings.TrimRight(b.String(), "\n")
		}
	case "find_files":
		if files, ok := m["files"].([]string); ok {
			if len(files) == 0 {
				return "✓ No files found"
			}
			return strings.Join(files, "\n")
		}
	case "undo":
		if restored, ok := m["restored"].(string); ok {
			return "✓ Restored: " + restored
		}
	}
	return "✓ Done"
}

// stringOr returns v as a string, or "" if it is not a string.
func stringOr(v any) string {
	s, _ := v.(string)
	return s
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

	c.pruneIdleSessions()

	// If this session has a build awaiting clarification, this message is an
	// ANSWER — continue the SAME build with the original request + the answer,
	// instead of re-classifying it as a brand-new request (which would restart
	// the job and re-ask the same questions).
	if c.isAwaitingClarification(sessionID) {
		return c.continueClarification(ctx, sessionID, userMsg)
	}

	// Rule-based fast path FIRST: simple file/terminal operations and slash
	// commands route to local tools directly — no AI call, instant. Only fall
	// back to the AI classifier when the rules don't match.
	switch ruleClassify(userMsg) {
	case IntentLocalFile:
		return c.handleLocalFile(ctx, sessionID, userMsg)
	case IntentBuildApp:
		return c.dispatchBuild(ctx, sessionID, userMsg)
	case IntentResearch:
		return c.handleResearch(ctx, userMsg)
	case IntentDevOpsCheck:
		return c.handleDevOps(ctx, userMsg)
	case IntentDataPipeline:
		return c.handlePipeline(ctx, userMsg)
	case IntentOrchestrate:
		return c.handleOrchestrate(ctx, userMsg)
	}

	intent := c.classify(ctx, userMsg)

	switch intent {
	case IntentGeneralQuestion:
		sysPrompt := c.agentSystemPrompt()
		skill := c.findSkill(userMsg)
		if skill != nil {
			sysPrompt += skillPromptBlock(skill)
		}
		sysPrompt += c.recallMemoriesBlock(userMsg)
		reply, err := c.cfg.AIGateway.Complete(ctx, c.contextPrompt(sessionID, userMsg), sysPrompt)
		if skill != nil {
			c.markSkillUsed(skill.ID, err == nil)
		}
		if err == nil {
			c.recordExchange(sessionID, userMsg, reply)
			c.rememberExchangeAsync(sessionID, userMsg, reply)
		}
		return reply, err
	case IntentBuildApp:
		return c.dispatchBuild(ctx, sessionID, userMsg)
	case IntentResearch:
		return c.handleResearch(ctx, userMsg)
	case IntentDevOpsCheck:
		return c.handleDevOps(ctx, userMsg)
	case IntentDataPipeline:
		return c.handlePipeline(ctx, userMsg)
	default:
		return "I'm not sure what you'd like me to do. Could you clarify whether you want me to build an app, research something, run a DevOps check, or answer a question?", nil
	}
}

// dispatchBuild submits a fresh build request and records the session's
// original request so a later clarification turn can continue it.
func (c *Coordinator) dispatchBuild(ctx context.Context, sessionID, userMsg string) (string, error) {
	if c.cfg.BuildApp == nil {
		return c.modeStub("BUILD_APP"), nil
	}
	c.mu.Lock()
	c.sessions[sessionID] = &SessionState{
		OriginalRequest:       userMsg,
		AwaitingClarification: true, // forge MAY ask; the live check governs next turn
		LastActivity:          time.Now(),
	}
	c.mu.Unlock()
	return c.cfg.BuildApp(ctx, userMsg, sessionID)
}

// continueClarification treats userMsg as an answer to the pending clarifying
// questions: it resubmits the SAME build with the original request plus the
// accumulated answers, so forge builds with full context instead of restarting.
func (c *Coordinator) continueClarification(ctx context.Context, sessionID, answer string) (string, error) {
	c.mu.Lock()
	st := c.sessions[sessionID]
	if st == nil {
		c.mu.Unlock()
		return c.dispatchBuild(ctx, sessionID, answer)
	}
	st.Answers = append(st.Answers, answer)
	// Keep AwaitingClarification true: the resubmitted build is async, so a
	// further answer (if forge asks again) must also continue this session. The
	// live pending/clarifying check gates it — it returns false once the build
	// reaches a terminal state, so this cannot loop forever.
	st.LastActivity = time.Now()
	combined := st.OriginalRequest + "\n\nAdditional details: " + strings.Join(st.Answers, "; ")
	c.mu.Unlock()

	if c.cfg.BuildApp == nil {
		return c.modeStub("BUILD_APP"), nil
	}
	return c.cfg.BuildApp(ctx, combined, sessionID)
}

// isAwaitingClarification reports whether the session's build is awaiting an
// answer. It trusts the live forge state (SessionClarifying) when available,
// falling back to the stored flag.
func (c *Coordinator) isAwaitingClarification(sessionID string) bool {
	c.mu.Lock()
	st, ok := c.sessions[sessionID]
	c.mu.Unlock()
	if !ok || st == nil || !st.AwaitingClarification {
		return false
	}
	// We dispatched a build for this session and haven't yet consumed an answer.
	// Treat the next message as the answer while that build is still in flight
	// (pending) OR has explicitly asked (clarifying). The build is async, so it
	// may not have reached needs_clarification when the user replies — the
	// pending check closes that window (the root cause of the restart loop).
	clarifying, pending := false, false
	if c.cfg.SessionClarifying != nil {
		clarifying = c.cfg.SessionClarifying(sessionID)
	}
	if c.cfg.SessionPending != nil {
		pending = c.cfg.SessionPending(sessionID)
	}
	awaiting := clarifying || pending
	if c.cfg.SessionPending == nil && c.cfg.SessionClarifying == nil {
		awaiting = st.AwaitingClarification // no live hooks → trust the flag
	}
	slog.Default().Info("session state check",
		"session", sessionID, "awaiting", awaiting,
		"clarifying", clarifying, "pending", pending,
		"original", st.OriginalRequest)
	return awaiting
}

// ClearSession drops a session's state (called when its job completes/fails).
func (c *Coordinator) ClearSession(sessionID string) {
	c.mu.Lock()
	delete(c.sessions, sessionID)
	c.mu.Unlock()
}

// pruneIdleSessions evicts session state and in-memory conversation memories
// that are idle longer than sessionTTL, then enforces a hard cap on both maps
// so neither grows without bound under a stream of distinct session IDs
// (production audit H5). Memories are loaded from disk on demand, so evicting
// the cached copy is lossless. The hard cap evicts the oldest entries first.
func (c *Coordinator) pruneIdleSessions() {
	cutoff := time.Now().Add(-sessionTTL)
	c.mu.Lock()
	defer c.mu.Unlock()

	for id, st := range c.sessions {
		if st.LastActivity.Before(cutoff) {
			delete(c.sessions, id)
		}
	}
	for id, m := range c.memories {
		if m.UpdatedAt.Before(cutoff) {
			delete(c.memories, id)
		}
	}

	// Hard caps as a backstop against a burst of fresh IDs within one TTL
	// window (which idle-eviction alone would not catch).
	evictOldestSessions(c.sessions, maxCachedSessions)
	evictOldestMemories(c.memories, maxCachedMemories)
}

// evictOldestSessions trims the sessions map to at most limit entries,
// removing those with the oldest LastActivity first.
func evictOldestSessions(m map[string]*SessionState, limit int) {
	if len(m) <= limit {
		return
	}
	type kv struct {
		id string
		ts time.Time
	}
	entries := make([]kv, 0, len(m))
	for id, st := range m {
		entries = append(entries, kv{id, st.LastActivity})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ts.Before(entries[j].ts) })
	for _, e := range entries[:len(m)-limit] {
		delete(m, e.id)
	}
}

// evictOldestMemories trims the memories map to at most limit entries,
// removing those with the oldest UpdatedAt first.
func evictOldestMemories(m map[string]*Memory, limit int) {
	if len(m) <= limit {
		return
	}
	type kv struct {
		id string
		ts time.Time
	}
	entries := make([]kv, 0, len(m))
	for id, mem := range m {
		entries = append(entries, kv{id, mem.UpdatedAt})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ts.Before(entries[j].ts) })
	for _, e := range entries[:len(m)-limit] {
		delete(m, e.id)
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

// localFileKeywords route a message to local tools directly (no AI). Includes
// git operations (GIT_OP), which use the git tools without an AI round-trip.
var localFileKeywords = []string{
	"create a file", "write a file", "save to", "save it to", "save it in",
	"read file", "read the file", "list files", "list the files",
	"edit file", "edit the file", "run command", "run the command",
	"git status", "git diff", "git add", "git commit", "stage files",
	"what changed", "show changes", "commit",
	"search for", "find files", "search files",
}

// buildAppKeywords route a message to Forge (which needs AI intent parsing).
var buildAppKeywords = []string{
	"build me an app", "build an android", "build a web app", "build a mobile",
	"create a project", "build and deploy", "scaffold a project",
}

// researchKeywords route a message to the research agent.
var researchKeywords = []string{
	"research ", "look up ", "search for ", "find information about ",
	"tell me about ", "summarize ",
}

// devopsKeywords route a message to the DevOps agent (SSH/Docker/Nginx).
var devopsKeywords = []string{
	"ssh ", "server status", "vps ", "docker ", "container", "deploy ",
	"nginx ", "ssl cert", "restart service", "install package",
	"disk space", "list containers", "add nginx site", "enable ssl ",
}

// pipelineKeywords route a message to the data pipeline agent.
var pipelineKeywords = []string{
	"analyze ", "analyse ", "chart ", "plot ", "graph ", "visualize ",
	"visualise ", "csv ", "dataset", "data from ", "group by ",
}

// ruleClassify is a fast, AI-free classifier. It returns IntentLocalFile for
// simple file/terminal operations and slash commands, IntentBuildApp for real
// build requests, or IntentUnknown when no rule matches (caller falls back to
// the AI classifier). Slash commands and the explicit local-file keywords take
// precedence over build keywords so "create a file" never reaches Forge.
func ruleClassify(userMsg string) Intent {
	msg := strings.ToLower(strings.TrimSpace(userMsg))
	// Orchestration first — an explicit /orchestrate (or "orchestrate:") signals
	// a multi-agent goal that should decompose, not route to one agent.
	if strings.HasPrefix(msg, "/orchestrate") || strings.HasPrefix(msg, "orchestrate:") {
		return IntentOrchestrate
	}
	// Research next — it is a specific intent and uses prefix matching, so
	// "/research …" must NOT be swallowed by the generic "/" → LOCAL_FILE.
	if strings.HasPrefix(msg, "/research") {
		return IntentResearch
	}
	for _, kw := range researchKeywords {
		if strings.HasPrefix(msg, kw) {
			return IntentResearch
		}
	}
	for _, kw := range devopsKeywords {
		if strings.Contains(msg, kw) {
			return IntentDevOpsCheck
		}
	}
	for _, kw := range pipelineKeywords {
		if strings.HasPrefix(msg, kw) || strings.Contains(msg, " "+kw) {
			return IntentDataPipeline
		}
	}
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

// extractResearchQuery strips a leading research command/keyword to get the
// actual query.
func extractResearchQuery(userMsg string) string {
	msg := strings.TrimSpace(userMsg)
	low := strings.ToLower(msg)
	if strings.HasPrefix(low, "/research") {
		return strings.TrimSpace(msg[len("/research"):])
	}
	for _, kw := range researchKeywords {
		if strings.HasPrefix(low, kw) {
			return strings.TrimSpace(msg[len(kw):])
		}
	}
	return msg
}

// handleResearch dispatches a RESEARCH request to the research agent, streaming
// progress; returns the user-facing reply (or a stub when not wired).
func (c *Coordinator) handleResearch(ctx context.Context, userMsg string) (string, error) {
	if c.cfg.Research == nil {
		return c.modeStub("RESEARCH"), nil
	}
	query := extractResearchQuery(userMsg)
	if query == "" {
		return "What would you like me to research?", nil
	}
	// Progress is currently collected but not streamed back per-step (the runtime
	// returns a single reply); the research agent's report is the result.
	return c.cfg.Research(ctx, query, func(string) {})
}

// handleDevOps dispatches a DEVOPS request to the devops agent (or stubs when
// not wired / no server connected).
func (c *Coordinator) handleDevOps(ctx context.Context, userMsg string) (string, error) {
	if c.cfg.DevOps == nil {
		return c.modeStub("DEVOPS_CHECK"), nil
	}
	return c.cfg.DevOps(ctx, userMsg, func(string) {})
}

// handlePipeline dispatches a DATA_PIPELINE request to the pipeline agent (or
// stubs when not wired).
func (c *Coordinator) handlePipeline(ctx context.Context, userMsg string) (string, error) {
	if c.cfg.Pipeline == nil {
		return c.modeStub("DATA_PIPELINE"), nil
	}
	return c.cfg.Pipeline(ctx, userMsg, func(string) {})
}

// handleOrchestrate dispatches a multi-agent goal to the orchestration agent
// (or stubs when not wired). It strips a leading /orchestrate or "orchestrate:".
func (c *Coordinator) handleOrchestrate(ctx context.Context, userMsg string) (string, error) {
	if c.cfg.Orchestrate == nil {
		return c.modeStub("ORCHESTRATE"), nil
	}
	goal := strings.TrimSpace(userMsg)
	low := strings.ToLower(goal)
	switch {
	case strings.HasPrefix(low, "/orchestrate"):
		goal = strings.TrimSpace(goal[len("/orchestrate"):])
	case strings.HasPrefix(low, "orchestrate:"):
		goal = strings.TrimSpace(goal[len("orchestrate:"):])
	}
	if goal == "" {
		return "What goal should I orchestrate? Describe the multi-step task.", nil
	}

	// Surface a proven skill (if any) to the orchestrator, collect progress
	// steps, and feed the completed run back to the skill writer so the next
	// similar goal starts from a known-good procedure.
	var stepsMu sync.Mutex
	var steps []string
	progress := func(s string) {
		stepsMu.Lock()
		steps = append(steps, s)
		stepsMu.Unlock()
	}
	skill := c.findSkill(goal)
	runGoal := goal
	if skill != nil {
		runGoal = goal + skillPromptBlock(skill)
	}
	reply, err := c.cfg.Orchestrate(ctx, runGoal, progress)
	if skill != nil {
		c.markSkillUsed(skill.ID, err == nil)
	}
	stepsMu.Lock()
	taken := append([]string(nil), steps...)
	stepsMu.Unlock()
	c.maybeLearn(ctx, goal, taken, reply, err == nil)
	return reply, err
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
	c.maybeLearn(ctx, userMsg, steps, transcript, true)
	return transcript, nil
}

// detectProjectContext returns the project description. It is computed once at
// construction (NewCoordinator) and read-only thereafter, so it is safe to read
// without a lock from concurrent HandleMessage calls.
func (c *Coordinator) detectProjectContext() string {
	return c.projectCtx
}

// describeProject detects the project type at dir from its marker files.
func describeProject(dir string) string {
	if dir == "" {
		return ""
	}
	type marker struct {
		file, kind string
	}
	markers := []marker{
		{"go.mod", "Go"}, {"package.json", "Node"}, {"requirements.txt", "Python"},
		{"pubspec.yaml", "Flutter"}, {"pom.xml", "Java"}, {"Cargo.toml", "Rust"},
	}
	for _, mk := range markers {
		path := filepath.Join(dir, mk.file)
		if data, err := os.ReadFile(path); err == nil { //nolint:gosec // project root
			head := string(data)
			if len(head) > 200 {
				head = head[:200]
			}
			return fmt.Sprintf("%s project at %s (%s):\n%s", mk.kind, dir, mk.file, strings.TrimSpace(head))
		}
	}
	// C# detection (any .sln/.csproj).
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			n := strings.ToLower(e.Name())
			if strings.HasSuffix(n, ".sln") || strings.HasSuffix(n, ".csproj") {
				return fmt.Sprintf("C# project at %s (%s)", dir, e.Name())
			}
		}
	}
	return fmt.Sprintf("directory %s (no recognised project type)", dir)
}

// agentSystemPrompt builds the system prompt for direct answers, including the
// detected project context and tool-usage guidance.
func (c *Coordinator) agentSystemPrompt() string {
	ctx := c.detectProjectContext()
	if ctx == "" {
		return answerSystemPrompt
	}
	return "You are working in a " + ctx + "\n\n" +
		"When modifying code: use read_file first. When creating files: use " +
		"write_file (approval required). When running commands: use run_terminal. " +
		answerSystemPrompt
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
		case "status":
			return "git_status", map[string]any{}
		case "diff":
			return "git_diff", map[string]any{"file": rest}
		case "commit":
			return "git_commit", map[string]any{"message": rest}
		case "search":
			return "search_files", map[string]any{"pattern": rest}
		case "find":
			return "find_files", map[string]any{"name_pattern": rest}
		case "undo":
			return "undo", map[string]any{}
		default:
			return "", nil
		}
	}

	switch {
	case strings.HasPrefix(lower, "search for ") || strings.Contains(lower, "search files"):
		_, pat, _ := strings.Cut(lower, "search for ")
		return "search_files", map[string]any{"pattern": strings.TrimSpace(pat)}
	case strings.Contains(lower, "find files"):
		return "find_files", map[string]any{"name_pattern": "*"}
	case strings.Contains(lower, "git status") || lower == "what changed" || strings.Contains(lower, "show changes"):
		return "git_status", map[string]any{}
	case strings.Contains(lower, "git diff"):
		return "git_diff", map[string]any{}
	case strings.Contains(lower, "git commit") || strings.HasPrefix(lower, "commit "):
		// Use the message after "commit" as the commit message.
		_, m, _ := strings.Cut(lower, "commit")
		return "git_commit", map[string]any{"message": strings.TrimSpace(m)}
	case strings.Contains(lower, "git add") || strings.Contains(lower, "stage files"):
		return "git_add", map[string]any{"files": []string{"."}}
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
