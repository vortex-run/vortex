package agents

import "testing"

func TestRiskFor(t *testing.T) {
	cases := map[string]string{
		"list_directory": RiskLow,
		"read_local":     RiskLow,
		"git_status":     RiskLow,
		"write_file":     RiskMedium,
		"write_local":    RiskMedium,
		"edit_file":      RiskMedium,
		"run_command":    RiskHigh,
		"run_terminal":   RiskHigh,
		"git_commit":     RiskHigh,
		"delete_file":    RiskHigh,
		"docker_run":     RiskHigh,
		"ssh_command":    RiskCritical,
	}
	for tool, want := range cases {
		if got := RiskFor(tool); got != want {
			t.Errorf("RiskFor(%q) = %q, want %q", tool, got, want)
		}
	}
}

func TestRiskFor_UnknownDefaultsMedium(t *testing.T) {
	if got := RiskFor("some_new_mutating_tool"); got != RiskMedium {
		t.Errorf("unknown tool risk = %q, want medium (fail toward caution)", got)
	}
}

func TestApprovalRequest_Risk(t *testing.T) {
	// Explicit risk wins.
	if got := (ApprovalRequest{RiskLevel: RiskCritical, Tool: "write_file"}).Risk(); got != RiskCritical {
		t.Errorf("explicit risk = %q, want critical", got)
	}
	// Derived from Tool when unset.
	if got := (ApprovalRequest{Tool: "run_terminal"}).Risk(); got != RiskHigh {
		t.Errorf("derived risk = %q, want high", got)
	}
	// A bare run_command request (no Tool) derives from its Command.
	if got := (ApprovalRequest{Command: "rm"}).Risk(); got != RiskHigh {
		t.Errorf("command-only risk = %q, want high", got)
	}
}

func TestRiskLevels_Escalate(t *testing.T) {
	// The four levels must be distinct strings (the UI keys headers/badges off them).
	levels := []string{RiskLow, RiskMedium, RiskHigh, RiskCritical}
	seen := map[string]bool{}
	for _, l := range levels {
		if l == "" || seen[l] {
			t.Errorf("risk level %q is empty or duplicated", l)
		}
		seen[l] = true
	}
}
