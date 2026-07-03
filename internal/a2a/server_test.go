package a2a

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubAgent is a fake Agent that emits a couple of progress events and returns
// a fixed result. A barrier channel lets tests hold it "busy".
type stubAgent struct {
	card    AgentCard
	hold    chan struct{} // if non-nil, HandleTask blocks until it receives
	gotTask Task
	mu      sync.Mutex
}

func (a *stubAgent) Card() AgentCard { return a.card }

func (a *stubAgent) HandleTask(_ context.Context, task Task, progressFn func(Progress)) TaskResult {
	a.mu.Lock()
	a.gotTask = task
	a.mu.Unlock()
	if a.hold != nil {
		<-a.hold
	}
	progressFn(Progress{Message: "step one", Step: 1, TotalSteps: 2})
	progressFn(Progress{Message: "step two", Step: 2, TotalSteps: 2})
	res := NewResult(task.ID, a.card.ID, true)
	res.Output = "done: " + task.Goal
	res.Files = []string{"main.py"}
	return *res
}

func newStub(id, role string) *stubAgent {
	return &stubAgent{card: AgentCard{ID: id, Name: id, Role: role, Status: StatusIdle,
		Capabilities: []string{"do_thing"}}}
}

// rpcPost issues a JSON-RPC request to an agent and decodes the response.
func rpcPost(t *testing.T, srv *httptest.Server, agentID, method string, params any) JSONRPCResponse {
	t.Helper()
	body, _ := json.Marshal(JSONRPCRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	resp, err := http.Post(srv.URL+"/a2a/agents/"+agentID+"/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST rpc: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestServer_RegisterAndList(t *testing.T) {
	s := NewAgentServer()
	s.Register(newStub("code-agent", "coder"))
	s.Register(newStub("test-agent", "tester"))
	if got := len(s.List()); got != 2 {
		t.Fatalf("List = %d agents, want 2", got)
	}

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/a2a/agents")
	defer func() { _ = resp.Body.Close() }()
	var cards []AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(cards) != 2 {
		t.Errorf("GET /a2a/agents = %d, want 2", len(cards))
	}
}

func TestServer_SubmitReturnsTaskID(t *testing.T) {
	s := NewAgentServer()
	s.Register(newStub("code-agent", "coder"))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp := rpcPost(t, srv, "code-agent", MethodSubmitTask,
		map[string]any{"task": NewTask("coordinator", "code-agent", "write code", "s1")})
	if resp.Error != nil {
		t.Fatalf("submit error: %+v", resp.Error)
	}
	m, _ := resp.Result.(map[string]any)
	if m["task_id"] == "" || m["task_id"] == nil {
		t.Errorf("submit result missing task_id: %+v", resp.Result)
	}
}

func TestServer_InvalidJSONParseError(t *testing.T) {
	s := NewAgentServer()
	s.Register(newStub("code-agent", "coder"))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/a2a/agents/code-agent/rpc", "application/json",
		strings.NewReader("{not json"))
	defer func() { _ = resp.Body.Close() }()
	var out JSONRPCResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Error == nil || out.Error.Code != ErrParse {
		t.Errorf("expected parse error, got %+v", out)
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	s := NewAgentServer()
	s.Register(newStub("code-agent", "coder"))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp := rpcPost(t, srv, "code-agent", "tasks/teleport", nil)
	if resp.Error == nil || resp.Error.Code != ErrMethodNotFound {
		t.Errorf("expected method-not-found, got %+v", resp.Error)
	}
}

func TestServer_CardEndpoint(t *testing.T) {
	s := NewAgentServer()
	s.Register(newStub("code-agent", "coder"))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/a2a/agents/code-agent/card")
	defer func() { _ = resp.Body.Close() }()
	var card AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.ID != "code-agent" || card.Role != "coder" {
		t.Errorf("card = %+v", card)
	}
}

func TestServer_UnknownAgent404(t *testing.T) {
	s := NewAgentServer()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/a2a/agents/ghost/card")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown agent card = %d, want 404", resp.StatusCode)
	}
}

