package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgentsLocalToolsDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agents.LocalTools.TrustedRoot != "" {
		t.Errorf("trusted_root default = %q, want empty", cfg.Agents.LocalTools.TrustedRoot)
	}
	if cfg.Agents.LocalTools.AutoMode {
		t.Error("auto_mode default should be false")
	}
}

func TestResolveLocalTools_AutoModeRequiresTrustedRoot(t *testing.T) {
	dir := t.TempDir()
	lt := LocalTools{AutoMode: true}
	if _, _, err := lt.ResolveLocalTools(dir); err == nil {
		t.Fatal("expected error for auto_mode without trusted_root")
	} else if !strings.Contains(err.Error(), "trusted_root") {
		t.Errorf("error = %v", err)
	}
}

func TestResolveLocalTools_EmptyTrustedRoot(t *testing.T) {
	dir := t.TempDir()
	lt := LocalTools{}
	root, auto, err := lt.ResolveLocalTools(dir)
	if err != nil {
		t.Fatalf("ResolveLocalTools: %v", err)
	}
	if root != "" || auto {
		t.Errorf("got trusted=%q auto=%v, want empty/false", root, auto)
	}
}

func TestResolveLocalTools_DisjointWorkDirAndTrusted(t *testing.T) {
	work := t.TempDir()
	other := t.TempDir()
	lt := LocalTools{TrustedRoot: other}
	if _, _, err := lt.ResolveLocalTools(work); err == nil {
		t.Fatal("expected error when work dir and trusted_root are disjoint")
	} else if !strings.Contains(err.Error(), "nest") && !strings.Contains(err.Error(), "contain") {
		t.Errorf("error = %v, want contain/nest message", err)
	}
}

// TestResolveLocalTools_SiblingPrefixRejected guards the prefix trap: a work
// dir "<base>/trusted-other" must not count as inside trusted_root "<base>/tr"
// merely because the strings share a prefix.
func TestResolveLocalTools_SiblingPrefixRejected(t *testing.T) {
	base := t.TempDir()
	trusted := filepath.Join(base, "tr")
	work := filepath.Join(base, "trusted-other")
	for _, d := range []string{trusted, work} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lt := LocalTools{TrustedRoot: trusted}
	if _, _, err := lt.ResolveLocalTools(work); err == nil {
		t.Fatal("expected error: sibling with shared name prefix is not inside trusted_root")
	}
	if pathLexicallyContainedIn(trusted, work) {
		t.Fatal("pathLexicallyContainedIn must reject sibling prefix")
	}
	if !pathLexicallyContainedIn(trusted, filepath.Join(trusted, "sub", "deeper")) {
		t.Fatal("pathLexicallyContainedIn must accept nested descendant")
	}
	if pathLexicallyContainedIn(trusted, filepath.Join(trusted, "..")) {
		t.Fatal("pathLexicallyContainedIn must reject parent")
	}
}

func TestResolveLocalTools_TrustedInsideWorkDirRejected(t *testing.T) {
	work := t.TempDir()
	trusted := filepath.Join(work, "only-subdir")
	if err := os.Mkdir(trusted, 0o755); err != nil {
		t.Fatal(err)
	}
	lt := LocalTools{TrustedRoot: trusted}
	if _, _, err := lt.ResolveLocalTools(work); err == nil {
		t.Fatal("expected error when trusted_root is a strict subdirectory of work dir")
	}
}

func TestResolveLocalTools_WorkDirInsideTrusted(t *testing.T) {
	trusted := t.TempDir()
	work := filepath.Join(trusted, "pkg")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	lt := LocalTools{TrustedRoot: trusted}
	got, _, err := lt.ResolveLocalTools(work)
	if err != nil {
		t.Fatalf("ResolveLocalTools: %v", err)
	}
	want, _ := filepath.Abs(trusted)
	if got != want {
		t.Errorf("trusted = %q, want %q", got, want)
	}
}

func TestLoadAutoModeRequiresTrustedRoot(t *testing.T) {
	body := validConfig + `
agents: {local_tools: {auto_mode: true}}
`
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("expected load error for auto_mode without trusted_root")
	}
	if !strings.Contains(err.Error(), "trusted_root") {
		t.Errorf("error should mention trusted_root, got: %v", err)
	}
}

func TestResolveLocalTools_InvalidPath(t *testing.T) {
	dir := t.TempDir()
	lt := LocalTools{TrustedRoot: filepath.Join(dir, "missing")}
	if _, _, err := lt.ResolveLocalTools(dir); err == nil {
		t.Fatal("expected error for missing trusted_root directory")
	} else if !strings.Contains(err.Error(), "trusted_root") {
		t.Errorf("error = %v, want trusted_root in message", err)
	}
}

func TestResolveLocalTools_NotDirectory(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lt := LocalTools{TrustedRoot: f}
	if _, _, err := lt.ResolveLocalTools(dir); err == nil {
		t.Fatal("expected error when trusted_root is a file")
	} else if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error = %v", err)
	}
}

func TestResolveLocalTools_RelativeToWorkDir(t *testing.T) {
	outer := t.TempDir()
	trusted := filepath.Join(outer, "proj")
	work := filepath.Join(trusted, "src")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// Relative trusted_root is resolved against workDir; ".." → parent proj,
	// which must contain work (workDir ⊆ trusted_root).
	lt := LocalTools{TrustedRoot: "..", AutoMode: true}
	gotRoot, auto, err := lt.ResolveLocalTools(work)
	if err != nil {
		t.Fatalf("ResolveLocalTools: %v", err)
	}
	if !auto {
		t.Error("auto_mode should be true")
	}
	want, _ := filepath.Abs(trusted)
	if gotRoot != want {
		t.Errorf("trusted root = %q, want %q", gotRoot, want)
	}
}

func TestEnvOverrideAgentsLocalTools(t *testing.T) {
	trusted := t.TempDir()
	work := filepath.Join(trusted, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"VORTEX_AGENTS_LOCAL_TOOLS_TRUSTED_ROOT": trusted,
		"VORTEX_AGENTS_LOCAL_TOOLS_AUTO_MODE":    "true",
	}
	cfg, err := loadFromBytes(filepath.Join(work, "vortex.cue"), []byte(validConfig), schemaBytes(), env)
	if err != nil {
		t.Fatalf("loadFromBytes: %v", err)
	}
	got, auto, err := cfg.Agents.LocalTools.ResolveLocalTools(work)
	if err != nil {
		t.Fatalf("ResolveLocalTools: %v", err)
	}
	if !auto {
		t.Error("expected auto_mode true from env")
	}
	want, _ := filepath.Abs(trusted)
	if got != want {
		t.Errorf("trusted = %q, want %q", got, want)
	}
}
