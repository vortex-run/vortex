package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Native tool calling. Complete is text in / text out; ChatWithTools sends
// tool definitions in each provider's native format and returns the model's
// proposed tool calls, normalized. It only proposes: the caller validates
// the arguments and decides whether anything runs.

// ErrToolsUnsupported means no configured provider can take tool definitions
// natively (e.g. only Gemini or Bedrock are configured). Callers fall back to
// a text protocol or report the capability gap; nothing is silently degraded.
var ErrToolsUnsupported = errors.New("messaging: no configured provider supports native tool calling")

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

// SupportsNativeTools reports whether ChatWithTools can use p.
func SupportsNativeTools(p AIProvider) bool {
	switch p.Name {
	case ProviderOpenAI, ProviderDeepSeek, ProviderGroq, ProviderOpenRouter, ProviderAzureOpenAI,
		ProviderClaude, ProviderOllama:
		return true
	}
	return false
}

// ChatWithTools sends messages and tool definitions to the first configured
// provider that supports native tool calling, falling back to the next on
// error, and returns the reply. Providers without native tools are skipped.
func (g *AIGateway) ChatWithTools(ctx context.Context, system string, msgs []ChatMessage, tools []ToolDef) (ToolChatResult, error) {
	if g.budgetExceeded() {
		return ToolChatResult{}, ErrBudgetExceeded
	}
	var lastErr error
	tried := false
	for _, p := range g.cfg.Providers {
		if !SupportsNativeTools(p) {
			continue
		}
		tried = true
		res, err := g.chatToolsProvider(ctx, p, system, msgs, tools)
		if err != nil {
			lastErr = err
			continue
		}
		g.recordCost(res.Model, res.Tokens)
		g.mu.Lock()
		g.rolloverLocked()
		g.requestsToday++
		g.mu.Unlock()
		return res, nil
	}
	if !tried {
		return ToolChatResult{}, ErrToolsUnsupported
	}
	return ToolChatResult{}, lastErr
}

func (g *AIGateway) chatToolsProvider(ctx context.Context, p AIProvider, system string, msgs []ChatMessage, tools []ToolDef) (ToolChatResult, error) {
	model := g.modelOf(p)
	var res ToolChatResult
	var err error
	switch p.Name {
	case ProviderClaude:
		res, err = g.toolsAnthropic(ctx, p, model, system, msgs, tools)
	case ProviderOllama:
		res, err = g.toolsOllama(ctx, p, model, system, msgs, tools)
	default:
		res, err = g.toolsOpenAICompat(ctx, p, model, system, msgs, tools)
	}
	if err != nil {
		return ToolChatResult{}, err
	}
	res.Provider, res.Model = p.Name, model
	return res, nil
}

// --- OpenAI-compatible (OpenAI, DeepSeek, Groq, OpenRouter, Azure) ----------

func openAITools(tools []ToolDef) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": t.Parameters,
		}})
	}
	return out
}

func openAIMessages(system string, msgs []ChatMessage, argsAsString bool) []map[string]any {
	out := []map[string]any{}
	if system != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, m := range msgs {
		msg := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				fn := map[string]any{"name": c.Name}
				if argsAsString {
					fn["arguments"] = rawArgs(c)
				} else {
					fn["arguments"] = c.Arguments
				}
				call := map[string]any{"type": "function", "function": fn}
				if c.ID != "" {
					call["id"] = c.ID
				}
				calls = append(calls, call)
			}
			msg["tool_calls"] = calls
		}
		if m.Role == "tool" {
			if m.ToolCallID != "" {
				msg["tool_call_id"] = m.ToolCallID
			}
			if m.ToolName != "" && !argsAsString { // Ollama keys tool results by name
				msg["tool_name"] = m.ToolName
			}
		}
		out = append(out, msg)
	}
	return out
}

func rawArgs(c ToolCall) string {
	if c.RawArgs != "" {
		return c.RawArgs
	}
	b, _ := json.Marshal(c.Arguments)
	return string(b)
}

