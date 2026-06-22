package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleAgentsMD = `# AGENTS.md

## Project
FastAPI authentication service

## Stack
- Python 3.12
- FastAPI
- PostgreSQL
- JWT tokens

## Commands
test: pytest tests/ -v
build: (not applicable)
lint: ruff check .
start: uvicorn main:app --reload

## Code Rules
- Use type hints on all functions
- Write docstrings for public functions
- Tests use pytest with fixtures
- Never commit secrets

## Paths
source: src/
tests: tests/

## Description
REST API with JWT auth, user management, PostgreSQL backend.`

func TestAgentsMD_LoadMissingIsNil(t *testing.T) {
	md, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	if md != nil {
		t.Errorf("missing AGENTS.md should return nil, got %+v", md)
	}
}

func TestAgentsMD_ParsesProjectAndStack(t *testing.T) {
	dir := t.TempDir()
	writeAgentsMD(t, dir, sampleAgentsMD)
	md, err := Load(dir)
	if err != nil || md == nil {
		t.Fatalf("Load: %v, %v", md, err)
	}
	if md.Project != "FastAPI authentication service" {
		t.Errorf("Project = %q", md.Project)
	}
	if len(md.Stack) != 4 || md.Stack[0] != "Python 3.12" || md.Stack[2] != "PostgreSQL" {
		t.Errorf("Stack = %v", md.Stack)
	}
}

func TestAgentsMD_ParsesCommands(t *testing.T) {
	dir := t.TempDir()
	writeAgentsMD(t, dir, sampleAgentsMD)
	md, _ := Load(dir)
	if md.TestCmd != "pytest tests/ -v" {
		t.Errorf("TestCmd = %q", md.TestCmd)
	}
	if md.LintCmd != "ruff check ." {
		t.Errorf("LintCmd = %q", md.LintCmd)
	}
	if md.StartCmd != "uvicorn main:app --reload" {
		t.Errorf("StartCmd = %q", md.StartCmd)
	}
}

func TestAgentsMD_ParsesRulesAndPaths(t *testing.T) {
	dir := t.TempDir()
	writeAgentsMD(t, dir, sampleAgentsMD)
	md, _ := Load(dir)
	if len(md.Rules) != 4 || md.Rules[0] != "Use type hints on all functions" {
		t.Errorf("Rules = %v", md.Rules)
	}
	if md.Paths.Source != "src/" || md.Paths.Tests != "tests/" {
		t.Errorf("Paths = %+v", md.Paths)
	}
	if !strings.Contains(md.Description, "JWT auth") {
		t.Errorf("Description = %q", md.Description)
	}
}

func TestAgentsMD_SystemPromptAddition(t *testing.T) {
	dir := t.TempDir()
	writeAgentsMD(t, dir, sampleAgentsMD)
	md, _ := Load(dir)
	add := md.SystemPromptAddition()
	for _, want := range []string{
		"Project: FastAPI authentication service",
		"Stack: Python 3.12, FastAPI, PostgreSQL, JWT tokens",
		"Test command: pytest tests/ -v",
		"Use type hints on all functions",
	} {
		if !strings.Contains(add, want) {
			t.Errorf("SystemPromptAddition missing %q:\n%s", want, add)
		}
	}
	// Nil receiver and empty MD are safe.
	if (*AgentsMD)(nil).SystemPromptAddition() != "" {
		t.Error("nil MD should give empty addition")
	}
	if (&AgentsMD{}).SystemPromptAddition() != "" {
		t.Error("empty MD should give empty addition")
	}
}

func TestAgentsMD_ParentDirectorySearch(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root, sampleAgentsMD)
	child := filepath.Join(root, "src", "deep")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	md, err := Load(child)
	if err != nil || md == nil {
		t.Fatalf("Load from child dir: %v, %v", md, err)
	}
	if md.Project != "FastAPI authentication service" {
		t.Error("parent-directory AGENTS.md not found from child")
	}
}

func TestAgentsMD_Generate(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\ngo 1.26\n"), 0o600)
	gw := &agentsMDGateway{reply: sampleAgentsMD}
	md, err := Generate(context.Background(), gw, dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if md.Project == "" {
		t.Error("generated MD not parsed")
	}
	// The file was written and is loadable.
	loaded, _ := Load(dir)
	if loaded == nil || loaded.Project != md.Project {
		t.Error("Generate should write a loadable AGENTS.md")
	}
	// The generator was given the project listing (go.mod head).
	if !strings.Contains(gw.lastPrompt, "go.mod") {
		t.Errorf("generator prompt missing project listing:\n%s", gw.lastPrompt)
	}
}

func TestAgentsMD_GenerateRejectsBadReply(t *testing.T) {
	dir := t.TempDir()
	gw := &agentsMDGateway{reply: "I cannot do that"}
	if _, err := Generate(context.Background(), gw, dir); err == nil {
		t.Error("Generate should reject a non-AGENTS.md reply")
	}
}

// --- helpers ---------------------------------------------------------------

type agentsMDGateway struct {
	reply      string
	lastPrompt string
}

func (g *agentsMDGateway) Complete(_ context.Context, prompt, _ string) (string, error) {
	g.lastPrompt = prompt
	return g.reply, nil
}

func writeAgentsMD(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
