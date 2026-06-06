package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestTune_CommandRegisters(t *testing.T) {
	c := newTuneCommand()
	if c.Use != "tune" {
		t.Errorf("Use = %q, want tune", c.Use)
	}
	names := map[string]bool{}
	for _, sub := range c.Commands() {
		names[sub.Name()] = true
	}
	for _, want := range []string{"show", "apply", "bench"} {
		if !names[want] {
			t.Errorf("tune should have %q subcommand, got %v", want, names)
		}
	}
}

func TestTune_ShowOutput(t *testing.T) {
	c := newTuneCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs([]string{"show"})
	if err := c.Execute(); err != nil {
		t.Fatalf("tune show: %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "Recommended OS tuning") {
		t.Errorf("show output missing header:\n%s", out)
	}
	if !strings.Contains(out, "GOMAXPROCS") {
		t.Errorf("show output missing GOMAXPROCS:\n%s", out)
	}
}

func TestTune_ApplyDryRun(t *testing.T) {
	c := newTuneCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs([]string{"apply", "--dry-run"})
	if err := c.Execute(); err != nil {
		t.Fatalf("tune apply --dry-run: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "Dry run") {
		t.Errorf("dry-run output missing marker:\n%s", buf.String())
	}
}

func TestTune_BenchReportsThroughput(t *testing.T) {
	c := newTuneCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs([]string{"bench"})
	if err := c.Execute(); err != nil {
		t.Fatalf("tune bench: %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "Benchmark results") {
		t.Errorf("bench output missing header:\n%s", out)
	}
	if !strings.Contains(out, "MB/s") {
		t.Errorf("bench output should report MB/s:\n%s", out)
	}
}
