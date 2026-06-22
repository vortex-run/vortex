package specialists

import (
	"context"
	"fmt"
	"testing"

	"github.com/vortex-run/vortex/internal/a2a"
)

// fakeGateway records the prompts it receives and returns a fixed reply.
type fakeGateway struct {
	reply      string
	err        error
	lastSystem string
	lastUser   string
}

func (g *fakeGateway) Complete(_ context.Context, prompt, system string) (string, error) {
	g.lastUser = prompt
	g.lastSystem = system
	return g.reply, g.err
}

// recordingTool records calls and returns a fixed result.
func recordingTool(calls *[]string, result any, err error) ToolFunc {
	return func(_ context.Context, name string, params map[string]any) (any, error) {
		*calls = append(*calls, name+":"+fmt.Sprint(params["path"]))
		return result, err
	}
}

func sampleCard() a2a.AgentCard {
	return a2a.AgentCard{ID: "code-agent", Name: "Code", Role: "coder",
		Capabilities: []string{"write_code"}, AIModel: "deepseek-chat"}
}

func TestBase_Card(t *testing.T) {
	b := NewBaseAgent(sampleCard(), &fakeGateway{}, nil, nil, "/work")
	card := b.Card()
	if card.ID != "code-agent" || card.Role != "coder" {
		t.Errorf("Card = %+v", card)
	}
	// Status defaults to idle.
	if card.Status != a2a.StatusIdle {
		t.Errorf("default status = %q, want idle", card.Status)
	}
	if b.WorkDir() != "/work" {
		t.Errorf("WorkDir = %q", b.WorkDir())
	}
}

func TestBase_SetStatus(t *testing.T) {
	b := NewBaseAgent(sampleCard(), &fakeGateway{}, nil, nil, "/work")
	b.SetStatus(a2a.StatusBusy)
	if b.Card().Status != a2a.StatusBusy {
		t.Errorf("status = %q, want busy", b.Card().Status)
	}
}

func TestBase_Complete(t *testing.T) {
	gw := &fakeGateway{reply: "ok"}
	b := NewBaseAgent(sampleCard(), gw, nil, nil, "/work")
	out, err := b.Complete(context.Background(), "be terse", "write main.py")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "ok" {
		t.Errorf("reply = %q", out)
	}
	if gw.lastSystem != "be terse" || gw.lastUser != "write main.py" {
		t.Errorf("gateway got system=%q user=%q", gw.lastSystem, gw.lastUser)
	}
}

func TestBase_CompleteNoGateway(t *testing.T) {
	b := NewBaseAgent(sampleCard(), nil, nil, nil, "/work")
	if _, err := b.Complete(context.Background(), "s", "u"); err == nil {
		t.Error("Complete with no gateway should error")
	}
}

func TestBase_RunTool(t *testing.T) {
	var calls []string
	b := NewBaseAgent(sampleCard(), &fakeGateway{}, recordingTool(&calls, map[string]any{"ok": true}, nil), nil, "/work")
	res, err := b.RunTool(context.Background(), "write_file", map[string]any{"path": "main.py"})
	if err != nil {
		t.Fatalf("RunTool: %v", err)
	}
	if m, _ := res.(map[string]any); m["ok"] != true {
		t.Errorf("RunTool result = %+v", res)
	}
	if len(calls) != 1 || calls[0] != "write_file:main.py" {
		t.Errorf("tool calls = %v", calls)
	}
}

func TestBase_RunToolNoExecutor(t *testing.T) {
	b := NewBaseAgent(sampleCard(), &fakeGateway{}, nil, nil, "/work")
	if _, err := b.RunTool(context.Background(), "write_file", nil); err == nil {
		t.Error("RunTool with no executor should error")
	}
}

func TestBase_Progress(t *testing.T) {
	b := NewBaseAgent(sampleCard(), &fakeGateway{}, nil, nil, "/work")
	var got a2a.Progress
	b.Progress(func(p a2a.Progress) { got = p }, "task-1", "writing main.py", 3, 5)
	if got.TaskID != "task-1" || got.AgentID != "code-agent" || got.Message != "writing main.py" {
		t.Errorf("progress = %+v", got)
	}
	if got.Step != 3 || got.TotalSteps != 5 {
		t.Errorf("progress steps = %d/%d, want 3/5", got.Step, got.TotalSteps)
	}
	// Nil progressFn must not panic.
	b.Progress(nil, "t", "m", 1, 1)
}
