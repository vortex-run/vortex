package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	neturl "net/url"
	"strings"
)

// --- Gemini generateContent with function calling ----------------------------

// geminiSchema copies a JSON Schema keeping only the keywords Gemini's
// OpenAPI-subset Schema accepts; anything else (additionalProperties,
// $schema, ...) makes the request fail.
func geminiSchema(s map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range s {
		switch k {
		case "type", "description", "enum", "format", "nullable", "required":
			out[k] = v
		case "properties":
			if props, ok := v.(map[string]any); ok {
				cp := map[string]any{}
				for name, sub := range props {
					if m, ok := sub.(map[string]any); ok {
						cp[name] = geminiSchema(m)
					}
				}
				out[k] = cp
			}
		case "items":
			if m, ok := v.(map[string]any); ok {
				out[k] = geminiSchema(m)
			}
		}
	}
	return out
}

// geminiContents converts the conversation. Assistant turns become "model"
// turns with functionCall parts (echoing any thoughtSignature); consecutive
// tool results are grouped into one user turn of functionResponse parts, as
// Gemini expects one response per call of the preceding model turn.
func geminiContents(msgs []ChatMessage) []map[string]any {
	var out []map[string]any
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch {
		case m.Role == "tool":
			var parts []map[string]any
			for ; i < len(msgs) && msgs[i].Role == "tool"; i++ {
				parts = append(parts, map[string]any{"functionResponse": map[string]any{
					"name": msgs[i].ToolName, "response": map[string]any{"content": msgs[i].Content},
				}})
			}
			i--
			out = append(out, map[string]any{"role": "user", "parts": parts})
		case m.Role == "assistant":
			var parts []map[string]any
			if m.Content != "" {
				parts = append(parts, map[string]any{"text": m.Content})
			}
			for _, c := range m.ToolCalls {
				part := map[string]any{"functionCall": map[string]any{"name": c.Name, "args": argsOrEmpty(c.Arguments)}}
				if c.Opaque != "" {
					part["thoughtSignature"] = c.Opaque
				}
				parts = append(parts, part)
			}
			if len(parts) == 0 {
				parts = append(parts, map[string]any{"text": ""})
			}
			out = append(out, map[string]any{"role": "model", "parts": parts})
		default:
			out = append(out, map[string]any{"role": "user", "parts": []map[string]any{{"text": m.Content}}})
		}
	}
	return out
}

func argsOrEmpty(a map[string]any) map[string]any {
	if a == nil {
		return map[string]any{}
	}
	return a
}

func (g *AIGateway) toolsGemini(ctx context.Context, p AIProvider, model, system string, msgs []ChatMessage, tools []ToolDef) (ToolChatResult, error) {
	base := p.Endpoint
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	if model == "" {
		model = "gemini-1.5-flash"
	}
	payload := map[string]any{"contents": geminiContents(msgs)}
	if system != "" {
		payload["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": system}}}
	}
	if len(tools) > 0 {
		decls := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			d := map[string]any{"name": t.Name, "description": t.Description}
			if ps := geminiSchema(t.Parameters); len(ps) > 0 {
				d["parameters"] = ps
			}
			decls = append(decls, d)
		}
		payload["tools"] = []map[string]any{{"functionDeclarations": decls}}
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					Thought      bool   `json:"thought"`
					FunctionCall *struct {
						ID   string          `json:"id"`
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
					ThoughtSignature string `json:"thoughtSignature"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			TotalTokenCount int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", strings.TrimRight(base, "/"), neturl.PathEscape(model))
	if _, err := g.doJSON(ctx, url, geminiHeaders(p), payload, &out); err != nil {
		return ToolChatResult{}, fmt.Errorf("gemini: %w", err)
	}
	if len(out.Candidates) == 0 {
		return ToolChatResult{}, fmt.Errorf("gemini: response has no candidates")
	}
	cand := out.Candidates[0]
	res := ToolChatResult{Stop: cand.FinishReason, Tokens: out.UsageMetadata.TotalTokenCount}
	var text []string
	for _, part := range cand.Content.Parts {
		if part.FunctionCall != nil {
			call := ToolCall{ID: part.FunctionCall.ID, Name: part.FunctionCall.Name, RawArgs: string(part.FunctionCall.Args), Opaque: part.ThoughtSignature}
			if call.ID == "" {
				call.ID = fmt.Sprintf("call_%d", len(res.ToolCalls))
			}
			if len(part.FunctionCall.Args) > 0 {
				if err := json.Unmarshal(part.FunctionCall.Args, &call.Arguments); err != nil {
					call.Arguments = nil // reported to the model by the caller's validator
				}
			}
			res.ToolCalls = append(res.ToolCalls, call)
			continue
		}
		if !part.Thought && part.Text != "" {
			text = append(text, part.Text)
		}
	}
	res.Text = strings.Join(text, "")
	if len(res.ToolCalls) > 0 {
		res.Stop = "tool_calls"
	}
	return res, nil
}
