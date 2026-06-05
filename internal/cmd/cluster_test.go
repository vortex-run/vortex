package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const clusterTestConfig = `
cluster: {name: "cluster-cli-test"}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`

func TestCluster_CommandRegisters(t *testing.T) {
	c := newClusterCommand()
	if c.Use != "cluster" {
		t.Errorf("Use = %q, want cluster", c.Use)
	}
	names := map[string]bool{}
	for _, sub := range c.Commands() {
		names[sub.Name()] = true
	}
	for _, want := range []string{"status", "join", "leave"} {
		if !names[want] {
			t.Errorf("cluster should have %q subcommand, got %v", want, names)
		}
	}
}

func TestCluster_JoinRequiresAddr(t *testing.T) {
	c := newClusterJoinCommand()
	if err := c.Args(c, []string{}); err == nil {
		t.Error("join should require an address argument")
	}
	if err := c.Args(c, []string{"10.0.0.1:7946"}); err != nil {
		t.Errorf("join with one arg should be valid: %v", err)
	}
}

func TestCluster_LeaveRegisters(t *testing.T) {
	if newClusterLeaveCommand().Use != "leave" {
		t.Error("leave subcommand Use should be 'leave'")
	}
}

func TestCluster_StatusSingleNode(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "vortex.cue")
	if err := os.WriteFile(cfgPath, []byte(clusterTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	old := flags.configPath
	flags.configPath = cfgPath
	t.Cleanup(func() { flags.configPath = old })

	c := newClusterCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs([]string{"status"})
	if err := c.Execute(); err != nil {
		t.Fatalf("cluster status: %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "Node ID:") {
		t.Errorf("status output should include a Node ID:\n%s", out)
	}
	if !strings.Contains(out, "single-node") {
		t.Errorf("status with one node should report single-node:\n%s", out)
	}
}

func TestCluster_StatusMultiNode(t *testing.T) {
	cfg := `
cluster: {name: "multi", nodes: ["10.0.0.1", "10.0.0.2", "10.0.0.3"]}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`
	cfgPath := filepath.Join(t.TempDir(), "vortex.cue")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	old := flags.configPath
	flags.configPath = cfgPath
	t.Cleanup(func() { flags.configPath = old })

	c := newClusterCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs([]string{"status"})
	if err := c.Execute(); err != nil {
		t.Fatalf("cluster status: %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "multi-node") {
		t.Errorf("status with 3 nodes should report multi-node:\n%s", out)
	}
	if !strings.Contains(out, "3 configured") {
		t.Errorf("status should list 3 configured members:\n%s", out)
	}
}
