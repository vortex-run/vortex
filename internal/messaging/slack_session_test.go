package messaging

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sandeepbollavaram/vortex/internal/agents"
)

type slackEchoGW struct{}

func (slackEchoGW) Complete(_ context.Context, prompt, sys string) (string, error) {
	if strings.Contains(strings.ToLower(sys), "classify") {
		return "GENERAL_QUESTION", nil
	}
	return "ECHO:" + strings.ReplaceAll(prompt, "\n", " | "), nil
}

// Audit F-07: one Slack user's conversation history must never be fed into
// another user's replies. Before the fix every request used session "slack".
func TestSlack_SessionsArePerPrincipal(t *testing.T) {
	c, err := agents.NewCoordinator(agents.CoordinatorConfig{
		Bus: agents.NewBus(), Tools: agents.NewSandboxedRegistry(agents.NewToolRegistry(), t.TempDir(), nil, nil),
		AIGateway: slackEchoGW{}, MaxAgents: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	ms, err := agents.NewMemoryStore(t.TempDir() + "/conversations.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	c.SetMemoryStore(ms) // as start.go wires it; no episodic store
	rt, err := agents.NewRuntime(agents.RuntimeConfig{Bus: agents.NewBus(), Coordinator: c, MaxAgents: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()
	const secret = "slack-session-test-secret"
	bot, err := NewSlackBot(SlackConfig{SigningSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	h := bot.HandleSlashCommand(rt)
	send := func(team, user, text string) string {
		body := []byte(url.Values{"team_id": {team}, "user_id": {user}, "command": {"/vortex"}, "text": {text}}.Encode())
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		m := hmac.New(sha256.New, []byte(secret))
		fmt.Fprintf(m, "v0:%s:%s", ts, body)
		req := httptest.NewRequest(http.MethodPost, "/webhook/slack", strings.NewReader(string(body)))
		req.Header.Set("X-Slack-Request-Timestamp", ts)
		req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(m.Sum(nil)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Result().Body)
		return fmt.Sprintf("%d %s", rec.Code, b)
	}
	r1 := send("T1", "U_ALICE", "remember canary CANARY-F07")
	if !strings.HasPrefix(r1, "200") {
		t.Fatalf("alice: %s", r1)
	}
	r2 := send("T1", "U_BOB", "what did I say before")
	if strings.Contains(r2, "CANARY-F07") {
		t.Fatalf("U_BOB's reply contains U_ALICE's history: %s", r2)
	}
	// Alice still gets her own history.
	if r3 := send("T1", "U_ALICE", "what did I say before"); !strings.Contains(r3, "CANARY-F07") {
		t.Errorf("own history lost: %s", r3)
	}
	// A request with no user identity is refused, not routed to a shared session.
	if r4 := send("T1", "", "hello there"); !strings.Contains(r4, "no Slack team/user identity") {
		t.Errorf("identity-less request: %s", r4)
	}
}

func TestSlackSessionID(t *testing.T) {
	if got := slackSessionID("T012AB", "U34CD"); got != "slack-T012AB-U34CD" {
		t.Errorf("got %q", got)
	}
	for _, bad := range [][2]string{{"", "U1"}, {"T1", ""}, {"T1", "U-1"}, {"T1", "../x"}, {"T1", strings.Repeat("U", 33)}} {
		if got := slackSessionID(bad[0], bad[1]); got != "" {
			t.Errorf("slackSessionID(%q,%q) = %q, want refusal", bad[0], bad[1], got)
		}
	}
}
