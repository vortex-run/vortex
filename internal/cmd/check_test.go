package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const checkValidConfig = `
cluster: {name: "prod-cluster-1", nodes: ["10.0.0.1", "10.0.0.2"]}
tls: {acme_email: "you@example.com"}
routes: [{name: "web", protocol: "https", host: "x.com", backends: [{host: "127.0.0.1", port: 3000}]}]
security: {}
secrets: {keys: ["a", "b", "c"]}
observability: {}
`

func writeCheckConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "vortex.cue")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

// runCheck executes the check command with flags.configPath set to path.
// Output (cmd.OutOrStdout) and error output (cmd.OutOrStderr) are captured into
// a single buffer; cobra's OutOrStderr resolves to the out writer when one is
// set, so success and failure lines both land here. Returns (output, error).
func runCheck(t *testing.T, path string) (string, error) {
	t.Helper()
	old := flags.configPath
	flags.configPath = path
	t.Cleanup(func() { flags.configPath = old })

	c := newCheckCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs(nil)
	err := c.Execute()
	return buf.String(), err
}

func TestCheckValidConfigSucceeds(t *testing.T) {
	out, err := runCheck(t, writeCheckConfig(t, checkValidConfig))
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !strings.Contains(out, "is valid") {
		t.Errorf("output should contain 'is valid':\n%s", out)
	}
	for _, section := range []string{"cluster", "tls", "routes", "security", "secrets", "observability"} {
		if !strings.Contains(out, section) {
			t.Errorf("output missing section %q:\n%s", section, out)
		}
	}
}

func TestCheckMissingRequiredFieldFails(t *testing.T) {
	body := `
cluster: {nodes: ["10.0.0.1"]}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`
	out, err := runCheck(t, writeCheckConfig(t, body))
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if !strings.Contains(out, "name") {
		t.Errorf("output should mention field 'name':\n%s", out)
	}
}

// TestCheckRejectsLocalToolsStartWouldRefuse is the regression for `check`
// reporting "is valid" on a config that `start` refuses: agents.local_tools is
// schema-valid as strings, but start also resolves trusted_root (must exist, be
// a directory, and contain the work dir). check must apply the same rule.
func TestCheckRejectsLocalToolsStartWouldRefuse(t *testing.T) {
	work := t.TempDir()
	t.Setenv("VORTEX_WORK_DIR", work)

	// trusted_root points at a directory that does not exist.
	body := checkValidConfig + "\nagents: {local_tools: {trusted_root: \"" +
		strings.ReplaceAll(filepath.Join(work, "missing"), `\`, `\\`) + "\"}}\n"
	out, err := runCheck(t, writeCheckConfig(t, body))
	if err == nil {
		t.Fatalf("expected check to fail for missing trusted_root, output:\n%s", out)
	}
	if !strings.Contains(out, "agents.local_tools") {
		t.Errorf("output should name agents.local_tools:\n%s", out)
	}
	if strings.Contains(out, "is valid") {
		t.Errorf("output must not claim validity:\n%s", out)
	}

	// Disjoint trusted_root (does not contain the work dir) is also refused.
	other := t.TempDir()
	body = checkValidConfig + "\nagents: {local_tools: {trusted_root: \"" +
		strings.ReplaceAll(other, `\`, `\\`) + "\"}}\n"
	out, err = runCheck(t, writeCheckConfig(t, body))
	if err == nil {
		t.Fatalf("expected check to fail for disjoint trusted_root, output:\n%s", out)
	}
	if !strings.Contains(out, "agents.local_tools") {
		t.Errorf("output should name agents.local_tools:\n%s", out)
	}
}

// TestCheckAcceptsLocalToolsContainingWorkDir: a trusted_root that contains the
// work dir passes and is reported on the agents line.
func TestCheckAcceptsLocalToolsContainingWorkDir(t *testing.T) {
	trusted := t.TempDir()
	work := filepath.Join(trusted, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VORTEX_WORK_DIR", work)
	body := checkValidConfig + "\nagents: {local_tools: {trusted_root: \"" +
		strings.ReplaceAll(trusted, `\`, `\\`) + "\", auto_mode: true}}\n"
	out, err := runCheck(t, writeCheckConfig(t, body))
	if err != nil {
		t.Fatalf("expected success, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "is valid") || !strings.Contains(out, "auto_mode=true") {
		t.Errorf("output should be valid and show auto_mode:\n%s", out)
	}
}

func TestCheckWrongTypeReportsLineNumber(t *testing.T) {
	body := "cluster: {\n  name: \"c\"\n  gossip_port: 99999\n}\ntls: {acme_email: \"a@b.com\"}\nroutes: []\nsecurity: {}\nsecrets: {}\nobservability: {}\n"
	out, err := runCheck(t, writeCheckConfig(t, body))
	if err == nil {
		t.Fatal("expected error for out-of-range value")
	}
	// Line numbers are rendered as ":<n>" within the (file:line:col) suffix.
	if !strings.Contains(out, ":3") && !strings.Contains(out, "schema.cue:") {
		t.Errorf("output should contain a line number:\n%s", out)
	}
}
