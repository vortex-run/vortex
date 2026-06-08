package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
)

// stubRuntime is a fake AgentRuntime for API tests.
type stubRuntime struct {
	response     string
	stats        AgentRuntimeStats
	subErr       error
	approveMatch bool // value returned by Approve
}

func (s stubRuntime) Submit(_ context.Context, _, _ string) (<-chan string, error) {
	if s.subErr != nil {
		return nil, s.subErr
	}
	ch := make(chan string, 1)
	ch <- s.response
	close(ch)
	return ch, nil
}

func (s stubRuntime) Stats() AgentRuntimeStats { return s.stats }

func (s stubRuntime) Approve(string, bool) (string, bool) { return "✓ done", s.approveMatch }

// newAgentTestServer starts a live management server with the agent runtime
// wired, returning its address and a cleanup func.
func newAgentTestServer(t *testing.T, rt AgentRuntime) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "vortex.cue")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr, err := config.NewManager(path, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := New(addr, mgr.Holder(), "test", discardLogger())
	srv.SetAgentRuntime(rt)
	srv.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	waitReady(t, addr)
	return addr
}

func TestAgentSubmit_ReturnsResponse(t *testing.T) {
	addr := newAgentTestServer(t, stubRuntime{response: "hello back"})

	body := strings.NewReader(`{"message":"hi","session_id":"s1"}`)
	resp, err := http.Post("http://"+addr+"/api/agents/submit", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got agentSubmitResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Response != "hello back" || got.SessionID != "s1" {
		t.Errorf("got %+v, want response='hello back' session=s1", got)
	}
}

func TestAgentSubmit_RejectsEmptyMessage(t *testing.T) {
	addr := newAgentTestServer(t, stubRuntime{})
	resp, err := http.Post("http://"+addr+"/api/agents/submit", "application/json",
		strings.NewReader(`{"message":""}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAgentStatus_ReturnsStats(t *testing.T) {
	addr := newAgentTestServer(t, stubRuntime{
		stats: AgentRuntimeStats{ActiveAgents: 2, TotalMessages: 5, QueueDepth: 1},
	})
	resp, err := http.Get("http://" + addr + "/api/agents/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got AgentRuntimeStats
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ActiveAgents != 2 || got.TotalMessages != 5 || got.QueueDepth != 1 {
		t.Errorf("stats = %+v, want {2 5 1}", got)
	}
}

func TestAgentEndpoints_503WhenUnconfigured(t *testing.T) {
	// No runtime wired → 503.
	addr := newAgentTestServer(t, nil)
	resp, err := http.Get("http://" + addr + "/api/agents/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
}

// newAuthedAgentServer builds an in-memory authed server with an agent runtime.
func newAuthedAgentServer(t *testing.T, rt AgentRuntime) (*Server, string) {
	t.Helper()
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	_, secret, err := keys.Issue("agent-user", "default", []auth.Role{auth.RoleOperator}, "agent token", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)
	s.SetAgentRuntime(rt)
	return s, secret
}

func TestAgentSubmit_RequiresAPIKeyEvenOnLocalhost(t *testing.T) {
	s, _ := newAuthedAgentServer(t, stubRuntime{response: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/api/agents/submit",
		strings.NewReader(`{"message":"x"}`))
	req.RemoteAddr = "127.0.0.1:5555" // loopback — must NOT bypass auth
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("submit without key from localhost = %d, want 401", rec.Code)
	}
}

func TestAgentSubmit_WithValidKeySucceeds(t *testing.T) {
	s, secret := newAuthedAgentServer(t, stubRuntime{response: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/api/agents/submit",
		strings.NewReader(`{"message":"x"}`))
	req.Header.Set("X-API-Key", secret)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Errorf("submit with valid key = %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
}

func TestAgentSubmit_RateLimited(t *testing.T) {
	s, secret := newAuthedAgentServer(t, stubRuntime{response: "hi"})
	// Freeze the limiter clock so the token bucket cannot refill mid-loop; this
	// makes the burst boundary deterministic regardless of CI load / -race
	// timing (the previous wall-clock version was flaky and could refill).
	frozen := time.Now()
	s.agentRateLimiter().SetClock(func() time.Time { return frozen })

	statuses := make([]int, 0, 12)
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/agents/submit",
			strings.NewReader(`{"message":"x"}`))
		req.Header.Set("X-API-Key", secret)
		req.RemoteAddr = "198.51.100.9:5555"
		statuses = append(statuses, serve(s, req).Code)
	}
	got429 := false
	for _, code := range statuses {
		if code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Errorf("expected a 429 within 12 rapid submits with frozen clock (burst 5); statuses=%v", statuses)
	}
}

func TestAgentSubmit_ConcurrencyCap503(t *testing.T) {
	// A runtime that reports ErrAgentBusy → handler must return 503.
	s, secret := newAuthedAgentServer(t, stubRuntime{subErr: ErrAgentBusy})
	req := httptest.NewRequest(http.MethodPost, "/api/agents/submit",
		strings.NewReader(`{"message":"x"}`))
	req.Header.Set("X-API-Key", secret)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("busy runtime = %d, want 503", rec.Code)
	}
}

func TestWantsSSE(t *testing.T) {
	cases := []struct {
		accept string
		want   bool
	}{
		{"text/event-stream", true},
		{"text/event-stream, */*", true},
		{"application/json, text/event-stream", true},
		{"text/event-stream;q=0.9", true},
		{"application/json", false},
		{"", false},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/agents/submit", nil)
		if c.accept != "" {
			req.Header.Set("Accept", c.accept)
		}
		if got := wantsSSE(req); got != c.want {
			t.Errorf("wantsSSE(%q) = %v, want %v", c.accept, got, c.want)
		}
	}
}

func TestAgentApprove_Resolves(t *testing.T) {
	s, secret := newAuthedAgentServer(t, stubRuntime{approveMatch: true})
	req := httptest.NewRequest(http.MethodPost, "/api/agents/approve",
		strings.NewReader(`{"session_id":"s1","approved":true}`))
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var body struct {
		Resolved bool `json:"resolved"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !body.Resolved {
		t.Error("resolved should be true")
	}
}

func TestAgentApprove_NoPending404(t *testing.T) {
	s, secret := newAuthedAgentServer(t, stubRuntime{approveMatch: false})
	req := httptest.NewRequest(http.MethodPost, "/api/agents/approve",
		strings.NewReader(`{"session_id":"ghost","approved":true}`))
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("no pending approval = %d, want 404", rec.Code)
	}
}

func TestAgentApprove_RequiresSession(t *testing.T) {
	s, secret := newAuthedAgentServer(t, stubRuntime{})
	req := httptest.NewRequest(http.MethodPost, "/api/agents/approve",
		strings.NewReader(`{"approved":true}`))
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing session_id = %d, want 400", rec.Code)
	}
}

func TestAgentApprove_RequiresAuth(t *testing.T) {
	s, _ := newAuthedAgentServer(t, stubRuntime{approveMatch: true})
	req := httptest.NewRequest(http.MethodPost, "/api/agents/approve",
		strings.NewReader(`{"session_id":"s1","approved":true}`))
	req.RemoteAddr = "127.0.0.1:5555" // loopback must NOT bypass
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("approve without key = %d, want 401", rec.Code)
	}
}
