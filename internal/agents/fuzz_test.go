package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Whatever path an agent asks for, containInTrustedRoot either refuses it or
// returns a path inside the (symlink-resolved) trusted root.
func FuzzContainInTrustedRoot(f *testing.F) {
	for _, s := range []string{"a.txt", "sub/a.txt", "../x", "..\\x", "sub/../../x", "/etc/passwd", `C:\Windows\x`, "NUL", "a/./b", ".git/hooks/pre-commit", "", "...", "a\x00b"} {
		f.Add(s)
	}
	root := f.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		f.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, p string) {
		// Unclean concatenation on purpose, so lexical tricks reach the guard.
		candidate := p
		if !filepath.IsAbs(p) {
			candidate = root + string(os.PathSeparator) + p
		}
		got, err := containInTrustedRoot(root, candidate)
		if err != nil {
			return
		}
		if !pathWithinBase(got, realRoot) {
			t.Fatalf("containInTrustedRoot(%q) = %q, outside %q", candidate, got, realRoot)
		}
	})
}

// Any path with a .git segment (however Windows spells it) must keep its
// approval prompt under Auto Mode.
func FuzzAutoModeSensitiveGitSegments(f *testing.F) {
	for _, s := range []string{".git/config", ".GIT/hooks/x", "a/.git./x", "a/.git /x", ".git::$INDEX_ALLOCATION/x", "x/y", "vortex.cue", "src/git/x"} {
		f.Add(s)
	}
	root := f.TempDir()
	f.Fuzz(func(t *testing.T, rel string) {
		rel = filepath.ToSlash(rel)
		if strings.ContainsAny(rel, "\x00") || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.Contains(rel, ":/") {
			return // not a plain relative path under the root
		}
		hasGit := false
		for _, part := range strings.Split(rel, "/") {
			part, _, _ = strings.Cut(part, ":")
			if strings.EqualFold(strings.TrimRight(part, ". "), ".git") {
				hasGit = true
			}
		}
		if !hasGit {
			return
		}
		if !autoModeSensitive(root, filepath.Join(root, filepath.FromSlash(rel))) {
			t.Fatalf("autoModeSensitive(%q) = false for a .git path", rel)
		}
	})
}

// The git argument filter must catch hooksPath however it's cased.
func FuzzValidateArgsGitHooksPath(f *testing.F) {
	f.Add("-c", "core.hooksPath=/tmp/h")
	f.Add("config", "CORE.HOOKSPATH")
	f.Add("status", "--short")
	f.Fuzz(func(t *testing.T, a, b string) {
		err := validateArgs("git", []string{a, b})
		if (strings.Contains(strings.ToLower(a), "core.hookspath") || strings.Contains(strings.ToLower(b), "core.hookspath")) && err == nil {
			t.Fatalf("validateArgs(git, %q, %q) allowed core.hooksPath", a, b)
		}
	})
}
