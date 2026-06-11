package agents

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// LSPDiagnosticsTool exposes language-server diagnostics (errors/warnings) for
// a file as an agent tool (build plan M20). It is read-only, so it needs no
// approval. The tool lazily starts one LSP client per language and reuses it;
// when the language server is not installed it returns an empty, non-error
// result so the agent degrades gracefully.
type LSPDiagnosticsTool struct {
	// WorkDir is the workspace root passed to the language server.
	WorkDir string

	mu      sync.Mutex
	clients map[string]*LSPClient // language → client (nil = tried, unavailable)
}

// Name returns the tool name.
func (*LSPDiagnosticsTool) Name() string { return "lsp_diagnostics" }

// Description returns a human-readable summary.
func (*LSPDiagnosticsTool) Description() string {
	return "Get language-server errors/warnings for a file (read-only, no approval)"
}

// Execute runs diagnostics on params["file"]. It returns a structured result
// with the diagnostics list and a count.
func (t *LSPDiagnosticsTool) Execute(_ context.Context, params map[string]any) (any, error) {
	file, err := strParam(params, "file")
	if err != nil {
		return nil, err
	}
	lang := languageForFile(file)
	if lang == "" {
		return map[string]any{"diagnostics": []Diagnostic{}, "count": 0, "note": "unsupported file type"}, nil
	}

	client := t.clientFor(lang)
	if client == nil {
		return map[string]any{
			"diagnostics": []Diagnostic{}, "count": 0,
			"note": fmt.Sprintf("no language server available for %s (install %s)", lang, defaultServerName(lang)),
		}, nil
	}

	diags, derr := client.Diagnostics(file)
	if derr != nil {
		return nil, derr
	}
	return map[string]any{"diagnostics": diags, "count": len(diags)}, nil
}

// clientFor returns (lazily starting) the LSP client for a language, or nil
// when the server is unavailable. The nil result is cached so a missing server
// is not re-probed on every call.
func (t *LSPDiagnosticsTool) clientFor(lang string) *LSPClient {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.clients == nil {
		t.clients = make(map[string]*LSPClient)
	}
	if c, tried := t.clients[lang]; tried {
		return c
	}
	c, err := NewLSPClient(LSPConfig{Language: lang, WorkDir: t.WorkDir})
	if err != nil {
		c = nil // start failed; treat as unavailable
	}
	t.clients[lang] = c // cache result (including nil) so we don't re-probe
	return c
}

// Close shuts down all started LSP clients.
func (t *LSPDiagnosticsTool) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.clients {
		if c != nil {
			_ = c.Close()
		}
	}
	t.clients = nil
}

// languageForFile maps a file extension to an LSP language id, or "" when
// unsupported.
func languageForFile(file string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx", ".js", ".jsx":
		return "typescript"
	case ".rs":
		return "rust"
	default:
		return ""
	}
}

// defaultServerName returns the conventional server binary name for a language.
func defaultServerName(lang string) string {
	if cmd := DefaultLSPServers[lang]; len(cmd) > 0 {
		return cmd[0]
	}
	return "the language server"
}
