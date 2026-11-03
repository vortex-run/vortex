package agents

import (
	"strings"
	"testing"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[strings.ToUpper(k)] = v
	}
	return m
}

// Forge builds run generated code (Gradle scripts, cgo). They need toolchain
// settings but must never see VORTEX's secrets.
func TestBuildEnv_KeepsToolchainDropsSecrets(t *testing.T) {
	t.Setenv("VORTEX_ANTHROPIC_KEY", "sk-secret")
	t.Setenv("VORTEX_TELEGRAM_TOKEN", "123:abc")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("VORTEX_MASTER_KEY", "deadbeef")
	t.Setenv("GOPROXY", "https://proxy.example")
	t.Setenv("ANDROID_HOME", "/opt/android")
	env := envMap(BuildEnv())
	for _, secret := range []string{"VORTEX_ANTHROPIC_KEY", "VORTEX_TELEGRAM_TOKEN", "AWS_SECRET_ACCESS_KEY", "VORTEX_MASTER_KEY"} {
		if _, ok := env[secret]; ok {
			t.Errorf("BuildEnv leaks %s", secret)
		}
	}
	if env["GOPROXY"] != "https://proxy.example" || env["ANDROID_HOME"] != "/opt/android" {
		t.Errorf("BuildEnv dropped toolchain settings: GOPROXY=%q ANDROID_HOME=%q", env["GOPROXY"], env["ANDROID_HOME"])
	}
	if _, ok := env["PATH"]; !ok {
		t.Error("BuildEnv dropped PATH")
	}
}

// The plain agent environment stays as narrow as before (no toolchain extras).
func TestScrubbedEnv_Unchanged(t *testing.T) {
	t.Setenv("GOPROXY", "https://proxy.example")
	t.Setenv("VORTEX_OPENAI_KEY", "sk-x")
	env := envMap(scrubbedEnv())
	if _, ok := env["GOPROXY"]; ok {
		t.Error("scrubbedEnv now includes build variables")
	}
	if _, ok := env["VORTEX_OPENAI_KEY"]; ok {
		t.Error("scrubbedEnv leaks a provider key")
	}
}

// git hooks run with the agent's git environment: SSH agent and identity
// pass through, secrets don't.
func TestGitEnv_KeepsGitAccessDropsSecrets(t *testing.T) {
	t.Setenv("VORTEX_ANTHROPIC_KEY", "sk-secret")
	t.Setenv("VORTEX_MASTER_KEY", "deadbeef")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("GIT_AUTHOR_NAME", "Dev")
	env := envMap(gitEnv())
	if _, ok := env["VORTEX_ANTHROPIC_KEY"]; ok {
		t.Error("gitEnv leaks a provider key")
	}
	if _, ok := env["VORTEX_MASTER_KEY"]; ok {
		t.Error("gitEnv leaks the master key")
	}
	if env["SSH_AUTH_SOCK"] != "/tmp/agent.sock" || env["GIT_AUTHOR_NAME"] != "Dev" {
		t.Errorf("gitEnv dropped git settings: %v", env)
	}
}
