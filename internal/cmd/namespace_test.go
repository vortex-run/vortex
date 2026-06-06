package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestNamespace_CommandRegisters(t *testing.T) {
	c := newNamespaceCommand()
	if c.Use != "namespace" {
		t.Errorf("Use = %q, want namespace", c.Use)
	}
	names := map[string]bool{}
	for _, sub := range c.Commands() {
		names[sub.Name()] = true
	}
	for _, want := range []string{"list", "create", "delete", "quota"} {
		if !names[want] {
			t.Errorf("namespace should have %q subcommand, got %v", want, names)
		}
	}
}

func TestNamespace_CreateRequiresOrg(t *testing.T) {
	t.Setenv("VORTEX_NAMESPACE_STORE", t.TempDir()+"/ns.json")
	c := newNamespaceCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs([]string{"create", "ns-1"}) // no --org
	if err := c.Execute(); err == nil {
		t.Error("create without --org should fail")
	}
	if !strings.Contains(buf.String(), "--org is required") {
		t.Errorf("expected org-required message, got: %s", buf.String())
	}
}

func TestNamespace_CreateAndList(t *testing.T) {
	t.Setenv("VORTEX_NAMESPACE_STORE", t.TempDir()+"/ns.json")

	run := func(args ...string) string {
		c := newNamespaceCommand()
		var buf bytes.Buffer
		c.SetOut(&buf)
		c.SetErr(&buf)
		c.SetArgs(args)
		if err := c.Execute(); err != nil {
			t.Fatalf("namespace %v: %v\n%s", args, err, buf.String())
		}
		return buf.String()
	}

	if out := run("create", "team-a", "--org", "org-1", "--max-routes", "5"); !strings.Contains(out, "created") {
		t.Errorf("create output = %q", out)
	}
	out := run("list")
	if !strings.Contains(out, "team-a") || !strings.Contains(out, "org-1") {
		t.Errorf("list should show team-a/org-1:\n%s", out)
	}
}

func TestNamespace_QuotaOutput(t *testing.T) {
	t.Setenv("VORTEX_NAMESPACE_STORE", t.TempDir()+"/ns.json")

	create := newNamespaceCommand()
	var cb bytes.Buffer
	create.SetOut(&cb)
	create.SetErr(&cb)
	create.SetArgs([]string{"create", "team-a", "--org", "org-1", "--max-routes", "7", "--bandwidth-mbps", "0"})
	if err := create.Execute(); err != nil {
		t.Fatalf("create: %v\n%s", err, cb.String())
	}

	q := newNamespaceCommand()
	var qb bytes.Buffer
	q.SetOut(&qb)
	q.SetErr(&qb)
	q.SetArgs([]string{"quota", "team-a"})
	if err := q.Execute(); err != nil {
		t.Fatalf("quota: %v\n%s", err, qb.String())
	}
	out := qb.String()
	if !strings.Contains(out, "Max routes:") || !strings.Contains(out, "7") {
		t.Errorf("quota output missing max routes:\n%s", out)
	}
	if !strings.Contains(out, "unlimited") {
		t.Errorf("0 bandwidth should render as unlimited:\n%s", out)
	}
}
