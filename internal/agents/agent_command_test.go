package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sandeepbollavaram/vortex/internal/llm"
)

// toolGateway is an AIGateway that also supports tool calling.
type toolGateway struct{ *scriptedModel }

func (toolGateway) Complete(context.Context, string, string) (string, error) { return "text", nil }

func agentCoord(t *testing.T, gw AIGateway, approval ApprovalFunc) (*Coordinator, string) {
	t.Helper()
	dir := t.TempDir()
	reg := NewToolRegistry()
	if err := RegisterLocalTools(reg, LocalFSConfig{Root: dir}); err != nil {
		t.Fatal(err)
	}
	c, err := NewCoordinator(CoordinatorConfig{Bus: NewBus(), AIGateway: gw, LocalTools: reg, WorkingDir: dir, Approval: approval})
	if err != nil {
		t.Fatal(err)
	}
	return c, dir
}

func writeScript() *scriptedModel {
	return &scriptedModel{replies: []llm.ToolChatResult{
		call("c1", "write_file", map[string]any{"path": "hello.txt", "content": "hi"}),
		{Text: "Created hello.txt."},
	}}
}

func TestAgentCommand_SyncApprover(t *testing.T) {
	c, dir := agentCoord(t, toolGateway{writeScript()}, func(context.Context, ApprovalRequest) bool { return true })
	reply, err := c.HandleMessage(context.Background(), "/agent create hello.txt", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "Created hello.txt.") {
		t.Errorf("reply = %q", reply)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "hello.txt")); string(data) != "hi" {
		t.Errorf("file = %q", data)
	}
}

// TestAgentCommand_AsyncApproval: without a synchronous approver the loop
// waits on a pending action that the TUI/API approve call resolves.
func TestAgentCommand_AsyncApproval(t *testing.T) {
	c, dir := agentCoord(t, toolGateway{writeScript()}, nil)
	done := make(chan string, 1)
	go func() {
		reply, _ := c.HandleMessage(context.Background(), "/agent create hello.txt", "s2")
		done <- reply
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !c.HasPendingApproval("s2") {
		if time.Now().After(deadline) {
			t.Fatal("loop never asked for approval")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "hello.txt")); err == nil {
		t.Fatal("file written before approval")
	}
	if _, matched := c.ApproveAction("s2", true); !matched {
		t.Fatal("ApproveAction didn't match the waiting loop")
	}
	select {
	case reply := <-done:
		if !strings.Contains(reply, "Created hello.txt.") || !strings.Contains(reply, "APPROVAL_REQUIRED") {
			t.Errorf("reply = %q", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loop didn't finish after approval")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "hello.txt")); string(data) != "hi" {
		t.Errorf("file = %q", data)
	}
}

func TestAgentCommand_AsyncReject(t *testing.T) {
	c, dir := agentCoord(t, toolGateway{writeScript()}, nil)
	done := make(chan string, 1)
	go func() {
		reply, _ := c.HandleMessage(context.Background(), "/agent create hello.txt", "s3")
		done <- reply
	}()
	for i := 0; !c.HasPendingApproval("s3"); i++ {
		if i > 500 {
			t.Fatal("no approval request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.ApproveAction("s3", false)
	<-done
	if _, err := os.Stat(filepath.Join(dir, "hello.txt")); err == nil {
		t.Error("rejected write happened")
	}
}

// TestAgentCommand_GatewayWithoutTools: a gateway with no native tool
// calling falls back to the text protocol instead of refusing.
func TestAgentCommand_GatewayWithoutTools(t *testing.T) {
	c, _ := agentCoord(t, StubAIGateway{AnswerReply: "All done."}, nil)
	reply, err := c.HandleMessage(context.Background(), "/agent do something", "s4")
	if err != nil || !strings.Contains(reply, "text protocol") || !strings.Contains(reply, "All done.") {
		t.Errorf("reply = %q, err = %v", reply, err)
	}
}

// TestAgentCommand_StreamedReplyIncludesAnswer: with a streaming caller the
// streamed lines are the reply, so the final answer must be streamed too.
func TestAgentCommand_StreamedReplyIncludesAnswer(t *testing.T) {
	c, _ := agentCoord(t, toolGateway{writeScript()}, func(context.Context, ApprovalRequest) bool { return true })
	var streamed strings.Builder
	if _, err := c.HandleMessageStream(context.Background(), "/agent create hello.txt", "s5", func(d string) { streamed.WriteString(d) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(streamed.String(), "Created hello.txt.") {
		t.Errorf("streamed reply lacks the answer: %q", streamed.String())
	}
}

// TestAgentCommand_SkillCatalogInPrompt: workspace skills are listed (name
// and description only) in the first user turn, fenced as repository data;
// never in the system prompt, where repo-written text could pose as
// instructions. Invalid skills and skill bodies are not included.
func TestAgentCommand_SkillCatalogInPrompt(t *testing.T) {
	model := &scriptedModel{}
	c, dir := agentCoord(t, toolGateway{model}, nil)
	for name, doc := range map[string]string{
		"release-notes": "---\nname: release-notes\ndescription: Draft release notes from git log\n---\nSECRET BODY\n",
		"bad":           "---\nname: not-bad\ndescription: x\n---\n",
	} {
		d := filepath.Join(dir, ".agents", "skills", name)
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.HandleMessage(context.Background(), "/agent summarise", "s6"); err != nil {
		t.Fatal(err)
	}
	first := model.seen[0][0]
	if first.Role != "user" || !strings.Contains(first.Content, "<workspace_skills>") ||
		!strings.Contains(first.Content, "- release-notes: Draft release notes from git log (.agents/skills/release-notes/SKILL.md)") ||
		!strings.HasSuffix(first.Content, "summarise") {
		t.Errorf("first turn lacks the fenced catalog before the task:\n%s", first.Content)
	}
	if strings.Contains(model.system, "release-notes") {
		t.Errorf("skill text reached the system prompt:\n%s", model.system)
	}
	if strings.Contains(first.Content, "SECRET BODY") || strings.Contains(first.Content, "not-bad") {
		t.Errorf("catalog leaked a body or listed an invalid skill:\n%s", first.Content)
	}
}
