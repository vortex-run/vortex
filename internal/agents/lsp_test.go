package agents

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
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
	// On Windows the round-trip normalises separators; compare base names.
	if filepath.Base(back) != "bar.go" {
		t.Errorf("uri round trip lost the file: %q -> %q -> %q", abs, uri, back)
	}
	if runtime.GOOS != "windows" && back != abs {
		t.Errorf("uri round trip = %q, want %q", back, abs)
	}
}
