package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/config"
)

// stubRuntime is a fake AgentRuntime for API tests.
type stubRuntime struct {
	response string
	stats    AgentRuntimeStats
	subErr   error
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
