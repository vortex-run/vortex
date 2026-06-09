package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func searchDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "util.go"), []byte("package main\nfunc helper() {}\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("just text\n"), 0o600)
	sub := filepath.Join(dir, "node_modules")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "dep.go"), []byte("func ignored() {}\n"), 0o600)
	return dir
}

func TestSearchFiles_FindsMatches(t *testing.T) {
	dir := searchDir(t)
	res, err := SearchFilesTool{cfg: LocalFSConfig{Root: dir}}.Execute(context.Background(),
		map[string]any{"pattern": "func"})
	if err != nil {
		t.Fatal(err)
	}
	matches := res.(map[string]any)["matches"].([]map[string]any)
	// main() + helper() = 2 matches; node_modules ignored.
	if len(matches) != 2 {
		t.Errorf("matches = %d, want 2 (node_modules excluded): %+v", len(matches), matches)
	}
	for _, m := range matches {
		if f := m["file"].(string); filepath.Base(filepath.Dir(f)) == "node_modules" {
			t.Error("node_modules should be skipped")
		}
	}
}

func TestSearchFiles_ExtensionFilter(t *testing.T) {
	dir := searchDir(t)
	res, _ := SearchFilesTool{cfg: LocalFSConfig{Root: dir}}.Execute(context.Background(),
		map[string]any{"pattern": "text", "extension": "txt"})
	matches := res.(map[string]any)["matches"].([]map[string]any)
	if len(matches) != 1 {
		t.Errorf("extension filter should match only readme.txt, got %d", len(matches))
	}
}

func TestSearchFiles_CaseInsensitiveDefault(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "x.go"), []byte("FUNC Main\n"), 0o600)
	res, _ := SearchFilesTool{cfg: LocalFSConfig{Root: dir}}.Execute(context.Background(),
		map[string]any{"pattern": "func"})
	if res.(map[string]any)["count"].(int) != 1 {
		t.Error("default search should be case-insensitive")
	}
}

func TestFindFiles_GlobMatch(t *testing.T) {
	dir := searchDir(t)
	res, err := FindFilesTool{cfg: LocalFSConfig{Root: dir}}.Execute(context.Background(),
		map[string]any{"name_pattern": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	files := res.(map[string]any)["files"].([]string)
	if len(files) != 2 {
		t.Errorf("*.go should match main.go + util.go (not node_modules), got %d: %v", len(files), files)
	}
}

func TestSearchTools_Registered(t *testing.T) {
	names := map[string]bool{}
	for _, tl := range NewLocalTools(LocalFSConfig{Root: t.TempDir()}) {
		names[tl.Name()] = true
	}
	for _, want := range []string{"search_files", "find_files"} {
		if !names[want] {
			t.Errorf("local tools missing %q", want)
		}
	}
}

func TestParseLocalRequest_SearchFind(t *testing.T) {
	tool, params := parseLocalRequest("/search func main")
	if tool != "search_files" || params["pattern"] != "func main" {
		t.Errorf("/search → %q %v", tool, params)
	}
	tool, params = parseLocalRequest("/find *.go")
	if tool != "find_files" || params["name_pattern"] != "*.go" {
		t.Errorf("/find → %q %v", tool, params)
	}
}
