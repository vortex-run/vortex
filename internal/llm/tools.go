// Package llm holds provider-neutral types for tool-using model calls, shared
// by the gateway (internal/messaging) and the agent loop (internal/agents)
// without either importing the other.
package llm

import "errors"

// ErrToolsUnsupported means no configured provider can take tool definitions
// natively (e.g. only Bedrock is configured). Callers fall back to
// a text protocol or report the capability gap; nothing is silently degraded.
var ErrToolsUnsupported = errors.New("llm: no configured provider supports native tool calling")

// ToolDef is a tool offered to the model: name, description and a JSON
// Schema object for its arguments.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall is one tool invocation proposed by the model.
type ToolCall struct {
	ID        string         // provider call ID, echoed back in the result
	Name      string         // tool name
	Arguments map[string]any // decoded arguments
	RawArgs   string         // the arguments exactly as the model produced them
	// Opaque is provider data that must be sent back with this call on the
	// next turn (Gemini's thoughtSignature); never shown to the model or user.
	Opaque string
}

// ChatMessage is one turn in a tool-using conversation.
type ChatMessage struct {
	Role       string     // "user", "assistant" or "tool"
	Content    string     // text (for "tool": the tool result)
	ToolCalls  []ToolCall // assistant turns that called tools
	ToolCallID string     // "tool" turns: the call this result answers
	ToolName   string     // "tool" turns: the tool that produced it
}

// ToolChatResult is the model's reply: text, tool calls, or both.
type ToolChatResult struct {
	Text      string
	ToolCalls []ToolCall
	Stop      string // "tool_calls", "end", "length", or the provider's reason
	Provider  string
	Model     string
	Tokens    int
}
