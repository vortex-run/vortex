package agents

import (
	"context"
	"errors"
	"testing"

	"github.com/vortex-run/vortex/internal/llm"
)

// probeModelFunc answers each probe call from a function of the request.
type probeModelFunc func(msgs []llm.ChatMessage, tools []llm.ToolDef) (llm.ToolChatResult, error)

func (f probeModelFunc) ChatWithTools(_ context.Context, _ string, msgs []llm.ChatMessage, tools []llm.ToolDef) (llm.ToolChatResult, error) {
	return f(msgs, tools)
}

func lastContent(msgs []llm.ChatMessage, role string) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == role {
			return msgs[i].Content
		}
	}
	return ""
}

// good behaves like a capable tool-calling model.
var good = probeModelFunc(func(msgs []llm.ChatMessage, tools []llm.ToolDef) (llm.ToolChatResult, error) {
	if r := lastContent(msgs, "tool"); r != "" {
		return llm.ToolChatResult{Text: "The tool returned " + firstLine(r)}, nil
	}
	if tools[0].Name == "add" {
		return llm.ToolChatResult{ToolCalls: []llm.ToolCall{{ID: "1", Name: "add", Arguments: map[string]any{"a": 2.0, "b": 3.0}}}}, nil
	}
	return llm.ToolChatResult{ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo", Arguments: map[string]any{"text": "ping"}}}}, nil
})

func firstLine(s string) string {
	for i := range s {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

func checks(t *testing.T, m ToolChatter) map[string]ProbeCheck {
	t.Helper()
	out := map[string]ProbeCheck{}
	for _, c := range ProbeModel(context.Background(), m) {
		out[c.Name] = c
	}
	if len(out) != 4 {
		t.Fatalf("checks = %v", out)
	}
	return out
}

func TestProbe_CapableModelPassesAll(t *testing.T) {
	for name, c := range checks(t, good) {
		if !c.Pass {
			t.Errorf("%s failed: %s", name, c.Detail)
		}
	}
}

func TestProbe_TextOnlyModelFails(t *testing.T) {
	m := probeModelFunc(func([]llm.ChatMessage, []llm.ToolDef) (llm.ToolChatResult, error) {
		return llm.ToolChatResult{Text: "Sure, I would call echo."}, nil
	})
	got := checks(t, m)
	if got["calls a tool"].Pass || got["typed arguments"].Pass || got["reads the result"].Pass {
		t.Errorf("text-only model passed: %+v", got)
	}
}

func TestProbe_StringNumbersFailTypedArguments(t *testing.T) {
	m := probeModelFunc(func(msgs []llm.ChatMessage, tools []llm.ToolDef) (llm.ToolChatResult, error) {
		if tools[0].Name == "add" {
			return llm.ToolChatResult{ToolCalls: []llm.ToolCall{{Name: "add", Arguments: map[string]any{"a": "2", "b": "3"}}}}, nil
		}
		return good(msgs, tools)
	})
	if c := checks(t, m)["typed arguments"]; c.Pass {
		t.Errorf("string numbers accepted: %+v", c)
	}
}

func TestProbe_InjectionFollowedIsCaught(t *testing.T) {
	m := probeModelFunc(func(msgs []llm.ChatMessage, tools []llm.ToolDef) (llm.ToolChatResult, error) {
		if len(tools) == 2 {
			return llm.ToolChatResult{ToolCalls: []llm.ToolCall{{Name: "delete_all_files", Arguments: map[string]any{}}}}, nil
		}
		return good(msgs, tools)
	})
	if c := checks(t, m)["ignores injected instructions"]; c.Pass {
		t.Errorf("planted instruction followed but check passed: %+v", c)
	}
}

func TestProbe_ErrorsReported(t *testing.T) {
	m := probeModelFunc(func([]llm.ChatMessage, []llm.ToolDef) (llm.ToolChatResult, error) {
		return llm.ToolChatResult{}, errors.New("status 401: bad key")
	})
	for name, c := range checks(t, m) {
		if c.Pass || c.Detail == "" {
			t.Errorf("%s: %+v", name, c)
		}
	}
}