func TestServer_BusyAgentRejected(t *testing.T) {
	s := NewAgentServer()
	agent := newStub("code-agent", "coder")
	agent.hold = make(chan struct{}) // blocks the first task so the agent stays busy
	s.Register(agent)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// First submit succeeds and leaves the agent busy.
	first := rpcPost(t, srv, "code-agent", MethodSubmitTask,
		map[string]any{"task": NewTask("c", "code-agent", "g1", "s1")})
	if first.Error != nil {
		t.Fatalf("first submit: %+v", first.Error)
	}
	// Give the goroutine a moment to mark busy.
	waitUntil(t, func() bool { return s.statusOf("code-agent") == StatusBusy })

	second := rpcPost(t, srv, "code-agent", MethodSubmitTask,
		map[string]any{"task": NewTask("c", "code-agent", "g2", "s1")})
	if second.Error == nil || second.Error.Code != ErrAgentBusy {
		t.Errorf("busy agent should reject, got %+v", second.Error)
	}
	close(agent.hold) // release the first task
}

func TestServer_SSEReceivesProgress(t *testing.T) {
	s := NewAgentServer()
	s.Register(newStub("code-agent", "coder"))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// Open the SSE stream first so we don't miss events.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/a2a/agents/code-agent/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Submit a task.
	rpcPost(t, srv, "code-agent", MethodSubmitTask,
		map[string]any{"task": NewTask("c", "code-agent", "write code", "s1")})

	// Read progress events; expect the two steps and a terminal result.
	scanner := bufio.NewScanner(resp.Body)
	var sawStep, sawResult bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var p Progress
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &p) != nil {
			continue
		}
		if p.Message == "step one" {
			sawStep = true
		}
		if p.Result != nil && p.Result.Success {
			sawResult = true
			break
		}
	}
	if !sawStep {
		t.Error("did not receive progress step over SSE")
	}
	if !sawResult {
		t.Error("did not receive terminal result over SSE")
	}
}

func TestServer_ConcurrentSubmitsNoRace(t *testing.T) {
	s := NewAgentServer()
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		s.Register(newStub(id, "coder"))
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := []string{"a1", "a2", "a3", "a4"}[n%4]
			rpcPost(t, srv, id, MethodSubmitTask,
				map[string]any{"task": NewTask("c", id, "g", "s")})
		}(i)
	}
	wg.Wait()
}

// statusOf reads an agent's live status under lock (test helper).
func (s *AgentServer) statusOf(id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statuses[id]
}

// waitUntil polls cond up to 2s.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestAgentServer_TrackedTasksBounded verifies results/tasks stay bounded by
// maxTrackedTasks under a churn of task IDs, and that FIFO eviction drops the
// oldest entries (production audit follow-up: unbounded a2a task maps).
func TestAgentServer_TrackedTasksBounded(t *testing.T) {
	s := NewAgentServer()
	total := maxTrackedTasks + 500
	for i := 0; i < total; i++ {
		id := "task-" + strings.Repeat("x", 0) + itoa(i)
		s.mu.Lock()
		s.trackTaskLocked(id, "agent")
		s.results[id] = TaskResult{TaskID: id}
		s.mu.Unlock()
	}
	s.mu.Lock()
	nTasks, nResults, nOrder := len(s.tasks), len(s.results), len(s.taskOrder)
	_, oldestPresent := s.tasks["task-"+itoa(0)]
	_, newestPresent := s.tasks["task-"+itoa(total-1)]
	s.mu.Unlock()

	if nTasks > maxTrackedTasks || nResults > maxTrackedTasks || nOrder > maxTrackedTasks {
		t.Fatalf("maps not bounded: tasks=%d results=%d order=%d cap=%d", nTasks, nResults, nOrder, maxTrackedTasks)
	}
	if oldestPresent {
		t.Error("oldest task should have been evicted")
	}
	if !newestPresent {
		t.Error("newest task should be retained")
	}
}

// itoa is a tiny int→string helper so the test avoids importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
