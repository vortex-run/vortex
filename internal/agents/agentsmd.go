package agents

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentsMD is a project's AGENTS.md context: the stack, commands, code rules,
// and paths that specialist agents read before starting work. Named after the
// AGENTS.md file it parses (the "stutter" with the package name is intentional
// and matches the on-disk artifact).
//
//nolint:revive // AgentsMD mirrors the AGENTS.md filename by design
type AgentsMD struct {
	Project     string
	Stack       []string
	TestCmd     string
	BuildCmd    string
	LintCmd     string
	StartCmd    string
	Rules       []string
	Paths       AgentPaths
	Description string
}

// AgentPaths are the conventional project directories.
type AgentPaths struct {
	Source string
	Tests  string
	Docs   string
	Config string
}

// agentsMDFileName is the on-disk file name.
const agentsMDFileName = "AGENTS.md"

// Load reads AGENTS.md from dir or any ancestor directory. It returns (nil,
// nil) when no file is found (absence is not an error).
func Load(dir string) (*AgentsMD, error) {
	path, ok := findAgentsMD(dir)
	if !ok {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agents: reading %s: %w", path, err)
	}
	return parseAgentsMD(string(data)), nil
}

// findAgentsMD searches dir and its ancestors for AGENTS.md.
func findAgentsMD(dir string) (string, bool) {
	d := dir
	for {
		p := filepath.Join(d, agentsMDFileName)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

// parseAgentsMD parses the markdown sections into an AgentsMD.
func parseAgentsMD(content string) *AgentsMD {
	m := &AgentsMD{}
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Section headers: "## Project", "## Stack", etc.
		if strings.HasPrefix(trimmed, "## ") {
			section = strings.ToLower(strings.TrimSpace(trimmed[3:]))
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			continue // document title
		}
		switch section {
		case "project":
			m.Project = appendLine(m.Project, trimmed)
		case "stack":
			if item := listItem(trimmed); item != "" {
				m.Stack = append(m.Stack, item)
			}
		case "commands":
			parseCommand(m, trimmed)
		case "code rules", "rules":
			if item := listItem(trimmed); item != "" {
				m.Rules = append(m.Rules, item)
			}
		case "paths":
			parsePath(&m.Paths, trimmed)
		case "description":
			m.Description = appendLine(m.Description, trimmed)
		}
	}
	return m
}

// parseCommand reads a "key: value" command line into the right field.
func parseCommand(m *AgentsMD, line string) {
	key, val, ok := splitKV(line)
	if !ok {
		return
	}
	switch key {
	case "test":
		m.TestCmd = val
	case "build":
		m.BuildCmd = val
	case "lint":
		m.LintCmd = val
	case "start", "run":
		m.StartCmd = val
	}
}

// parsePath reads a "key: value" path line into AgentPaths.
func parsePath(p *AgentPaths, line string) {
	key, val, ok := splitKV(line)
	if !ok {
		return
	}
	switch key {
	case "source", "src":
		p.Source = val
	case "tests", "test":
		p.Tests = val
	case "docs":
		p.Docs = val
	case "config":
		p.Config = val
	}
}

// SystemPromptAddition renders the AGENTS.md context for an agent's system
// prompt. Returns "" when nothing useful is set.
func (m *AgentsMD) SystemPromptAddition() string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Project context from AGENTS.md:\n")
	if m.Project != "" {
		b.WriteString("Project: " + m.Project + "\n")
	}
	if len(m.Stack) > 0 {
		b.WriteString("Stack: " + strings.Join(m.Stack, ", ") + "\n")
	}
	if m.TestCmd != "" {
		b.WriteString("Test command: " + m.TestCmd + "\n")
	}
	if m.BuildCmd != "" {
		b.WriteString("Build command: " + m.BuildCmd + "\n")
	}
	if m.LintCmd != "" {
		b.WriteString("Lint command: " + m.LintCmd + "\n")
	}
	if len(m.Rules) > 0 {
		b.WriteString("Code rules:\n")
		for _, r := range m.Rules {
			b.WriteString("- " + r + "\n")
		}
	}
	if m.Description != "" {
		b.WriteString("Description: " + m.Description + "\n")
	}
	out := strings.TrimRight(b.String(), "\n")
	if out == "Project context from AGENTS.md:" {
		return ""
	}
	return out
}

// genSystemPrompt instructs the AI to write an AGENTS.md for a project.
const genSystemPrompt = `You generate AGENTS.md files for software projects.
Given a directory listing and key file contents, produce an AGENTS.md in this exact markdown format:

# AGENTS.md

## Project
<one-line project name>

## Stack
- <language/framework>
- <...>

## Commands
test: <test command>
build: <build command>
lint: <lint command>
start: <start command>

## Code Rules
- <rule>
- <...>

## Paths
source: <dir>
tests: <dir>

## Description
<2-3 sentence description>

Return ONLY the markdown, no prose.`

// Generate auto-generates an AGENTS.md for the project at dir using the AI
// gateway, writes it to dir/AGENTS.md, and returns the parsed result.
func Generate(ctx context.Context, gateway AIGateway, dir string) (*AgentsMD, error) {
	if gateway == nil {
		return nil, fmt.Errorf("agents: no AI gateway for AGENTS.md generation")
	}
	listing := describeProjectDir(dir)
	reply, err := gateway.Complete(ctx, listing, genSystemPrompt)
	if err != nil {
		return nil, fmt.Errorf("agents: generating AGENTS.md: %w", err)
	}
	md := stripMarkdownFence(reply)
	if !strings.Contains(md, "## Project") {
		return nil, fmt.Errorf("agents: AI returned an unrecognised AGENTS.md")
	}
	path := filepath.Join(dir, agentsMDFileName)
	if werr := os.WriteFile(path, []byte(md), 0o644); werr != nil { //nolint:gosec // project doc
		return nil, fmt.Errorf("agents: writing AGENTS.md: %w", werr)
	}
	return parseAgentsMD(md), nil
}

// describeProjectDir builds a compact description of dir (entry names + a few
// key file heads) for the AGENTS.md generator prompt.
func describeProjectDir(dir string) string {
	var b strings.Builder
	b.WriteString("Directory: " + dir + "\nFiles:\n")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return b.String()
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		b.WriteString("  " + name + "\n")
	}
	// Include the head of common marker files.
	for _, f := range []string{"go.mod", "package.json", "requirements.txt", "pyproject.toml", "Cargo.toml", "pubspec.yaml"} {
		if data, rerr := os.ReadFile(filepath.Join(dir, f)); rerr == nil {
			head := string(data)
			if len(head) > 400 {
				head = head[:400]
			}
			b.WriteString("\n--- " + f + " ---\n" + head + "\n")
		}
	}
	return b.String()
}

// --- small parse helpers ----------------------------------------------------

// listItem strips a leading "- " or "* " bullet, returning "" for non-bullets.
func listItem(line string) string {
	for _, p := range []string{"- ", "* "} {
		if strings.HasPrefix(line, p) {
			return strings.TrimSpace(line[len(p):])
		}
	}
	return line // a bare line under a list section still counts
}

// splitKV splits "key: value" (case-insensitive key), trimming both sides.
func splitKV(line string) (key, val string, ok bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(line[:i])), strings.TrimSpace(line[i+1:]), true
}

// appendLine joins multi-line section text with a space.
func appendLine(existing, line string) string {
	if existing == "" {
		return line
	}
	return existing + " " + line
}

// stripMarkdownFence removes a wrapping ```markdown fence if present.
func stripMarkdownFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimRight(s, "\n"), "```"))
}
