package agents

import (
	"context"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/llm"
)

func TestParseAgentFlags(t *testing.T) {
	cases := []struct {
		in, check, rest string
		bad             bool
	}{
		{"fix the test", "", "fix the test", false},
		{`--check "go test ./..." fix the test`, "go test ./...", "fix the test", false},
		{"--check=make fix it", "make", "fix it", false},
		{"--check make fix it", "make", "fix it", false},
		{`--check "unclosed fix`, "", "", true},
		{`--check "" fix`, "", "", true},
	}
	for _, tc := range cases {
		check, rest, err := parseAgentFlags(tc.in)
		if (err != nil) != tc.bad || check != tc.check || rest != tc.rest {
			t.Errorf("%q → check=%q rest=%q err=%v", tc.in, check, rest, err)
		}
	}
}

// TestLoop_VerifyFailureContinuesTheRun: a failed check sends its output
// back and the run continues until the check passes.
func TestLoop_VerifyFailureContinuesTheRun(t *testing.T) {
	m := &scriptedModel{replies: []llm.ToolChatResult{{Text: "done"}, {Text: "really done"}}}
	calls := 0
	res := RunLoop(context.Background(), LoopConfig{Model: m, Verify: func(context.Context) (bool, string) {
		calls++
		return calls == 2, "FAIL: TestX"
	}}, "fix it")
	if !res.Checked || !res.Verified || res.Checks != 2 || res.Answer != "really done" {
		t.Fatalf("res = %+v", res)
	}
	last := m.seen[1][len(m.seen[1])-1]
	if last.Role != "user" || !strings.Contains(last.Content, "FAIL: TestX") {
		t.Errorf("model wasn't shown the failing check: %+v", last)
	}
}

func TestLoop_VerifyGivesUpAfterMaxAttempts(t *testing.T) {
	m := &scriptedModel{} // always answers "done"
	res := RunLoop(context.Background(), LoopConfig{Model: m, MaxVerify: 2,
		Verify: func(context.Context) (bool, string) { return false, "still failing" }}, "fix it")
	if res.Stop != LoopDone || res.Verified || res.Checks != 2 {
		t.Errorf("res = %+v", res)
	}
}

// TestAgentCommand_CheckRunsThroughApproval: the check is a real command,
// approved like any other, and the run reports verified or not.
func TestAgentCommand_CheckRunsThroughApproval(t *testing.T) {
	for _, tc := range []struct {
		check, want string
	}{
		{"exit 0", "✓ Verified: exit 0 passed"},
		{"exit 1", "✗ Not verified: exit 1 still fails after 3 attempt(s)"},
	} {
		var asked []string
		approve := func(_ context.Context, req ApprovalRequest) bool {
			asked = append(asked, req.Tool)
			return true
		}
		c, _ := agentCoord(t, toolGateway{&scriptedModel{}}, approve)
		reply, err := c.HandleMessage(context.Background(), `/agent --check "`+tc.check+`" make it pass`, "chk")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(reply, tc.want) {
			t.Errorf("%s: reply = %q", tc.check, reply)
		}
		if len(asked) == 0 || asked[0] != "run_terminal" {
			t.Errorf("%s: check didn't go through approval: %v", tc.check, asked)
		}
	}
}

func TestAgentCommand_CheckRejectedIsNotVerified(t *testing.T) {
	c, _ := agentCoord(t, toolGateway{&scriptedModel{}}, func(context.Context, ApprovalRequest) bool { return false })
	reply, _ := c.HandleMessage(context.Background(), `/agent --check "exit 0" do it`, "chk2")
	if !strings.Contains(reply, "Not verified") {
		t.Errorf("a check the user refused must not count as passed: %q", reply)
	}
}
