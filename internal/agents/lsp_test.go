package agents

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLSPClient_GracefulWhenServerAbsent(t *testing.T) {
	// A language whose server is almost certainly not installed in CI.
	c, err := NewLSPClient(LSPConfig{Language: "rust", ServerCmd: []string{"definitely-not-a-real-lsp-binary"}})
	if err != nil {
		t.Errorf("missing server should be a graceful nil,nil, got err: %v", err)
	}
	if c != nil {
		t.Error("missing server should yield a nil client")
		_ = c.Close()
	}
}

func TestNewLSPClient_UnknownLanguage(t *testing.T) {
	c, err := NewLSPClient(LSPConfig{Language: "cobol"})
	if err != nil || c != nil {
		t.Errorf("unknown language should be nil,nil, got %v,%v", c, err)
	}
}

func TestLSPTool_Registers(t *testing.T) {
	reg := NewToolRegistry()
	tool := &LSPDiagnosticsTool{WorkDir: t.TempDir()}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("registering LSP tool: %v", err)
	}
	if tool.Name() != "lsp_diagnostics" {
		t.Errorf("Name = %q", tool.Name())
	}
	got, err := reg.Get("lsp_diagnostics")
	if err != nil || got == nil {
		t.Errorf("LSP tool not retrievable: %v", err)
	}
}

func TestLSPTool_UnsupportedFileIsEmptyResult(t *testing.T) {
	tool := &LSPDiagnosticsTool{WorkDir: t.TempDir()}
	defer tool.Close()
	res, err := tool.Execute(context.Background(), map[string]any{"file": "notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["count"] != 0 {
		t.Errorf("unsupported file should yield 0 diagnostics, got %v", m["count"])
	}
}

func TestLSPTool_NoServerIsEmptyResult(t *testing.T) {
	// If gopls happens to be installed in this environment, skip — we want to
	// exercise the missing-server path.
	if _, err := exec.LookPath("gopls"); err == nil {
		t.Skip("gopls is installed; this test covers the missing-server path")
	}
	tool := &LSPDiagnosticsTool{WorkDir: t.TempDir()}
	defer tool.Close()
	res, err := tool.Execute(context.Background(), map[string]any{"file": "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["count"] != 0 {
		t.Errorf("missing server should yield 0 diagnostics, got %v", m["count"])
	}
	if m["note"] == nil {
		t.Error("missing-server result should include an explanatory note")
	}
}

func TestLanguageForFile(t *testing.T) {
	cases := map[string]string{
		"main.go":   "go",
		"app.py":    "python",
		"index.ts":  "typescript",
		"page.tsx":  "typescript",
		"lib.rs":    "rust",
		"notes.txt": "",
		"Makefile":  "",
	}
	for file, want := range cases {
		if got := languageForFile(file); got != want {
			t.Errorf("languageForFile(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestURIRoundTrip(t *testing.T) {
	abs, _ := filepath.Abs("foo/bar.go")
	uri := pathToURI(abs)
	back := uriToPath(uri)
	// The round trip must preserve the absolute path on every platform: Unix
	// absolute paths keep their leading slash; Windows drive paths round-trip
	// to the original drive-letter form.
	if back != abs {
		t.Errorf("uri round trip = %q, want %q (via %q)", back, abs, uri)
	}
	if !strings.HasPrefix(uri, "file://") {
		t.Errorf("uri %q should start with file://", uri)
	}
}

func TestURIToPath_PlatformForms(t *testing.T) {
	// Unix absolute path must keep its leading slash.
	if got := uriToPath("file:///home/user/main.go"); filepath.ToSlash(got) != "/home/user/main.go" {
		t.Errorf("unix uriToPath = %q, want /home/user/main.go", got)
	}
	// Windows drive path must drop the URI's leading slash.
	if got := uriToPath("file:///C:/proj/main.go"); filepath.ToSlash(got) != "C:/proj/main.go" {
		t.Errorf("windows uriToPath = %q, want C:/proj/main.go", got)
	}
}

// TestLSPTool_StaysInTheWorkspace: a file outside WorkDir is refused before
// anything reads it or starts a language server.
func TestLSPTool_StaysInTheWorkspace(t *testing.T) {
	work, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(secret, []byte("package x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := &LSPDiagnosticsTool{WorkDir: work}
	defer tool.Close()
	rel, _ := filepath.Rel(work, secret)
	for _, f := range []string{secret, rel} {
		if _, err := tool.Execute(context.Background(), map[string]any{"file": f}); !errors.Is(err, ErrWorkspaceEscape) {
			t.Errorf("file %q: err = %v, want ErrWorkspaceEscape", f, err)
		}
	}
	// Compare real paths: the temp dir can sit behind a symlink (macOS
	// /var → /private/var) or a short name (Windows RUNNER~1).
	realWork, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tool.resolve("pkg/main.go"); err != nil || got != filepath.Join(realWork, "pkg", "main.go") {
		t.Errorf("relative path resolved to %q, %v; want %q", got, err, filepath.Join(realWork, "pkg", "main.go"))
	}
}
