package messaging

import (
	"context"
	"encoding/json"
	"fmt"
)

// --- Bedrock Converse API with toolConfig -------------------------------------
//
// Converse is Bedrock's model-neutral chat API, so one adapter covers every
// Bedrock model that supports tool use.

func bedrockConverseMessages(msgs []ChatMessage) []map[string]any {
	var out []map[string]any
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch {
		case m.Role == "tool":
			// Results for one assistant turn go back together in one user turn.
			var blocks []map[string]any
			for ; i < len(msgs) && msgs[i].Role == "tool"; i++ {
				blocks = append(blocks, map[string]any{"toolResult": map[string]any{
					"toolUseId": msgs[i].ToolCallID,
					"content":   []map[string]any{{"text": msgs[i].Content}},
				}})
			}
			i--
			out = append(out, map[string]any{"role": "user", "content": blocks})
		case m.Role == "assistant":
			var blocks []map[string]any
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"text": m.Content})
			}
			for _, c := range m.ToolCalls {
				blocks = append(blocks, map[string]any{"toolUse": map[string]any{
					"toolUseId": c.ID, "name": c.Name, "input": argsOrEmpty(c.Arguments),
				}})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{"text": " "}) // Converse rejects empty content
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
		default:
			out = append(out, map[string]any{"role": "user", "content": []map[string]any{{"text": m.Content}}})
		}
	}
	return out
}

func (g *AIGateway) toolsBedrock(ctx context.Context, p AIProvider, model, system string, msgs []ChatMessage, tools []ToolDef) (ToolChatResult, error) {
	region := p.Endpoint // Endpoint carries the region for bedrock
	if region == "" {
		region = "us-east-1"
	}
	accessKey, secretKey, ok := splitBedrockKey(p.APIKey)
	if !ok {
		return ToolChatResult{}, fmt.Errorf("bedrock: APIKey must be \"<accessKey>:<secretKey>\"")
	}
	if model == "" {
		model = "anthropic.claude-3-5-sonnet-20240620-v1:0"
	}
	host := fmt.Sprintf("bedrock-runtime.%s.amazonaws.com", region)
	wirePath, canonicalPath := bedrockModelPath(model, "converse")

	payload := map[string]any{
		"messages":        bedrockConverseMessages(msgs),
		"inferenceConfig": map[string]any{"maxTokens": 4096},
	}
	if system != "" {
		payload["system"] = []map[string]any{{"text": system}}
	}
	if len(tools) > 0 {
		specs := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			schema := t.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			specs = append(specs, map[string]any{"toolSpec": map[string]any{
				"name": t.Name, "description": t.Description, "inputSchema": map[string]any{"json": schema},
			}})
		}
		payload["toolConfig"] = map[string]any{"tools": specs}
	}

	var out struct {
		Output struct {
			Message struct {
				Content []struct {
					Text    string `json:"text"`
					ToolUse *struct {
						ToolUseID string          `json:"toolUseId"`
						Name      string          `json:"name"`
						Input     json.RawMessage `json:"input"`
					} `json:"toolUse"`
				} `json:"content"`
			} `json:"message"`
		} `json:"output"`
		StopReason string `json:"stopReason"`
		Usage      struct {
			TotalTokens int `json:"totalTokens"`
		} `json:"usage"`
	}
	signer := func(headers map[string]string, body []byte) {
		bedrockSignV4(headers, body, "POST", host, canonicalPath, accessKey, secretKey, region, "bedrock", g.now())
	}
	if _, err := g.doSignedJSON(ctx, "https://"+host+wirePath, signer, payload, &out); err != nil {
		return ToolChatResult{}, fmt.Errorf("bedrock: %w", err)
	}
	res := ToolChatResult{Stop: out.StopReason, Tokens: out.Usage.TotalTokens}
	for _, b := range out.Output.Message.Content {
		if b.ToolUse != nil {
			call := ToolCall{ID: b.ToolUse.ToolUseID, Name: b.ToolUse.Name, RawArgs: string(b.ToolUse.Input)}
			if err := json.Unmarshal(b.ToolUse.Input, &call.Arguments); err != nil {
				call.Arguments = nil // reported to the model by the caller's validator
			}
			res.ToolCalls = append(res.ToolCalls, call)
			continue
		}
		res.Text += b.Text
	}
	if len(res.ToolCalls) > 0 {
		res.Stop = "tool_calls"
	}
	return res, nil
}