func (g *AIGateway) toolsOpenAICompat(ctx context.Context, p AIProvider, model, system string, msgs []ChatMessage, tools []ToolDef) (ToolChatResult, error) {
	url, headers, prefix, includeModel, err := openaiStreamTarget(p, model)
	if err != nil {
		return ToolChatResult{}, err
	}
	payload := map[string]any{"messages": openAIMessages(system, msgs, true)}
	if includeModel {
		payload["model"] = model
	}
	if len(tools) > 0 {
		payload["tools"] = openAITools(tools)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if _, err := g.doJSON(ctx, url, headers, payload, &out); err != nil {
		return ToolChatResult{}, fmt.Errorf("%s: %w", prefix, err)
	}
	if len(out.Choices) == 0 {
		return ToolChatResult{}, fmt.Errorf("%s: response has no choices", prefix)
	}
	ch := out.Choices[0]
	res := ToolChatResult{Text: ch.Message.Content, Stop: ch.FinishReason, Tokens: out.Usage.TotalTokens}
	for _, tc := range ch.Message.ToolCalls {
		call := ToolCall{ID: tc.ID, Name: tc.Function.Name, RawArgs: tc.Function.Arguments}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &call.Arguments); err != nil {
			// Keep the call; the caller's validator reports the bad arguments
			// back to the model rather than the gateway dropping it.
			call.Arguments = nil
		}
		res.ToolCalls = append(res.ToolCalls, call)
	}
	if len(res.ToolCalls) > 0 {
		res.Stop = "tool_calls"
	}
	return res, nil
}

// --- Anthropic Messages API ---------------------------------------------------

func (g *AIGateway) toolsAnthropic(ctx context.Context, p AIProvider, model, system string, msgs []ChatMessage, tools []ToolDef) (ToolChatResult, error) {
	base := p.Endpoint
	if base == "" {
		base = "https://api.anthropic.com"
	}
	var amsgs []map[string]any
	for _, m := range msgs {
		switch {
		case m.Role == "tool":
			// Tool results go back as a user turn with tool_result blocks.
			amsgs = append(amsgs, map[string]any{"role": "user", "content": []map[string]any{{
				"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content,
			}}})
		case len(m.ToolCalls) > 0:
			blocks := []map[string]any{}
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, c := range m.ToolCalls {
				input := c.Arguments
				if input == nil {
					input = map[string]any{}
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": input})
			}
			amsgs = append(amsgs, map[string]any{"role": "assistant", "content": blocks})
		default:
			amsgs = append(amsgs, map[string]any{"role": m.Role, "content": m.Content})
		}
	}
	payload := map[string]any{"model": model, "max_tokens": 4096, "messages": amsgs}
	if system != "" {
		payload["system"] = system
	}
	if len(tools) > 0 {
		at := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			at = append(at, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Parameters})
		}
		payload["tools"] = at
	}
	var out struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if _, err := g.doJSON(ctx, base+"/v1/messages",
		map[string]string{"x-api-key": p.APIKey, "anthropic-version": "2023-06-01"}, payload, &out); err != nil {
		return ToolChatResult{}, fmt.Errorf("claude: %w", err)
	}
	res := ToolChatResult{Stop: out.StopReason, Tokens: out.Usage.InputTokens + out.Usage.OutputTokens}
	var text []string
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "tool_use":
			call := ToolCall{ID: b.ID, Name: b.Name, RawArgs: string(b.Input)}
			_ = json.Unmarshal(b.Input, &call.Arguments)
			res.ToolCalls = append(res.ToolCalls, call)
		}
	}
	res.Text = strings.Join(text, "\n")
	if res.Stop == "tool_use" || len(res.ToolCalls) > 0 {
		res.Stop = "tool_calls"
	}
	return res, nil
}

// --- Ollama /api/chat ---------------------------------------------------------

func (g *AIGateway) toolsOllama(ctx context.Context, p AIProvider, model, system string, msgs []ChatMessage, tools []ToolDef) (ToolChatResult, error) {
	base := p.Endpoint
	if base == "" {
		base = "http://localhost:11434"
	}
	payload := map[string]any{
		"model": model, "stream": false,
		"messages": openAIMessages(system, msgs, false), // Ollama takes arguments as an object
	}
	if len(tools) > 0 {
		payload["tools"] = openAITools(tools)
	}
	var out struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		DoneReason      string `json:"done_reason"`
		EvalCount       int    `json:"eval_count"`
		PromptEvalCount int    `json:"prompt_eval_count"`
	}
	if _, err := g.doJSON(ctx, strings.TrimRight(base, "/")+"/api/chat", nil, payload, &out); err != nil {
		return ToolChatResult{}, fmt.Errorf("ollama: %w", err)
	}
	res := ToolChatResult{Text: out.Message.Content, Stop: out.DoneReason, Tokens: out.EvalCount + out.PromptEvalCount}
	for i, tc := range out.Message.ToolCalls {
		raw, _ := json.Marshal(tc.Function.Arguments)
		res.ToolCalls = append(res.ToolCalls, ToolCall{
			ID: fmt.Sprintf("call_%d", i), Name: tc.Function.Name, Arguments: tc.Function.Arguments, RawArgs: string(raw),
		})
	}
	if len(res.ToolCalls) > 0 {
		res.Stop = "tool_calls"
	}
	return res, nil
}
