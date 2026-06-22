package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
)

// --- fakes -----------------------------------------------------------------

type fakeComms struct {
	history []CommsRecord
	ch      chan CommsRecord
}

func (f *fakeComms) History(limit int) []CommsRecord {
	if limit < len(f.history) {
		return f.history[len(f.history)-limit:]
	}
	return f.history
}

func (f *fakeComms) Subscribe() (<-chan CommsRecord, func()) {
	if f.ch == nil {
		f.ch = make(chan CommsRecord, 8)
	}
	return f.ch, func() {}
}

type fakeChatProvider struct {
	mu      sync.Mutex
	lastID  string
	lastMsg string
	reply   string
	err     error
}

func (c *fakeChatProvider) Chat(_ context.Context, agentID, _, message string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastID, c.lastMsg = agentID, message
	return c.reply, c.err
}

type fakeCheckpointProvider struct {
	list     []CheckpointRecord
	approved []string
	rejected []string
	err      error
}

func (c *fakeCheckpointProvider) List() []CheckpointRecord { return c.list }
func (c *fakeCheckpointProvider) Approve(id string) error {
	if c.err != nil {
		return c.err
	}
	c.approved = append(c.approved, id)
	return nil
}
func (c *fakeCheckpointProvider) Reject(id, _ string) error {
	if c.err != nil {
		return c.err
	}
	c.rejected = append(c.rejected, id)
	return nil
}

// newCollabServer builds an authed server for collaboration-endpoint tests.
func newCollabServer(t *testing.T) (*Server, string) {
	t.Helper()
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	_, secret, err := keys.Issue("collab-user", "default", []auth.Role{auth.RoleOperator}, "collab token", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)
	return s, secret
}

func authedReq(method, target, secret, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r.Header.Set("X-API-Key", secret)
	r.RemoteAddr = "127.0.0.1:5555"
	return r
}

// --- auth ------------------------------------------------------------------

func TestComms_RequiresAPIKey(t *testing.T) {
	s, _ := newCollabServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/agents/comms", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	if rec := serve(s, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("comms without key = %d, want 401", rec.Code)
	}
}

func TestDirectChat_RequiresAPIKey(t *testing.T) {
	s, _ := newCollabServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/code-agent/chat",
		strings.NewReader(`{"message":"hi"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	if rec := serve(s, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("chat without key = %d, want 401", rec.Code)
	}
}

// --- comms -----------------------------------------------------------------

func TestComms_ReturnsHistory(t *testing.T) {
	s, secret := newCollabServer(t)
	s.SetCommsProvider(&fakeComms{history: []CommsRecord{
		{From: "coordinator", To: "code-agent", Type: "task", Content: "go"},
		{From: "code-agent", To: "coordinator", Type: "result", Content: "done"},
	}})
	rec := serve(s, authedReq(http.MethodGet, "/api/agents/comms", secret, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("comms = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp struct {
		Messages []CommsRecord `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Messages) != 2 || resp.Messages[0].Content != "go" {
		t.Errorf("messages = %+v", resp.Messages)
	}
}

func TestComms_LimitParam(t *testing.T) {
	s, secret := newCollabServer(t)
	s.SetCommsProvider(&fakeComms{history: []CommsRecord{{Content: "a"}, {Content: "b"}, {Content: "c"}}})
	rec := serve(s, authedReq(http.MethodGet, "/api/agents/comms?limit=1", secret, ""))
	var resp struct {
		Messages []CommsRecord `json:"messages"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Messages) != 1 || resp.Messages[0].Content != "c" {
		t.Errorf("limit=1 returned %+v", resp.Messages)
	}
}

func TestComms_EmptyWhenUnconfigured(t *testing.T) {
	s, secret := newCollabServer(t)
	rec := serve(s, authedReq(http.MethodGet, "/api/agents/comms", secret, ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"messages":[]`) {
		t.Errorf("unconfigured comms = %d %s", rec.Code, rec.Body)
	}
}

func TestCommsStream_SSE(t *testing.T) {
	s, secret := newCollabServer(t)
	fc := &fakeComms{
		history: []CommsRecord{{From: "coordinator", Content: "history-line"}},
		ch:      make(chan CommsRecord, 4),
	}
	s.SetCommsProvider(fc)

	srv := httptest.NewServer(s.srv.Handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/agents/comms/stream", nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// Publish a live message, then read until we see both history + live.
	fc.ch <- CommsRecord{From: "code-agent", Content: "live-line"}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, "history-line") {
		t.Errorf("stream missing replayed history:\n%s", got)
	}
}

