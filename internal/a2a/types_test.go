package a2a

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewTask(t *testing.T) {
	task := NewTask("coordinator", "code-agent", "write main.py", "sess-1")
	if task.ID == "" || task.FromAgent != "coordinator" || task.ToAgent != "code-agent" {
		t.Fatalf("NewTask = %+v", task)
	}
	if task.Goal != "write main.py" || task.SessionID != "sess-1" {
		t.Errorf("NewTask goal/session wrong: %+v", task)
	}
	if task.Priority != 3 {
		t.Errorf("default priority = %d, want 3", task.Priority)
	}
	if task.CreatedAt.IsZero() {
		t.Error("CreatedAt not set")
	}
}

func TestNewResult(t *testing.T) {
	r := NewResult("task-1", "code-agent", true)
	if r.TaskID != "task-1" || r.AgentID != "code-agent" || !r.Success {
		t.Fatalf("NewResult = %+v", r)
	}
	if r.CompletedAt.IsZero() {
		t.Error("CompletedAt not set")
	}
}

func TestNewProgress(t *testing.T) {
	p := NewProgress("task-1", "code-agent", "writing main.py")
	if p.TaskID != "task-1" || p.AgentID != "code-agent" || p.Message != "writing main.py" {
		t.Fatalf("NewProgress = %+v", p)
	}
	if p.Timestamp.IsZero() {
		t.Error("Timestamp not set")
	}
}

func TestOKResponse(t *testing.T) {
	resp := OKResponse(7, map[string]string{"task_id": "task-1"})
	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", resp.JSONRPC)
	}
	if resp.Error != nil {
		t.Error("OK response should have nil error")
	}
	if resp.ID != 7 {
		t.Errorf("id = %v, want 7", resp.ID)
	}
}

func TestErrResponse(t *testing.T) {
	resp := ErrResponse("abc", ErrMethodNotFound, "no such method")
	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q", resp.JSONRPC)
	}
	if resp.Error == nil {
		t.Fatal("error response must set Error")
	}
	if resp.Error.Code != ErrMethodNotFound || resp.Error.Message != "no such method" {
		t.Errorf("error = %+v", resp.Error)
	}
	if resp.Result != nil {
		t.Error("error response should omit result")
	}
}

func TestErrorCodes(t *testing.T) {
	cases := map[int]int{
		ErrParse:          -32700,
		ErrInvalidRequest: -32600,
		ErrMethodNotFound: -32601,
		ErrInvalidParams:  -32602,
		ErrInternal:       -32603,
		ErrTaskFailed:     -32001,
		ErrAgentBusy:      -32002,
		ErrTimeout:        -32003,
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("error code = %d, want %d", got, want)
		}
	}
}

func TestTask_JSONRoundTrip(t *testing.T) {
	task := Task{
		ID: "task-1", SessionID: "s1", FromAgent: "a", ToAgent: "b",
		Goal: "g", Context: "ctx", Files: []string{"main.py"},
		Constraints: []string{"no deletes"}, Priority: 2,
		Timeout: 30 * time.Second, CreatedAt: time.Unix(1700000000, 0).UTC(),
	}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Task
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != task.ID || got.Goal != task.Goal || len(got.Files) != 1 ||
		got.Priority != 2 || got.Timeout != 30*time.Second {
		t.Errorf("round-trip = %+v, want %+v", got, task)
	}
}

func TestTaskResult_JSONRoundTrip(t *testing.T) {
	r := TaskResult{
		TaskID: "task-1", AgentID: "review", Success: true, Output: "ok",
		Files: []string{"main.py"}, Errors: nil, Score: 9, Approved: true,
		TokensUsed: 123, DurationMs: 4500, CompletedAt: time.Unix(1700000000, 0).UTC(),
	}
	data, _ := json.Marshal(r)
	var got TaskResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Score != 9 || !got.Approved || got.TokensUsed != 123 || got.DurationMs != 4500 {
		t.Errorf("round-trip = %+v", got)
	}
}

func TestAgentCard_JSONRoundTrip(t *testing.T) {
	card := AgentCard{
		ID: "code-agent", Name: "VORTEX Code Agent", Role: "coder",
		Description: "writes code", Version: "1.0.0",
		Capabilities: []string{"write_code", "edit_file"},
		Tools:        []string{"write_file"}, AIModel: "deepseek-chat",
		Endpoint: "/a2a/agents/code-agent", Status: StatusIdle,
	}
	data, _ := json.Marshal(card)
	var got AgentCard
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "code-agent" || got.Role != "coder" || len(got.Capabilities) != 2 || got.Status != StatusIdle {
		t.Errorf("round-trip = %+v", got)
	}
}

func TestMethodNames(t *testing.T) {
	if MethodSubmitTask != "tasks/submit" || MethodGetStatus != "tasks/status" ||
		MethodCancelTask != "tasks/cancel" || MethodGetCard != "agent/card" ||
		MethodListTasks != "tasks/list" {
		t.Error("RPC method name constants drifted")
	}
}
