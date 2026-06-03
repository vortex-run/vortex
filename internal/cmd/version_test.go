package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionDefaultOutputHasAllLabels(t *testing.T) {
	c := newVersionCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetArgs(nil)
	if err := c.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}
	out := buf.String()
	for _, label := range []string{"Version:", "Commit:", "Built:", "Go version:", "OS/Arch:"} {
		if !strings.Contains(out, label) {
			t.Errorf("output missing label %q\n%s", label, out)
		}
	}
}

func TestVersionShortIsExactlyOneLine(t *testing.T) {
	c := newVersionCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetArgs([]string{"--short"})
	if err := c.Execute(); err != nil {
		t.Fatalf("version --short failed: %v", err)
	}
	out := buf.String()

	// Exactly one trailing newline, no labels, no blank lines.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("--short produced %d lines, want 1: %q", len(lines), out)
	}
	if lines[0] != version {
		t.Errorf("--short output = %q, want %q", lines[0], version)
	}
	if strings.Contains(out, ":") {
		t.Errorf("--short output should contain no label: %q", out)
	}
}
