package agents

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// This file implements a minimal Language Server Protocol (LSP) client (build
// plan M20) giving agents real code intelligence: diagnostics (errors/
// warnings), hover docs, references, and go-to-definition. It speaks JSON-RPC
// 2.0 over a language server's stdin/stdout. The client is deliberately small
// and stdlib-only; it supports just the requests the agent needs.
//
// LSP is an optional enhancement: if the configured server binary is not
// installed, NewLSPClient returns (nil, nil) and callers degrade gracefully.

// LSPConfig configures an LSP client for one language/workspace.
type LSPConfig struct {
	Language  string   // "go" | "python" | "typescript" | "rust"
	ServerCmd []string // e.g. ["gopls"]; ServerCmd[0] is the binary
	WorkDir   string   // workspace root
}

// DefaultLSPServers maps a language to its conventional server command.
var DefaultLSPServers = map[string][]string{
	"go":         {"gopls"},
	"python":     {"pylsp"},
	"typescript": {"typescript-language-server", "--stdio"},
	"rust":       {"rust-analyzer"},
}

// Diagnostic is one error/warning reported by the language server.
type Diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`     // 1-based
	Col      int    `json:"col"`      // 1-based
	Severity string `json:"severity"` // "error" | "warning" | "info" | "hint"
	Message  string `json:"message"`
}

// Location is a file position range (go-to-definition / references).
type Location struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

// LSPClient is a JSON-RPC client to a language server subprocess.
type LSPClient struct {
	cfg     LSPConfig
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
	// diagnostics collected from publishDiagnostics notifications, keyed by URI.
	diagMu sync.Mutex
	diags  map[string][]Diagnostic
	closed bool
}

// NewLSPClient starts the language server for cfg and performs the LSP
// initialize handshake. If the server binary is not on PATH it returns
// (nil, nil) — LSP is an optional enhancement, not a hard dependency. The
// caller owns Close.
func NewLSPClient(cfg LSPConfig) (*LSPClient, error) {
	if len(cfg.ServerCmd) == 0 {
		cfg.ServerCmd = DefaultLSPServers[cfg.Language]
	}
	if len(cfg.ServerCmd) == 0 {
		return nil, nil // unknown language, no server
	}
	if _, err := exec.LookPath(cfg.ServerCmd[0]); err != nil {
		return nil, nil // server not installed — graceful no-op
	}

	cmd := exec.Command(cfg.ServerCmd[0], cfg.ServerCmd[1:]...) //nolint:gosec // server cmd is operator/default configured
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("agents: starting LSP server: %w", err)
	}

	c := &LSPClient{
		cfg:     cfg,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		pending: make(map[int]chan json.RawMessage),
		diags:   make(map[string][]Diagnostic),
	}
	go c.readLoop()

	if err := c.initialize(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// initialize performs the LSP initialize/initialized handshake.
func (c *LSPClient) initialize() error {
	root := c.cfg.WorkDir
	if root == "" {
		root = "."
	}
	abs, _ := filepath.Abs(root)
	_, err := c.call("initialize", map[string]any{
		"processId": nil,
		"rootUri":   pathToURI(abs),
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"publishDiagnostics": map[string]any{},
			},
		},
	})
	if err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
}

// Diagnostics opens file in the server and returns the diagnostics it reports.
// It waits briefly for the server's asynchronous publishDiagnostics.
func (c *LSPClient) Diagnostics(file string) ([]Diagnostic, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	uri := pathToURI(abs)
	content, err := readFileString(abs)
	if err != nil {
		return nil, err
	}
	if err := c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": uri, "languageId": c.cfg.Language, "version": 1, "text": content,
		},
	}); err != nil {
		return nil, err
	}

	// Diagnostics arrive asynchronously; poll for up to 2s.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.diagMu.Lock()
		d, ok := c.diags[uri]
		c.diagMu.Unlock()
		if ok {
			return d, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, nil // no diagnostics within the window (clean file or slow server)
}

// Hover returns documentation for the symbol at (line, col) (both 1-based).
func (c *LSPClient) Hover(file string, line, col int) (string, error) {
	uri, err := c.openForQuery(file)
	if err != nil {
		return "", err
	}
	raw, err := c.call("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line - 1, "character": col - 1},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Contents struct {
			Value string `json:"value"`
		} `json:"contents"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Contents.Value, nil
}

