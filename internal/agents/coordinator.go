package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	IntentResearch        Intent = "RESEARCH"
	IntentDevOpsCheck     Intent = "DEVOPS_CHECK"
	IntentDataPipeline    Intent = "DATA_PIPELINE"
	IntentGeneralQuestion Intent = "GENERAL_QUESTION"
	IntentUnknown         Intent = "UNKNOWN"
)

const intentSystemPrompt = `You are the VORTEX coordinator. Classify the user's request into exactly one of: BUILD_APP, RESEARCH, DEVOPS_CHECK, DATA_PIPELINE, GENERAL_QUESTION, UNKNOWN. Respond with only the classification keyword.`

const answerSystemPrompt = `You are the VORTEX assistant. Answer the user's question concisely.`

// ApprovalFunc requests human approval for an action (e.g. a run_command).
// It returns true if approved, false if rejected or timed out. The messaging
// layer supplies the real implementation (Telegram approve/reject buttons);
// when nil, approval-gated actions are denied by default (fail safe).
type ApprovalFunc func(ctx context.Context, req ApprovalRequest) bool

// CoordinatorConfig configures the user-facing coordinator agent.
type CoordinatorConfig struct {
	Bus       *Bus
	Tools     *SandboxedToolRegistry
	AIGateway AIGateway
	MaxAgents int          // concurrent sub-agent limit (default 8)
	Approval  ApprovalFunc // human-in-the-loop approval; nil = deny gated actions
}

// Coordinator is the single user-facing agent. It classifies user messages,
// routes them to the appropriate mode handler, and supervises spawned
// sub-agents. It implements Agent via an embedded BaseAgent.
type Coordinator struct {
	*BaseAgent
	cfg CoordinatorConfig

	mu     sync.Mutex
	active map[string]Agent
}

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
		cfg:    cfg,
		active: make(map[string]Agent),
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

// HandleMessage is the main entry point for a user message. It classifies the
// intent via the AI gateway and routes to the matching handler. For M10, the
// BUILD_APP/RESEARCH/DEVOPS/DATA handlers are stubs; GENERAL_QUESTION is
// answered directly and UNKNOWN returns a clarifying question.
func (c *Coordinator) HandleMessage(ctx context.Context, userMsg, sessionID string) (string, error) {
	if strings.TrimSpace(userMsg) == "" {
		return "", fmt.Errorf("agents: empty user message")
	}
	// sessionID scopes future per-conversation state (memory, cost, sub-agent
	// reuse); the routing in M10 is stateless, so we only validate it here.
	_ = sessionID
	intent := c.classify(ctx, userMsg)

	switch intent {
	case IntentGeneralQuestion:
		return c.cfg.AIGateway.Complete(ctx, userMsg, answerSystemPrompt)
	case IntentBuildApp:
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
