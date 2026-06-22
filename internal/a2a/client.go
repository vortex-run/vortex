package a2a

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AgentClient calls other agents over the A2A protocol. It is what the
// coordinator uses to drive specialist agents.
type AgentClient struct {
	baseURL    string
	httpClient *http.Client
	agentID    string // caller identity, stamped onto outgoing tasks
}

// NewAgentClient constructs a client rooted at baseURL identifying as callerID.
func NewAgentClient(baseURL, callerID string) *AgentClient {
	return &AgentClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 0}, // SSE needs no overall timeout; per-call ctx governs
		agentID:    callerID,
	}
}

// rpc issues a JSON-RPC call and returns the decoded response.
func (c *AgentClient) rpc(ctx context.Context, agentID, method string, params any) (*JSONRPCResponse, error) {
	body, err := json.Marshal(JSONRPCRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/a2a/agents/"+agentID+"/rpc", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2a: rpc to %s: %w", agentID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("a2a: decoding rpc response: %w", err)
	}
	return &out, nil
}

// Submit sends a task to an agent and returns the assigned task id. The task's
// FromAgent is stamped with the caller identity.
func (c *AgentClient) Submit(ctx context.Context, agentID string, task Task) (string, error) {
	if task.FromAgent == "" {
		task.FromAgent = c.agentID
	}
	task.ToAgent = agentID
	resp, err := c.rpc(ctx, agentID, MethodSubmitTask, map[string]any{"task": task})
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("a2a: submit rejected (%d): %s", resp.Error.Code, resp.Error.Message)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a2a: unexpected submit result")
	}
	id, _ := m["task_id"].(string)
	if id == "" {
		return "", fmt.Errorf("a2a: submit returned no task id")
	}
	return id, nil
}

// WaitForResult opens the agent's SSE stream and reads progress events,
// invoking progressFn for each, until the terminal result for taskID arrives.
// It respects ctx cancellation.
func (c *AgentClient) WaitForResult(ctx context.Context, agentID, taskID string, progressFn func(Progress)) (*TaskResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/a2a/agents/"+agentID+"/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2a: opening event stream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var p Progress
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &p) != nil {
			continue
		}
		// Only act on this task's events (the stream is per-agent, not per-task).
		if p.TaskID != "" && taskID != "" && p.TaskID != taskID {
			continue
		}
		if p.Result != nil {
			return p.Result, nil
		}
		if progressFn != nil {
			progressFn(p)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("a2a: reading event stream: %w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("a2a: event stream ended before result")
}

// Call is the convenience the coordinator uses: Submit then WaitForResult. To
// avoid missing early events, the SSE stream is opened before submitting.
func (c *AgentClient) Call(ctx context.Context, agentID string, task Task, progressFn func(Progress)) (*TaskResult, error) {
	if task.FromAgent == "" {
		task.FromAgent = c.agentID
	}
	task.ToAgent = agentID
	if task.ID == "" {
		task.ID = "task-" + randomID()
	}

	// Open the event stream first so no progress is lost between submit and
	// subscribe. The result is delivered over this stream.
	type waitOut struct {
		res *TaskResult
		err error
	}
	done := make(chan waitOut, 1)
	go func() {
		res, err := c.WaitForResult(ctx, agentID, task.ID, progressFn)
		done <- waitOut{res, err}
	}()

	// Give the subscription a brief head start, then submit.
	time.Sleep(50 * time.Millisecond)
	if _, err := c.Submit(ctx, agentID, task); err != nil {
		return nil, err
	}
	out := <-done
	return out.res, out.err
}

// GetCard fetches an agent's card.
func (c *AgentClient) GetCard(ctx context.Context, agentID string) (*AgentCard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/a2a/agents/"+agentID+"/card", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2a: get card: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("a2a: get card returned %d", resp.StatusCode)
	}
	var card AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return nil, err
	}
	return &card, nil
}

// ListAgents fetches all registered agent cards.
func (c *AgentClient) ListAgents(ctx context.Context) ([]AgentCard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/a2a/agents", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2a: list agents: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var cards []AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		return nil, err
	}
	return cards, nil
}

// CancelTask requests cancellation of a task.
func (c *AgentClient) CancelTask(ctx context.Context, agentID, taskID string) error {
	resp, err := c.rpc(ctx, agentID, MethodCancelTask, map[string]any{"task_id": taskID})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("a2a: cancel rejected (%d): %s", resp.Error.Code, resp.Error.Message)
	}
	return nil
}
