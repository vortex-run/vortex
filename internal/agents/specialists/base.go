// Package specialists implements VORTEX's specialist agents (coder, tester,
// reviewer) that collaborate over the A2A protocol. Each embeds BaseAgent for
// shared AI-completion, tool execution, and progress reporting; each overrides
// HandleTask with its own focused behaviour.
package specialists

import (
	"context"
	"fmt"

	"github.com/vortex-run/vortex/internal/a2a"
)

// AIGateway is the completion interface specialists use (satisfied by the
// messaging AI gateway and the agents stub).
type AIGateway interface {
	Complete(ctx context.Context, prompt, systemPrompt string) (string, error)
}

// ToolFunc executes a named tool with params and returns its result. The
// agents/team package builds this from a *agents.ToolRegistry, keeping the
// specialists package free of an agents import (which would cycle, since
// agents/team.go imports specialists).
type ToolFunc func(ctx context.Context, name string, params map[string]any) (any, error)

// BaseAgent provides the shared machinery every specialist needs: its A2A
// card, an AI gateway, a tool executor, an A2A client to call peers, and the
// working directory.
type BaseAgent struct {
	card      a2a.AgentCard
	gateway   AIGateway
	runTool   ToolFunc
	client    *a2a.AgentClient
	workDir   string
	sysPrompt string // the agent's role system prompt (for direct chat)
}

// NewBaseAgent constructs a BaseAgent. runTool may be nil for agents that do
// no tool work (e.g. the reviewer reads via the same executor when provided).
func NewBaseAgent(card a2a.AgentCard, gateway AIGateway, runTool ToolFunc, client *a2a.AgentClient, workDir string) *BaseAgent {
	if card.Status == "" {
		card.Status = a2a.StatusIdle
	}
	return &BaseAgent{card: card, gateway: gateway, runTool: runTool, client: client, workDir: workDir}
}

// Card returns the agent's A2A card.
func (b *BaseAgent) Card() a2a.AgentCard { return b.card }

// SetStatus updates the card's live status.
func (b *BaseAgent) SetStatus(status string) { b.card.Status = status }

// SetSystemPrompt records the agent's role system prompt so direct chat
// answers in character.
func (b *BaseAgent) SetSystemPrompt(p string) { b.sysPrompt = p }

// Chat answers a user message directly (not as a task), using the agent's own
// system prompt plus the supplied conversation context. Implements a2a.Chatter.
func (b *BaseAgent) Chat(ctx context.Context, taskContext, userMessage string) (string, error) {
	if b.gateway == nil {
		return "", fmt.Errorf("specialists: no AI gateway for direct chat")
	}
	prompt := userMessage
	if taskContext != "" {
		prompt = "Conversation so far:\n" + taskContext + "\nUser: " + userMessage
	}
	return b.gateway.Complete(ctx, prompt, b.sysPrompt)
}

// WorkDir returns the agent's working directory.
func (b *BaseAgent) WorkDir() string { return b.workDir }

// Complete calls the AI gateway with the agent's system prompt. The gateway's
// key rotation (when enabled) and provider routing apply transparently.
func (b *BaseAgent) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if b.gateway == nil {
		return "", fmt.Errorf("specialists: no AI gateway configured")
	}
	return b.gateway.Complete(ctx, userPrompt, systemPrompt)
}

// RunTool executes a tool via the configured executor. Specialist agents are
// trusted — the human approved the overall team task — so file operations run
// without an approval gate (the executor is built from NewTrustedLocalTools).
// RunTerminal keeps its approval gate, which surfaces here as an error the
// caller handles.
func (b *BaseAgent) RunTool(ctx context.Context, toolName string, params map[string]any) (any, error) {
	if b.runTool == nil {
		return nil, fmt.Errorf("specialists: no tool executor configured")
	}
	return b.runTool(ctx, toolName, params)
}

// Progress sends a structured progress update via progressFn (a no-op when nil).
func (b *BaseAgent) Progress(progressFn func(a2a.Progress), taskID, message string, step, total int) {
	if progressFn == nil {
		return
	}
	p := a2a.NewProgress(taskID, b.card.ID, message)
	p.Step = step
	p.TotalSteps = total
	progressFn(*p)
}
