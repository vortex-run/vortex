package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestSkill(t *testing.T, root, name, doc string) {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(d, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsList(t *testing.T) {
	ws := t.TempDir()
	writeTestSkill(t, filepath.Join(ws, ".agents", "skills"), "deploy",
		"---\nname: deploy\ndescription: Deploy the app\nallowed-tools: Bash Read\n---\nbody\n")
	writeTestSkill(t, filepath.Join(ws, ".claude", "skills"), "broken",
		"---\nname: other\ndescription: x\n---\n")
	var out bytes.Buffer
	err := runSkillsList(&out, ws)
	if !errors.Is(err, errSkills) {
		t.Fatalf("err = %v, want errSkills", err)
	}
	text := out.String()
	for _, want := range []string{
		"deploy", "Deploy the app", ".agents/skills/deploy/SKILL.md", "sha256:",
		"allowed-tools requested (not granted): Bash Read",
		"SKIPPED", "must match its directory",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}

func TestSkillsList_Empty(t *testing.T) {
	var out bytes.Buffer
	if err := runSkillsList(&out, t.TempDir()); err != nil || !strings.Contains(out.String(), "No skills") {
		t.Errorf("err=%v out=%q", err, out.String())
	}
}
