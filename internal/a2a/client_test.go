package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// liveClient starts a real AgentServer with the given stub agents and returns
// a client pointed at it.
func liveClient(t *testing.T, agents ...*stubAgent) (*AgentClient, *httptest.Server) {
	t.Helper()
	s := NewAgentServer()
	for _, a := range agents {
		s.Register(a)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return NewAgentClient(srv.URL, "coordinator"), srv
}

func TestClient_Submit(t *testing.T) {
	agent := newStub("code-agent", "coder")
	c, _ := liveClient(t, agent)

	id, err := c.Submit(context.Background(), "code-agent",
		Task{Goal: "write main.py", SessionID: "s1"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !strings.HasPrefix(id, "task-") {
		t.Errorf("task id = %q", id)
	}
	// The caller identity + recipient were stamped onto the task.
	waitUntil(t, func() bool {
		agent.mu.Lock()
		defer agent.mu.Unlock()
		return agent.gotTask.FromAgent == "coordinator" && agent.gotTask.ToAgent == "code-agent"
	})
}

func TestClient_GetCard(t *testing.T) {
	c, _ := liveClient(t, newStub("code-agent", "coder"))
	card, err := c.GetCard(context.Background(), "code-agent")
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if card.ID != "code-agent" || card.Role != "coder" {
		t.Errorf("card = %+v", card)
	}
}

func TestClient_ListAgents(t *testing.T) {
	c, _ := liveClient(t, newStub("code-agent", "coder"), newStub("test-agent", "tester"))
	cards, err := c.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(cards) != 2 {
		t.Errorf("ListAgents = %d, want 2", len(cards))
	}
}

func TestClient_Call(t *testing.T) {
	c, _ := liveClient(t, newStub("code-agent", "coder"))
	var steps []string
	res, err := c.Call(context.Background(), "code-agent",
		Task{Goal: "write code", SessionID: "s1"},
		func(p Progress) { steps = append(steps, p.Message) })
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res == nil || !res.Success {
		t.Fatalf("Call result = %+v", res)
	}
	if res.Output != "done: write code" {
		t.Errorf("output = %q", res.Output)
	}
	if len(steps) == 0 {
		t.Error("Call should have streamed progress steps")
	}
}

func TestClient_WaitForResult(t *testing.T) {
	agent := newStub("code-agent", "coder")
	c, _ := liveClient(t, agent)

	// Open the wait before submitting so it catches the events.
	type out struct {
		res *TaskResult
		err error
	}
	done := make(chan out, 1)
	taskID := "task-fixed-1"
	go func() {
		res, err := c.WaitForResult(context.Background(), "code-agent", taskID, nil)
		done <- out{res, err}
	}()
	time.Sleep(50 * time.Millisecond)

	_, err := c.Submit(context.Background(), "code-agent", Task{ID: taskID, Goal: "g", SessionID: "s1"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case o := <-done:
		if o.err != nil || o.res == nil || !o.res.Success {
			t.Fatalf("WaitForResult = %+v, %v", o.res, o.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForResult timed out")
	}
}

func TestClient_CancelTask(t *testing.T) {
	c, _ := liveClient(t, newStub("code-agent", "coder"))
	if err := c.CancelTask(context.Background(), "code-agent", "task-xyz"); err != nil {
		t.Errorf("CancelTask: %v", err)
	}
}

func TestClient_ContextCancelStopsWait(t *testing.T) {
	// An agent that holds forever so no result arrives.
	agent := newStub("code-agent", "coder")
	agent.hold = make(chan struct{})
	c, _ := liveClient(t, agent)
	defer close(agent.hold)

	ctx, cancel := context.WithCancel(context.Background())
	type out struct{ err error }
	done := make(chan out, 1)
	go func() {
		_, err := c.WaitForResult(ctx, "code-agent", "task-1", nil)
		done <- out{err}
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case o := <-done:
		if o.err == nil {
			t.Error("cancelled WaitForResult should return an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitForResult did not stop on context cancel")
	}
}

func TestClient_SubmitNetworkError(t *testing.T) {
	c := NewAgentClient("http://127.0.0.1:1", "coordinator") // nothing listening
	if _, err := c.Submit(context.Background(), "code-agent", Task{Goal: "g"}); err == nil {
		t.Error("Submit to a dead endpoint should error")
	}
}

func TestClient_SubmitRejectedError(t *testing.T) {
	// A fake server that returns a JSON-RPC error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, ErrResponse(1, ErrAgentBusy, "busy"))
	}))
	defer srv.Close()
	c := NewAgentClient(srv.URL, "coordinator")
	if _, err := c.Submit(context.Background(), "code-agent", Task{Goal: "g"}); err == nil ||
		!strings.Contains(err.Error(), "busy") {
		t.Errorf("expected busy rejection, got %v", err)
	}
}
