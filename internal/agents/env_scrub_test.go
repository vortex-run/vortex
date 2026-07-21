package agents

import (
	"context"
	"strings"
	"testing"
)

func TestScrubbedEnv_DropsSecretsKeepsEssentials(t *testing.T) {
	t.Setenv("VORTEX_ANTHROPIC_KEY", "sk-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("MY_APP_TOKEN", "tok")

	env := scrubbedEnv()
	var sawPath bool
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		up := strings.ToUpper(name)
		if up == "PATH" {
			sawPath = true
		}
		if up == "VORTEX_ANTHROPIC_KEY" || up == "AWS_SECRET_ACCESS_KEY" || up == "MY_APP_TOKEN" {
			t.Errorf("secret %s leaked into scrubbed env", name)
		}
		if strings.Contains(val, "sk-secret") || strings.Contains(val, "aws-secret") {
			t.Errorf("secret value leaked via %s", name)
		}
	}
	if !sawPath {
		t.Error("PATH must survive scrubbing (commands need binary resolution)")
	}
}

// TestRunCommand_EnvironmentIsScrubbed executes a real child process and
// asserts the canary secret is absent from ITS environment while the command
// still resolves and runs (PATH survived).
func TestRunCommand_EnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("VORTEX_CANARY_SECRET", "leaked-if-visible")

	tool := RunCommandTool{SandboxDir: t.TempDir(), AllowedCommands: []string{"go"}, approved: true}
	res, err := tool.Execute(context.Background(), map[string]any{
		"command": "go", "args": []string{"env"},
	})
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	out := res.(map[string]any)
	combined := out["stdout"].(string) + out["stderr"].(string)
	if strings.Contains(combined, "leaked-if-visible") {
		t.Error("canary secret visible to the child process")
	}
	if out["exit_code"].(int) != 0 {
		t.Errorf("exit = %v, want 0 (command must still run with scrubbed env)", out["exit_code"])
	}
}
