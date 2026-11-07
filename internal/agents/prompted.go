package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/sandeepbollavaram/vortex/internal/llm"
)

// PromptedTools drives the agent loop with a model that has no native tool
// calling (many small local models). Tools are described in the prompt and
// the model asks for one by replying with a fenced JSON block; anything else
// is its answer. It is the fallback rung below native tools: less reliable,
// but every proposed call still goes through the loop's validation and
// approval gate exactly as a native call does.
type PromptedTools struct {
	Model AIGateway
}

const promptedProtocol = `
To use a tool, reply with ONLY a fenced JSON block naming one tool, then stop:

` + "```json" + `
{"tool": "<tool name>", "args": {<arguments>}}
` + "```" + `

You will get the result in a <tool_result> block. Call one tool at a time.
When the task is done, reply with your answer as plain text and no JSON block.`

// ChatWithTools renders the conversation as text, asks the model, and parses
// a tool request out of the reply.
func (p PromptedTools) ChatWithTools(ctx context.Context, system string, msgs []llm.ChatMessage, tools []llm.ToolDef) (llm.ToolChatResult, error) {
	reply, err := p.Model.Complete(ctx, renderPromptedTranscript(msgs), promptedSystem(system, tools))
	if err != nil {
		return llm.ToolChatResult{}, err
	}
	call, text, ok := parsePromptedCall(reply)
	if !ok {
		return llm.ToolChatResult{Text: strings.TrimSpace(reply), Stop: "end"}, nil
	}
	call.ID = fmt.Sprintf("p%d", countAssistantCalls(msgs)+1)
	return llm.ToolChatResult{Text: text, ToolCalls: []llm.ToolCall{call}, Stop: "tool_calls"}, nil
}

func promptedSystem(system string, tools []llm.ToolDef) string {
	var b strings.Builder
	b.WriteString(system)
	if len(tools) > 0 {
		b.WriteString("\n\nTools you can use:\n")
		for _, t := range tools {
			b.WriteString("- ")
			b.WriteString(t.Name)
			b.WriteString(": ")
			b.WriteString(t.Description)
			if args := describeArgs(t.Parameters); args != "" {
				b.WriteString(" Arguments: ")
				b.WriteString(args)
			}
			b.WriteString("\n")
		}
		b.WriteString(promptedProtocol)
	}
	return b.String()
}

// describeArgs summarises a schema's properties as "name (type, required)".
func describeArgs(schema map[string]any) string {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return ""
	}
	required := map[string]bool{}
	switch r := schema["required"].(type) {
	case []string:
		for _, n := range r {
			required[n] = true
		}
	case []any:
		for _, n := range r {
			if s, ok := n.(string); ok {
				required[s] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		typ := "any"
		if m, ok := props[n].(map[string]any); ok {
			if t, ok := m["type"].(string); ok {
				typ = t
			}
		}
		if required[n] {
			parts = append(parts, fmt.Sprintf("%s (%s, required)", n, typ))
		} else {
			parts = append(parts, fmt.Sprintf("%s (%s)", n, typ))
		}
	}
	return strings.Join(parts, ", ")
}

// renderPromptedTranscript flattens the conversation. Tool results are
// fenced so the model can tell them from the user, and a closing tag inside
// a result can't end the fence early.
func renderPromptedTranscript(msgs []llm.ChatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			content := strings.ReplaceAll(m.Content, "</tool_result>", "<\\/tool_result>")
			fmt.Fprintf(&b, "<tool_result tool=%q>\n%s\n</tool_result>\n\n", m.ToolName, content)
		case "assistant":
			if m.Content != "" {
				fmt.Fprintf(&b, "Assistant: %s\n", m.Content)
			}
			for _, c := range m.ToolCalls {
				args, _ := json.Marshal(c.Arguments)
				fmt.Fprintf(&b, "Assistant called tool %s with %s\n\n", c.Name, args)
			}
		default:
			fmt.Fprintf(&b, "User: %s\n\n", m.Content)
		}
	}
	b.WriteString("Assistant:")
	return b.String()
}

var fencedJSON = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// parsePromptedCall finds a {"tool": ..., "args": ...} request: in a fenced
// block, or as the whole reply. text is whatever came before it.
func parsePromptedCall(reply string) (llm.ToolCall, string, bool) {
	candidates := []struct{ json, before string }{}
	if m := fencedJSON.FindStringSubmatchIndex(reply); m != nil {
		candidates = append(candidates, struct{ json, before string }{reply[m[2]:m[3]], reply[:m[0]]})
	}
	if t := strings.TrimSpace(reply); strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
		candidates = append(candidates, struct{ json, before string }{t, ""})
	}
	for _, c := range candidates {
		var req struct {
			Tool string          `json:"tool"`
			Args json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal([]byte(c.json), &req); err != nil || req.Tool == "" {
			continue
		}
		call := llm.ToolCall{Name: req.Tool, RawArgs: string(req.Args)}
		if len(req.Args) > 0 {
			if err := json.Unmarshal(req.Args, &call.Arguments); err != nil {
				call.Arguments = nil // the loop reports invalid arguments to the model
			}
		} else {
			call.Arguments = map[string]any{}
		}
		return call, strings.TrimSpace(c.before), true
	}
	return llm.ToolCall{}, "", false
}

func countAssistantCalls(msgs []llm.ChatMessage) int {
	n := 0
	for _, m := range msgs {
		n += len(m.ToolCalls)
	}
	return n
}

// FallbackTools uses Native until it reports that tools are unsupported
// (llm.ErrToolsUnsupported), then Prompted for the rest of the run. OnFallback
// is called once when that happens.
type FallbackTools struct {
	Native     ToolChatter
	Prompted   ToolChatter
	OnFallback func(reason error)
	fellBack   bool
}

// ChatWithTools implements ToolChatter.
func (f *FallbackTools) ChatWithTools(ctx context.Context, system string, msgs []llm.ChatMessage, tools []llm.ToolDef) (llm.ToolChatResult, error) {
	if !f.fellBack && f.Native != nil {
		res, err := f.Native.ChatWithTools(ctx, system, msgs, tools)
		if err == nil || !errors.Is(err, llm.ErrToolsUnsupported) {
			return res, err
		}
		f.fellBack = true
		if f.OnFallback != nil {
			f.OnFallback(err)
		}
	}
	return f.Prompted.ChatWithTools(ctx, system, msgs, tools)
}
