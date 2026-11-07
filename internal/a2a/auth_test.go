package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireToken(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	tok, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(tok))
	}

	cases := []struct {
		name, configured, sent string
		want                   int
	}{
		{"no header", tok, "", http.StatusUnauthorized},
		{"wrong token", tok, tok[:63] + "x", http.StatusUnauthorized},
		{"right token", tok, tok, http.StatusOK},
		{"empty configured token rejects even an empty header", "", "", http.StatusUnauthorized},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/a2a/agents/code-agent/rpc", nil)
		if c.sent != "" {
			req.Header.Set(TokenHeader, c.sent)
		}
		rec := httptest.NewRecorder()
		RequireToken(c.configured, ok).ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestNewToken_Unique(t *testing.T) {
	a, _ := NewToken()
	b, _ := NewToken()
	if a == b {
		t.Fatal("two tokens are equal")
	}
}

// The coordinator's client must authenticate end to end, and a client without
// the token (anyone else reaching the API port) must be refused.
func TestAgentClient_SendsToken(t *testing.T) {
	srv := NewAgentServer()
	tok, _ := NewToken()
	ts := httptest.NewServer(RequireToken(tok, srv.Handler()))
	defer ts.Close()

	authed := NewAgentClient(ts.URL, "coordinator")
	authed.SetToken(tok)
	if _, err := authed.ListAgents(context.Background()); err != nil {
		t.Fatalf("client with token: %v", err)
	}

	stranger := NewAgentClient(ts.URL, "stranger")
	if _, err := stranger.ListAgents(context.Background()); err == nil {
		t.Fatal("client without token was served")
	}
	if _, err := stranger.Submit(context.Background(), "code-agent", *NewTask("stranger", "code-agent", "rm -rf", "s")); err == nil {
		t.Fatal("task submitted without token")
	}
}