// References returns all references to the symbol at (line, col).
func (c *LSPClient) References(file string, line, col int) ([]Location, error) {
	uri, err := c.openForQuery(file)
	if err != nil {
		return nil, err
	}
	raw, err := c.call("textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line - 1, "character": col - 1},
		"context":      map[string]any{"includeDeclaration": true},
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
}

// Definition returns the definition location of the symbol at (line, col).
func (c *LSPClient) Definition(file string, line, col int) (*Location, error) {
	uri, err := c.openForQuery(file)
	if err != nil {
		return nil, err
	}
	raw, err := c.call("textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line - 1, "character": col - 1},
	})
	if err != nil {
		return nil, err
	}
	locs := parseLocations(raw)
	if len(locs) == 0 {
		return nil, nil
	}
	return &locs[0], nil
}

// openForQuery opens a file (idempotent for the server) and returns its URI.
func (c *LSPClient) openForQuery(file string) (string, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	uri := pathToURI(abs)
	content, err := readFileString(abs)
	if err != nil {
		return "", err
	}
	_ = c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": uri, "languageId": c.cfg.Language, "version": 1, "text": content,
		},
	})
	return uri, nil
}

// Close shuts down the language server.
func (c *LSPClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	_ = c.notify("exit", nil)
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
	return nil
}

// --- JSON-RPC plumbing ------------------------------------------------------

// call sends a request and waits for its response result.
func (c *LSPClient) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		return nil, err
	}
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(5 * time.Second):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("agents: LSP %s timed out", method)
	}
}

// notify sends a notification (no response expected).
func (c *LSPClient) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// write frames a JSON-RPC message with the LSP Content-Length header.
func (c *LSPClient) write(msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("agents: LSP client closed")
	}
	_, err = fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

// readLoop reads framed messages and dispatches responses/notifications.
func (c *LSPClient) readLoop() {
	for {
		body, err := readMessage(c.stdout)
		if err != nil {
			return // server closed
		}
		var env struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &env) != nil {
			continue
		}
		switch {
		case env.ID != nil && env.Method == "":
			// Response to a request.
			c.mu.Lock()
			ch, ok := c.pending[*env.ID]
			delete(c.pending, *env.ID)
			c.mu.Unlock()
			if ok {
				ch <- env.Result
			}
		case env.Method == "textDocument/publishDiagnostics":
			c.handleDiagnostics(env.Params)
		}
	}
}

// handleDiagnostics stores a publishDiagnostics notification.
func (c *LSPClient) handleDiagnostics(params json.RawMessage) {
	var p struct {
		URI         string `json:"uri"`
		Diagnostics []struct {
			Range struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
			} `json:"range"`
			Severity int    `json:"severity"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	out := make([]Diagnostic, 0, len(p.Diagnostics))
	for _, d := range p.Diagnostics {
		out = append(out, Diagnostic{
			File:     uriToPath(p.URI),
			Line:     d.Range.Start.Line + 1,
			Col:      d.Range.Start.Character + 1,
			Severity: severityName(d.Severity),
			Message:  d.Message,
		})
	}
	c.diagMu.Lock()
	c.diags[p.URI] = out
	c.diagMu.Unlock()
}

// readMessage reads one Content-Length-framed LSP message body.
func readMessage(r *bufio.Reader) ([]byte, error) {
	contentLen := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			_, _ = fmt.Sscanf(line[len("content-length:"):], "%d", &contentLen)
		}
	}
	if contentLen <= 0 {
		return nil, fmt.Errorf("agents: LSP message missing content length")
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// parseLocations decodes an LSP Location or Location[] result.
func parseLocations(raw json.RawMessage) []Location {
	type lspLoc struct {
		URI   string `json:"uri"`
		Range struct {
			Start struct {
				Line, Character int
			} `json:"start"`
		} `json:"range"`
	}
	var arr []lspLoc
	if json.Unmarshal(raw, &arr) != nil {
		var single lspLoc
		if json.Unmarshal(raw, &single) != nil {
			return nil
		}
		arr = []lspLoc{single}
	}
	out := make([]Location, 0, len(arr))
	for _, l := range arr {
		if l.URI == "" {
			continue
		}
		out = append(out, Location{File: uriToPath(l.URI), Line: l.Range.Start.Line + 1, Col: l.Range.Start.Character + 1})
	}
	return out
}

func severityName(s int) string {
	switch s {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "info"
	}
}

// pathToURI converts an absolute path to a file:// URI.
func pathToURI(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		// Windows drive path (C:/...) → file:///C:/...
		p = "/" + p
	}
	return "file://" + p
}

// uriToPath converts a file:// URI back to a filesystem path. The leading
// slash is only stripped for Windows drive-letter paths (file:///C:/...);
// Unix absolute paths (file:///home/...) keep their leading slash.
func uriToPath(uri string) string {
	p := strings.TrimPrefix(uri, "file://")
	// "/C:/..." → "C:/..."; "/home/..." stays "/home/...".
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// readFileString reads a file into a string.
func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator/agent-supplied workspace path
	if err != nil {
		return "", err
	}
	return string(b), nil
}
