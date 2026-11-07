package messaging

import (
	"strings"
	"testing"
	"time"
)

func TestSlackAllowList_Access(t *testing.T) {
	l := newSlackAllowList(nil, []string{"U_OPS", "T2:U_SCOPED"}, []string{"U_VIEW"})
	cases := []struct {
		team, user string
		want       SlackAccess
	}{
		{"T1", "U_OPS", SlackOperator},
		{"T9", "U_OPS", SlackOperator}, // bare user ID: any workspace
		{"T2", "U_SCOPED", SlackOperator},
		{"T1", "U_SCOPED", SlackNone}, // team-scoped entry only matches its team
		{"T1", "U_VIEW", SlackReadOnly},
		{"T1", "U_RANDOM", SlackNone},
		{"T1", "", SlackNone},
		{"", "U_OPS", SlackNone},
	}
	for _, c := range cases {
		if got := l.access(c.team, c.user); got != c.want {
			t.Errorf("access(%q,%q) = %v, want %v", c.team, c.user, got, c.want)
		}
	}
	pinned := newSlackAllowList([]string{"T1"}, []string{"U_OPS"}, nil)
	if pinned.access("T9", "U_OPS") != SlackNone {
		t.Error("AllowedTeams must restrict registered users to those workspaces")
	}
}

// Deny by default: an unregistered workspace member can only run whoami;
// a read-only user cannot reach the agent; an operator can.
func TestSlack_SlashAccessLevels(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	bot, _ := NewSlackBot(SlackConfig{SigningSecret: "sec", AllowedUsers: []string{"T1:U_OPS"},
		ReadOnlyUsers: []string{"U_VIEW"}, now: func() time.Time { return now }})
	run := func(user, text string) string {
		return slashAs(t, bot, "sec", "T1", user, text, now)
	}
	if out := run("U_RANDOM", "reload"); !strings.Contains(out, "not registered") {
		t.Errorf("unregistered reload: %s", out)
	}
	if out := run("U_RANDOM", "whoami"); !strings.Contains(out, "user_id=U_RANDOM") || !strings.Contains(out, "access=not registered") {
		t.Errorf("whoami: %s", out)
	}
	if out := run("U_VIEW", "status"); strings.Contains(out, "not registered") || strings.Contains(out, "read-only access") {
		t.Errorf("read-only status refused: %s", out)
	}
	if out := run("U_VIEW", "deploy prod"); !strings.Contains(out, "read-only access") {
		t.Errorf("read-only deploy must be refused: %s", out)
	}
	if out := run("U_OPS", "reload"); !strings.Contains(out, "reload requested") {
		t.Errorf("operator reload: %s", out)
	}
	empty, _ := NewSlackBot(SlackConfig{SigningSecret: "sec", now: func() time.Time { return now }})
	if out := slashAs(t, empty, "sec", "T1", "U_OPS", "reload", now); !strings.Contains(out, "No Slack users are registered") {
		t.Errorf("unconfigured bot must deny and explain: %s", out)
	}
}
