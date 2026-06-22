package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Agent is implemented by every registered agent. HandleTask runs the work,
// streaming intermediate updates via progressFn, and returns the result.
type Agent interface {
	Card() AgentCard
	HandleTask(ctx context.Context, task Task, progressFn func(Progress)) TaskResult
}

// AgentServer hosts the A2A surface for a set of agents on one HTTP mux. All
// agents share the path space /a2a/agents/<id>/{rpc,events,card}; /a2a/agents
// lists them. Safe for concurrent use.
type AgentServer struct {
	mu       sync.RWMutex
	agents   map[string]Agent
	statuses map[string]string                // live status override (busy while running)
	subs     map[string]map[int]chan Progress // agentID → subscriber id → channel
	results  map[string]TaskResult            // taskID → last result (for tasks/status)
	tasks    map[string]string                // taskID → agentID
	nextSub  int
	mux      *http.ServeMux
}

// NewAgentServer constructs an empty server with its routes installed.
func NewAgentServer() *AgentServer {
	s := &AgentServer{
		agents:   map[string]Agent{},
		statuses: map[string]string{},
		subs:     map[string]map[int]chan Progress{},
		results:  map[string]TaskResult{},
		tasks:    map[string]string{},
		mux:      http.NewServeMux(),
	}
	// Per-agent routes use a single catch-all so registration doesn't need to
	// re-register patterns; the handler parses the agent id + action from the
	// path. The list route is exact.
	s.mux.HandleFunc("/a2a/agents/", s.route)
	s.mux.HandleFunc("/a2a/agents", s.handleList)
	return s
}

// Register adds an agent, making its rpc/events/card routes live.
func (s *AgentServer) Register(agent Agent) {
	id := agent.Card().ID
	s.mu.Lock()
	s.agents[id] = agent
	s.subs[id] = map[int]chan Progress{}
	s.mu.Unlock()
}

// Handler returns the mux for mounting on the main server.
func (s *AgentServer) Handler() http.Handler { return s.mux }

// List returns every registered agent's card (with live status applied).
func (s *AgentServer) List() []AgentCard {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AgentCard, 0, len(s.agents))
	for id, a := range s.agents {
		card := a.Card()
		if st, ok := s.statuses[id]; ok && st != "" {
			card.Status = st
		}
		out = append(out, card)
	}
	return out
}

// route dispatches /a2a/agents/<id>/<action> to the right handler.
func (s *AgentServer) route(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/a2a/agents/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	_, ok := s.agents[id]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown agent: "+id, http.StatusNotFound)
		return
	}
	switch action {
	case "rpc":
		s.handleRPC(id, w, r)
	case "events":
		s.handleSSE(id, w, r)
	case "card":
		s.handleCard(id, w)
	default:
		http.NotFound(w, r)
	}
}

// handleList serves GET /a2a/agents.
func (s *AgentServer) handleList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.List())
}

// handleCard serves GET /a2a/agents/<id>/card.
func (s *AgentServer) handleCard(id string, w http.ResponseWriter) {
	s.mu.RLock()
	a := s.agents[id]
	st := s.statuses[id]
	s.mu.RUnlock()
	card := a.Card()
	if st != "" {
		card.Status = st
	}
	writeJSON(w, http.StatusOK, card)
}

// handleRPC parses a JSON-RPC request and routes it by method.
func (s *AgentServer) handleRPC(agentID string, w http.ResponseWriter, r *http.Request) {
	var req JSONRPCRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, ErrResponse(nil, ErrParse, "parse error: "+err.Error()))
		return
	}
	if req.JSONRPC != "2.0" {
		writeJSON(w, http.StatusOK, ErrResponse(req.ID, ErrInvalidRequest, "jsonrpc must be 2.0"))
		return
	}
	switch req.Method {
	case MethodSubmitTask:
		s.submitTask(agentID, req, w)
	case MethodGetStatus:
		s.getStatus(req, w)
	case MethodCancelTask:
		s.cancelTask(req, w)
	case MethodGetCard:
		s.mu.RLock()
		card := s.agents[agentID].Card()
		s.mu.RUnlock()
		writeJSON(w, http.StatusOK, OKResponse(req.ID, card))
	default:
		writeJSON(w, http.StatusOK, ErrResponse(req.ID, ErrMethodNotFound, "unknown method: "+req.Method))
	}
}

// submitTask validates and starts a task asynchronously, returning its id at
// once. Progress + the terminal result stream over the agent's SSE channel.
func (s *AgentServer) submitTask(agentID string, req JSONRPCRequest, w http.ResponseWriter) {
	task, err := taskFromParams(req.Params)
	if err != nil {
		writeJSON(w, http.StatusOK, ErrResponse(req.ID, ErrInvalidParams, err.Error()))
		return
	}
	s.mu.Lock()
	agent := s.agents[agentID]
	if s.statuses[agentID] == StatusBusy {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, ErrResponse(req.ID, ErrAgentBusy, "agent is busy"))
		return
	}
	if task.ID == "" {
		task.ID = "task-" + randomID()
	}
	s.statuses[agentID] = StatusBusy
	s.tasks[task.ID] = agentID
	s.mu.Unlock()

	go s.runTask(agentID, agent, *task)

	writeJSON(w, http.StatusOK, OKResponse(req.ID, map[string]string{"task_id": task.ID}))
}

