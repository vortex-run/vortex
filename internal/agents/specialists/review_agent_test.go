package specialists

import (
	"context"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/a2a"
)

func reviewAgentWith(t *testing.T, gw AIGateway, tools *fakeTools) *ReviewAgent {
	t.Helper()
	base := NewBaseAgent(a2a.AgentCard{ID: "review-agent", Name: "Review"}, gw, tools.run, nil, t.TempDir())
	return NewReviewAgent(base)
}

const approvedReview = `{"score":9,"approved":true,
  "issues":[{"severity":"low","file":"main.py","line":45,"message":"missing docstring"}],
  "security_issues":[],
  "summary":"Clean implementation, well tested."}`

const rejectedReview = `{"score":5,"approved":false,
  "issues":[{"severity":"high","file":"auth.py","line":67,"message":"SQL injection risk"}],
  "security_issues":[{"severity":"high","file":"auth.py","line":67,"message":"SQL injection risk"}],
  "summary":"Security problems."}`

func TestReview_ReadsAllFiles(t *testing.T) {
	tools := newFakeTools()
	tools.reads["main.py"] = "print(1)"
	tools.reads["auth.py"] = "def login(): pass"
	gw := &fakeGateway{reply: approvedReview}
	a := reviewAgentWith(t, gw, tools)

	a.HandleTask(context.Background(),
		a2a.Task{ID: "t1", Goal: "review", Files: []string{"main.py", "auth.py"}}, nil)

	if !tools.calledWith("read_file") {
		t.Error("review should read the files")
	}
	if !strings.Contains(gw.lastUser, "print(1)") || !strings.Contains(gw.lastUser, "def login") {
		t.Errorf("file contents not in review prompt:\n%s", gw.lastUser)
	}
}

func TestReview_HighScoreApproved(t *testing.T) {
	a := reviewAgentWith(t, &fakeGateway{reply: approvedReview}, newFakeTools())
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "review"}, nil)
	if !res.Approved || !res.Success {
		t.Errorf("score 9 should be approved: %+v", res)
	}
	if res.Score != 9 {
		t.Errorf("score = %d, want 9", res.Score)
	}
	if !strings.Contains(res.Output, "✓ Approved") || !strings.Contains(res.Output, "well tested") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestReview_LowScoreNotApproved(t *testing.T) {
	a := reviewAgentWith(t, &fakeGateway{reply: rejectedReview}, newFakeTools())
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "review"}, nil)
	if res.Approved || res.Success {
		t.Errorf("score 5 should not be approved: %+v", res)
	}
	if !strings.Contains(res.Output, "✗ Needs fixes") {
		t.Errorf("output = %q", res.Output)
	}
	if len(res.Errors) == 0 {
		t.Error("rejected review should surface issues as errors")
	}
}

func TestReview_ScoreGateOverridesModelFlag(t *testing.T) {
	// Model claims approved:true but score is 4 — the gate must reject.
	a := reviewAgentWith(t, &fakeGateway{reply: `{"score":4,"approved":true,"summary":"meh"}`}, newFakeTools())
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "review"}, nil)
	if res.Approved {
		t.Error("score 4 must not be approved even if the model says so")
	}
}

func TestReview_SecurityIssuesInOutput(t *testing.T) {
	a := reviewAgentWith(t, &fakeGateway{reply: rejectedReview}, newFakeTools())
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "review"}, nil)
	if !strings.Contains(res.Output, "Security issues") || !strings.Contains(res.Output, "SQL injection") {
		t.Errorf("security issues missing from output:\n%s", res.Output)
	}
}

func TestReview_MalformedJSONHandled(t *testing.T) {
	a := reviewAgentWith(t, &fakeGateway{reply: "Looks good to me, ship it!"}, newFakeTools())
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "review"}, nil)
	if res.Approved || res.Success {
		t.Error("unparseable review must fail safe (not approved)")
	}
	if res.Score != 0 {
		t.Errorf("malformed review score = %d, want 0", res.Score)
	}
	if len(res.Errors) == 0 {
		t.Error("expected a parse error")
	}
}

func TestFormatReview(t *testing.T) {
	approved := formatReview(ReviewResult{
		Score: 8, Approved: true, Summary: "good",
		Issues: []ReviewIssue{{Severity: "low", File: "main.py", Line: 45, Message: "docstring"}},
	})
	if !strings.Contains(approved, "Score: 8/10 ✓ Approved") || !strings.Contains(approved, "main.py:45") {
		t.Errorf("approved format = %q", approved)
	}
	rejected := formatReview(ReviewResult{
		Score: 5, Approved: false,
		Issues: []ReviewIssue{{Severity: "high", File: "auth.py", Line: 67, Message: "injection"}},
	})
	if !strings.Contains(rejected, "✗ Needs fixes") || !strings.Contains(rejected, "[HIGH]") {
		t.Errorf("rejected format = %q", rejected)
	}
}