// --- direct chat -----------------------------------------------------------

func TestDirectChat_RoutesToAgent(t *testing.T) {
	s, secret := newCollabServer(t)
	cp := &fakeChatProvider{reply: "SQLite is zero-config."}
	s.SetChatProvider(cp)
	rec := serve(s, authedReq(http.MethodPost, "/api/agents/code-agent/chat", secret,
		`{"session_id":"sess","message":"why sqlite?"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("chat = %d (%s)", rec.Code, rec.Body)
	}
	if cp.lastID != "code-agent" || cp.lastMsg != "why sqlite?" {
		t.Errorf("routed id=%q msg=%q", cp.lastID, cp.lastMsg)
	}
	var resp struct {
		AgentID  string `json:"agent_id"`
		Response string `json:"response"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.AgentID != "code-agent" || resp.Response != "SQLite is zero-config." {
		t.Errorf("resp = %+v", resp)
	}
}

func TestDirectChat_EmptyMessageRejected(t *testing.T) {
	s, secret := newCollabServer(t)
	s.SetChatProvider(&fakeChatProvider{})
	rec := serve(s, authedReq(http.MethodPost, "/api/agents/code-agent/chat", secret, `{"message":"  "}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty message = %d, want 400", rec.Code)
	}
}

func TestDirectChat_Unconfigured503(t *testing.T) {
	s, secret := newCollabServer(t)
	rec := serve(s, authedReq(http.MethodPost, "/api/agents/code-agent/chat", secret, `{"message":"hi"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured chat = %d, want 503", rec.Code)
	}
}

// --- checkpoints -----------------------------------------------------------

func TestCheckpoints_List(t *testing.T) {
	s, secret := newCollabServer(t)
	s.SetCheckpointProvider(&fakeCheckpointProvider{list: []CheckpointRecord{
		{ID: "cp-1", FromAgent: "code-agent", ToAgent: "test-agent", Status: "pending",
			Files: []CheckpointFileRecord{{Path: "main.py", Lines: 10, IsNew: true}}},
	}})
	rec := serve(s, authedReq(http.MethodGet, "/api/checkpoints", secret, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", rec.Code, rec.Body)
	}
	var resp struct {
		Checkpoints []CheckpointRecord `json:"checkpoints"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Checkpoints) != 1 || resp.Checkpoints[0].ID != "cp-1" {
		t.Errorf("checkpoints = %+v", resp.Checkpoints)
	}
}

func TestCheckpoints_Approve(t *testing.T) {
	s, secret := newCollabServer(t)
	cp := &fakeCheckpointProvider{}
	s.SetCheckpointProvider(cp)
	rec := serve(s, authedReq(http.MethodPost, "/api/checkpoints/cp-1/approve", secret, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d (%s)", rec.Code, rec.Body)
	}
	if len(cp.approved) != 1 || cp.approved[0] != "cp-1" {
		t.Errorf("approved = %v", cp.approved)
	}
}

func TestCheckpoints_Reject(t *testing.T) {
	s, secret := newCollabServer(t)
	cp := &fakeCheckpointProvider{}
	s.SetCheckpointProvider(cp)
	rec := serve(s, authedReq(http.MethodPost, "/api/checkpoints/cp-2/reject", secret, `{"reason":"nope"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("reject = %d (%s)", rec.Code, rec.Body)
	}
	if len(cp.rejected) != 1 || cp.rejected[0] != "cp-2" {
		t.Errorf("rejected = %v", cp.rejected)
	}
}

func TestCheckpoints_ApproveUnknown404(t *testing.T) {
	s, secret := newCollabServer(t)
	s.SetCheckpointProvider(&fakeCheckpointProvider{err: errNotFound})
	rec := serve(s, authedReq(http.MethodPost, "/api/checkpoints/missing/approve", secret, ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("approve unknown = %d, want 404", rec.Code)
	}
}

func TestCheckpoints_Unconfigured503(t *testing.T) {
	s, secret := newCollabServer(t)
	rec := serve(s, authedReq(http.MethodPost, "/api/checkpoints/cp-1/approve", secret, ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured approve = %d, want 503", rec.Code)
	}
}

func TestParseLimit(t *testing.T) {
	cases := map[string]int{"": 100, "5": 5, "0": 100, "-3": 100, "abc": 100, "25": 25}
	for in, want := range cases {
		if got := parseLimit(in, 100); got != want {
			t.Errorf("parseLimit(%q) = %d, want %d", in, got, want)
		}
	}
}
