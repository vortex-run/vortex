package messaging

import (
	"strings"
)

// SlackAccess is what a signed Slack principal may do. Slack proves a request
// came from Slack, not that the person is someone the operator trusts, so
// access is granted per registered user and denied by default.
type SlackAccess int

const (
	// SlackNone: not registered. Only `whoami` and `help` answer, so the
	// person can find their IDs and ask the operator to register them.
	SlackNone SlackAccess = iota
	// SlackReadOnly: status, logs and help. No agent requests, no actions.
	SlackReadOnly
	// SlackOperator: full access: free-form agent requests, deploy, reload.
	SlackOperator
)

// slackAllowList holds the registered Slack principals. Entries are a user ID
// ("U012AB") or a team-scoped user ("T012AB:U012AB"); a bare user ID matches in
// any allowed workspace.
type slackAllowList struct {
	teams    map[string]bool // empty = any workspace (users still required)
	operator map[string]bool
	readOnly map[string]bool
}

func newSlackAllowList(teams, operators, readOnly []string) slackAllowList {
	set := func(xs []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			if x = strings.TrimSpace(x); x != "" {
				m[x] = true
			}
		}
		return m
	}
	return slackAllowList{teams: set(teams), operator: set(operators), readOnly: set(readOnly)}
}

// access resolves the level for a verified (team, user) pair. A request with a
// missing or malformed identity gets SlackNone.
func (l slackAllowList) access(teamID, userID string) SlackAccess {
	if slackSessionID(teamID, userID) == "" {
		return SlackNone
	}
	if len(l.teams) > 0 && !l.teams[teamID] {
		return SlackNone
	}
	has := func(m map[string]bool) bool { return m[userID] || m[teamID+":"+userID] }
	switch {
	case has(l.operator):
		return SlackOperator
	case has(l.readOnly):
		return SlackReadOnly
	}
	return SlackNone
}

// configured reports whether any user is registered at all, so the bot can
// tell an unconfigured install apart from an unregistered person.
func (l slackAllowList) configured() bool { return len(l.operator)+len(l.readOnly) > 0 }

// requiredAccess is the level a subcommand needs.
func requiredAccess(sub string) SlackAccess {
	switch sub {
	case "whoami", "help", "":
		return SlackNone
	case "status", "logs":
		return SlackReadOnly
	default: // deploy, reload, and every free-form agent request
		return SlackOperator
	}
}
