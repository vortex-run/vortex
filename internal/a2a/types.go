// Package a2a implements VORTEX's agent-to-agent communication protocol: a
// JSON-RPC 2.0 surface (the core of Google's A2A protocol v1.0) over net/http,
// with SSE streaming for live progress. Specialist agents register with an
// AgentServer and call one another through an AgentClient — no new
// dependencies, pure standard library.
package a2a

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// AgentCard identifies an agent and its capabilities (served at /a2a/.../card).
type AgentCard struct {
	ID           string   `json:"id"`           // "coordinator"|"code-agent" etc
	Name         string   `json:"name"`         // "VORTEX Coordinator"
	Role         string   `json:"role"`         // coordinator|coder|tester|reviewer|researcher|devops
	Description  string   `json:"description"`  //
	Version      string   `json:"version"`      // "1.0.0"
	Capabilities []string `json:"capabilities"` // what this agent can do
	Tools        []string `json:"tools"`        // tool names available
	AIModel      string   `json:"ai_model"`     // preferred model
	Endpoint     string   `json:"endpoint"`     // base URL for this agent
	Status       string   `json:"status"`       // idle|busy|offline
}

// Task is the unit of work passed between agents.
type Task struct {
	ID          string        `json:"id"`
	SessionID   string        `json:"session_id"`
	FromAgent   string        `json:"from_agent"`
	ToAgent     string        `json:"to_agent"`
	Goal        string        `json:"goal"`
	Context     string        `json:"context"`
	Files       []string      `json:"files"`
	Constraints []string      `json:"constraints"`
	Priority    int           `json:"priority"` // 1=urgent .. 5=background
	Timeout     time.Duration `json:"timeout"`
	CreatedAt   time.Time     `json:"created_at"`
}

// TaskResult is what an agent returns when a task is done.
type TaskResult struct {
	TaskID      string    `json:"task_id"`
	AgentID     string    `json:"agent_id"`
	Success     bool      `json:"success"`
	Output      string    `json:"output"`
	Files       []string  `json:"files"`
	Errors      []string  `json:"errors"`
	Score       int       `json:"score"`    // 0-10 (review agent)
	Approved    bool      `json:"approved"` // review agent
	TokensUsed  int       `json:"tokens_used"`
	DurationMs  int64     `json:"duration_ms"`
	CompletedAt time.Time `json:"completed_at"`
}

// Progress is a streaming update emitted during task execution (over SSE).
type Progress struct {
	TaskID     string    `json:"task_id"`
	AgentID    string    `json:"agent_id"`
	Message    string    `json:"message"`
	FileOp     string    `json:"file_op"`
	Step       int       `json:"step"`
	TotalSteps int       `json:"total_steps"`
	Timestamp  time.Time `json:"timestamp"`
	// Result, when non-nil, marks the terminal progress event carrying the
	// task's TaskResult so SSE clients can return without a second round-trip.
	Result *TaskResult `json:"result,omitempty"`
}

// JSONRPCRequest is a standard JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"` // always "2.0"
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      any    `json:"id"`
}

// JSONRPCResponse is a standard JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
	ID      any           `json:"id"`
}

// JSONRPCError is a standard JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC standard + A2A-specific error codes.
const (
	ErrParse          = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
	ErrTaskFailed     = -32001
	ErrAgentBusy      = -32002
	ErrTimeout        = -32003
)

// A2A RPC method names.
const (
	MethodSubmitTask = "tasks/submit"
	MethodGetStatus  = "tasks/status"
	MethodCancelTask = "tasks/cancel"
	MethodGetCard    = "agent/card"
	MethodListTasks  = "tasks/list"
)

// Agent statuses.
const (
	StatusIdle    = "idle"
	StatusBusy    = "busy"
	StatusOffline = "offline"
)

// randomID returns a 16-hex-char identifier.
func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	return hex.EncodeToString(b[:])
}

// NewTask constructs a Task with a generated ID and the current time.
func NewTask(from, to, goal, sessionID string) *Task {
	return &Task{
		ID:        "task-" + randomID(),
		SessionID: sessionID,
		FromAgent: from,
		ToAgent:   to,
		Goal:      goal,
		Priority:  3,
		CreatedAt: time.Now(),
	}
}

// NewResult constructs a TaskResult stamped with the completion time.
func NewResult(taskID, agentID string, success bool) *TaskResult {
	return &TaskResult{
		TaskID:      taskID,
		AgentID:     agentID,
		Success:     success,
		CompletedAt: time.Now(),
	}
}

// NewProgress constructs a Progress update stamped with the current time.
func NewProgress(taskID, agentID, message string) *Progress {
	return &Progress{
		TaskID:    taskID,
		AgentID:   agentID,
		Message:   message,
		Timestamp: time.Now(),
	}
}

// OKResponse builds a successful JSON-RPC response carrying result.
func OKResponse(id any, result any) JSONRPCResponse {
	return JSONRPCResponse{JSONRPC: "2.0", Result: result, ID: id}
}

// ErrResponse builds a JSON-RPC error response with the given code and message.
func ErrResponse(id any, code int, msg string) JSONRPCResponse {
	return JSONRPCResponse{JSONRPC: "2.0", Error: &JSONRPCError{Code: code, Message: msg}, ID: id}
}
