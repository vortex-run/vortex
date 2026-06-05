package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const auditTestConfig = `
cluster: {name: "audit-test-cluster"}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`

// setupAudit points the audit log + config at temp paths and returns a runner
// that executes `vortex audit <args>` against an isolated log.
func setupAudit(t *testing.T) func(args ...string) (string, error) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "audit.log")
	t.Setenv("VORTEX_AUDIT_LOG", logPath)

	cfgPath := filepath.Join(t.TempDir(), "vortex.cue")
	if err := os.WriteFile(cfgPath, []byte(auditTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	old := flags.configPath
	flags.configPath = cfgPath
	t.Cleanup(func() { flags.configPath = old })

	return func(args ...string) (string, error) {
		c := newAuditCommand()
		var buf bytes.Buffer
		c.SetOut(&buf)
		c.SetErr(&buf)
		c.SetArgs(args)
		err := c.Execute()
		return buf.String(), err
	}
}

func TestAudit_CommandRegisters(t *testing.T) {
	c := newAuditCommand()
	if c.Use != "audit" {
		t.Errorf("Use = %q, want audit", c.Use)
	}
	names := map[string]bool{}
	for _, sub := range c.Commands() {
		names[sub.Name()] = true
	}
	if !names["verify"] || !names["export"] {
		t.Errorf("audit should have verify and export subcommands, got %v", names)
	}
}

func TestAudit_VerifyHasDescription(t *testing.T) {
	c := newAuditVerifyCommand()
	if c.Use != "verify" {
		t.Errorf("Use = %q, want verify", c.Use)
	}
	if c.Short == "" || !strings.Contains(strings.ToLower(c.Short), "integrity") {
		t.Errorf("verify Short should mention integrity: %q", c.Short)
	}
}

func TestAudit_ExportHasFlags(t *testing.T) {
	c := newAuditExportCommand()
	for _, name := range []string{"format", "since", "until", "actor", "action", "output"} {
		if c.Flags().Lookup(name) == nil {
			t.Errorf("export should have --%s flag", name)
		}
	}
}

func TestAudit_VerifyEmptyLogSucceeds(t *testing.T) {
	run := setupAudit(t)
	out, err := run("verify")
	if err != nil {
		t.Fatalf("verify on empty log: %v\n%s", err, out)
	}
	if !strings.Contains(out, "integrity verified") {
		t.Errorf("verify output = %q, want integrity verified", out)
	}
}

func TestAudit_ExportJSONToFile(t *testing.T) {
	run := setupAudit(t)
	outFile := filepath.Join(t.TempDir(), "out.json")
	if _, err := run("export", "--format", "json", "--output", outFile); err != nil {
		t.Fatalf("export: %v", err)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("export output is not valid JSON: %v\n%s", err, data)
	}
}
