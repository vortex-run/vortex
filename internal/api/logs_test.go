package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
)

func TestLogBuffer_StoresEntries(t *testing.T) {
	b := NewLogBuffer(10)
	b.Write(LogEntry{Msg: "one"})
	b.Write(LogEntry{Msg: "two"})
	if b.Len() != 2 {
		t.Fatalf("Len = %d, want 2", b.Len())
	}
	last := b.Last(2)
	if last[0].Msg != "one" || last[1].Msg != "two" {
		t.Errorf("Last = %+v, want [one two] in order", last)
	}
}

func TestLogBuffer_RingEviction(t *testing.T) {
	b := NewLogBuffer(3)
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		b.Write(LogEntry{Msg: m})
	}
	if b.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (capped)", b.Len())
	}
	last := b.Last(3)
	if last[0].Msg != "c" || last[2].Msg != "e" {
		t.Errorf("ring should keep the newest 3 [c d e], got %+v", last)
	}
}

func TestLogBuffer_LastLimit(t *testing.T) {
	b := NewLogBuffer(10)
	for _, m := range []string{"a", "b", "c", "d"} {
		b.Write(LogEntry{Msg: m})
	}
	if got := b.Last(2); len(got) != 2 || got[0].Msg != "c" || got[1].Msg != "d" {
		t.Errorf("Last(2) = %+v, want [c d]", got)
	}
	if got := b.Last(0); len(got) != 4 {
		t.Errorf("Last(0) should return all, got %d", len(got))
	}
}

func TestLogBuffer_ConcurrentWrites(t *testing.T) {
	b := NewLogBuffer(1000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				b.Write(LogEntry{Msg: "x"})
			}
		}()
	}
	wg.Wait()
	if b.Len() != 1000 {
		t.Errorf("Len = %d, want 1000 after 1000 concurrent writes", b.Len())
	}
}

// logsServer builds an authed server with a populated log buffer.
func logsServer(t *testing.T, populate bool) (*Server, string) {
	t.Helper()
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	_, secret, err := keys.Issue("u", "default", []auth.Role{auth.RoleOperator}, "tok", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)

	buf := NewLogBuffer(100)
	if populate {
		buf.Write(LogEntry{Time: "10:00:00", Level: "INFO", Msg: "VORTEX started"})
		buf.Write(LogEntry{Time: "10:00:01", Level: "WARN", Msg: "gossip"})
	}
	s.SetLogBuffer(buf)
	return s, secret
}

func TestAPI_LogsReturnsEntries(t *testing.T) {
	s, secret := logsServer(t, true)
	req := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Entries []LogEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 2 || body.Entries[0].Msg != "VORTEX started" {
		t.Errorf("entries = %+v, want the 2 buffered lines", body.Entries)
	}
}

func TestAPI_LogsLimit(t *testing.T) {
	s, secret := logsServer(t, true)
	req := httptest.NewRequest(http.MethodGet, "/api/logs?limit=1", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	var body struct {
		Entries []LogEntry `json:"entries"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Entries) != 1 || body.Entries[0].Msg != "gossip" {
		t.Errorf("limit=1 should return the newest entry, got %+v", body.Entries)
	}
}

func TestAPI_LogsRequiresAuth(t *testing.T) {
	s, _ := logsServer(t, true)
	req := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	req.RemoteAddr = "127.0.0.1:5555" // loopback must NOT bypass
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("logs without key = %d, want 401", rec.Code)
	}
}