// runTask executes the agent and broadcasts progress + a terminal result event.
func (s *AgentServer) runTask(agentID string, agent Agent, task Task) {
	defer func() {
		if rec := recover(); rec != nil {
			res := NewResult(task.ID, agentID, false)
			res.Errors = []string{fmt.Sprintf("agent panic: %v", rec)}
			s.finish(agentID, *res)
		}
	}()
	ctx := context.Background()
	if task.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, task.Timeout)
		defer cancel()
	}
	progressFn := func(p Progress) {
		p.AgentID = agentID
		p.TaskID = task.ID
		if p.Timestamp.IsZero() {
			p.Timestamp = time.Now()
		}
		s.broadcast(agentID, p)
	}
	result := agent.HandleTask(ctx, task, progressFn)
	result.TaskID = task.ID
	result.AgentID = agentID
	s.finish(agentID, result)
}

// finish records the result, clears busy, and broadcasts a terminal event.
func (s *AgentServer) finish(agentID string, result TaskResult) {
	s.mu.Lock()
	s.statuses[agentID] = StatusIdle
	s.results[result.TaskID] = result
	s.mu.Unlock()
	final := Progress{
		TaskID: result.TaskID, AgentID: agentID, Message: "task complete",
		Timestamp: time.Now(), Result: &result,
	}
	s.broadcast(agentID, final)
}

// getStatus serves tasks/status: returns the stored result for a task id.
func (s *AgentServer) getStatus(req JSONRPCRequest, w http.ResponseWriter) {
	taskID := paramString(req.Params, "task_id")
	s.mu.RLock()
	res, ok := s.results[taskID]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusOK, OKResponse(req.ID, map[string]string{"status": "running", "task_id": taskID}))
		return
	}
	writeJSON(w, http.StatusOK, OKResponse(req.ID, res))
}

// cancelTask serves tasks/cancel (best effort: marks the agent idle; running
// goroutines observe ctx only when a timeout was set).
func (s *AgentServer) cancelTask(req JSONRPCRequest, w http.ResponseWriter) {
	taskID := paramString(req.Params, "task_id")
	s.mu.Lock()
	if agentID, ok := s.tasks[taskID]; ok {
		s.statuses[agentID] = StatusIdle
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, OKResponse(req.ID, map[string]string{"status": "cancelled", "task_id": taskID}))
}

// handleSSE streams Progress events for an agent until the client disconnects.
func (s *AgentServer) handleSSE(agentID string, w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch, subID := s.subscribe(agentID)
	defer s.unsubscribe(agentID, subID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case p, open := <-ch:
			if !open {
				return
			}
			b, _ := json.Marshal(p)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// subscribe registers an SSE channel for an agent, returning it + its id.
func (s *AgentServer) subscribe(agentID string) (chan Progress, int) {
	ch := make(chan Progress, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs[agentID] == nil {
		s.subs[agentID] = map[int]chan Progress{}
	}
	id := s.nextSub
	s.nextSub++
	s.subs[agentID][id] = ch
	return ch, id
}

// unsubscribe removes and closes an SSE channel.
func (s *AgentServer) unsubscribe(agentID string, id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.subs[agentID]; m != nil {
		if ch, ok := m[id]; ok {
			delete(m, id)
			close(ch)
		}
	}
}

// broadcast delivers a progress event to all of an agent's subscribers,
// dropping events for slow consumers rather than blocking the agent.
func (s *AgentServer) broadcast(agentID string, p Progress) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.subs[agentID] {
		select {
		case ch <- p:
		default: // subscriber buffer full; drop rather than stall the agent
		}
	}
}

// --- param parsing ----------------------------------------------------------

// taskFromParams extracts a Task from JSON-RPC params (re-marshalled because
// Params decodes as a generic map).
func taskFromParams(params any) (*Task, error) {
	wrapped := struct {
		Task *Task `json:"task"`
	}{}
	raw, _ := json.Marshal(params)
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Task != nil {
		return wrapped.Task, nil
	}
	// Also accept a bare task object as params.
	var bare Task
	if err := json.Unmarshal(raw, &bare); err != nil {
		return nil, fmt.Errorf("invalid task params: %w", err)
	}
	if bare.Goal == "" {
		return nil, fmt.Errorf("task goal is required")
	}
	return &bare, nil
}

// paramString reads a string field from generic params.
func paramString(params any, key string) string {
	if m, ok := params.(map[string]any); ok {
		if v, ok := m[key].(string); ok {
			return v
		}
	}
	raw, _ := json.Marshal(params)
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if v, ok := m[key].(string); ok {
			return v
		}
	}
	return ""
}

// writeJSON encodes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
