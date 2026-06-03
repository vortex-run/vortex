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
