// Package studio implements VORTEX Studio (build plan M12): a browser-based
// IDE and operations console served from the binary. It manages an external
// code-server (VS Code in the browser) process, a WebSocket terminal, a browser
// database GUI, and a Git panel — all mounted under /studio/ by the management
// API.
//
// This file manages the code-server subprocess lifecycle. code-server is a
// separate binary (not embedded); when it is not installed, Studio degrades
// gracefully rather than failing the whole server.
package studio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ErrCodeServerNotInstalled indicates the code-server binary was not found in
// any known location. It is non-fatal: Studio runs without the IDE panel.
var ErrCodeServerNotInstalled = errors.New("studio: code-server binary not installed")

// CodeServerConfig configures the code-server lifecycle manager.
type CodeServerConfig struct {
	BinaryPath   string // path to code-server; auto-detected when empty
	WorkspaceDir string // directory to open
	Port         int    // internal bind port (default 8080)
	Auth         string // "none" — VORTEX handles auth at the edge
	CertFile     string // optional; VORTEX serves TLS, code-server stays plaintext
	DataDir      string // code-server --user-data-dir
	Logger       *slog.Logger
}

// commonCodeServerPaths are the locations probed when BinaryPath is empty.
var commonCodeServerPaths = []string{
	"/usr/lib/code-server/bin/code-server",
	"/usr/local/bin/code-server",
}

// CodeServer manages a code-server subprocess and proxies HTTP/WebSocket traffic
// to it.
type CodeServer struct {
	cfg  CodeServerConfig
	log  *slog.Logger
	addr string // 127.0.0.1:<port>

	mu  sync.Mutex
	cmd *exec.Cmd
}

// resolveBinary returns the code-server binary path, probing common locations
// (and ~/.local/bin) when cfg.BinaryPath is empty. It returns
// ErrCodeServerNotInstalled when none is found.
func resolveBinary(cfg CodeServerConfig) (string, error) {
	if cfg.BinaryPath != "" {
		if _, err := os.Stat(cfg.BinaryPath); err != nil {
			return "", fmt.Errorf("%w: %s", ErrCodeServerNotInstalled, cfg.BinaryPath)
		}
		return cfg.BinaryPath, nil
	}
	candidates := append([]string{}, commonCodeServerPaths...)
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "code-server"))
	}
	// Also honour PATH.
	if p, err := exec.LookPath("code-server"); err == nil {
		candidates = append([]string{p}, candidates...)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", ErrCodeServerNotInstalled
}

// NewCodeServer constructs a manager. It returns ErrCodeServerNotInstalled when
// the binary is missing (callers treat this as a non-fatal degraded mode), or
// an error when the workspace directory does not exist.
func NewCodeServer(cfg CodeServerConfig) (*CodeServer, error) {
	if cfg.Port == 0 {
		cfg.Port = 8080
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	bin, err := resolveBinary(cfg)
	if err != nil {
		return nil, err
	}
	cfg.BinaryPath = bin
	if cfg.WorkspaceDir != "" {
		if _, serr := os.Stat(cfg.WorkspaceDir); serr != nil {
			return nil, fmt.Errorf("studio: workspace dir not found: %w", serr)
		}
	}
	return &CodeServer{
		cfg:  cfg,
		log:  cfg.Logger,
		addr: fmt.Sprintf("127.0.0.1:%d", cfg.Port),
	}, nil
}

// Start launches the code-server subprocess and waits (up to 10s) for its port
// to accept connections.
func (c *CodeServer) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		return nil // already running
	}
	args := []string{
		"--bind-addr", c.addr,
		"--auth", "none",
	}
	if c.cfg.DataDir != "" {
		args = append(args, "--user-data-dir", c.cfg.DataDir)
	}
	if c.cfg.WorkspaceDir != "" {
		args = append(args, c.cfg.WorkspaceDir)
	}
	cmd := exec.CommandContext(ctx, c.cfg.BinaryPath, args...)
	cmd.Stdout = logWriter{c.log, "code-server"}
	cmd.Stderr = logWriter{c.log, "code-server"}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("studio: starting code-server: %w", err)
	}
	c.cmd = cmd

	if err := waitForPort(c.addr, 10*time.Second); err != nil {
		_ = c.stopLocked()
		return fmt.Errorf("studio: code-server did not become ready: %w", err)
	}
	c.log.Info("code-server started", "addr", c.addr, "workspace", c.cfg.WorkspaceDir)
	return nil
}

// Stop sends SIGTERM (or the platform equivalent via Process.Kill on Windows)
// and waits up to 10s for the process to exit, force-killing if needed.
func (c *CodeServer) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopLocked()
}

// stopLocked stops the process; the caller holds c.mu.
func (c *CodeServer) stopLocked() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	proc := c.cmd.Process
	done := make(chan struct{})
	go func() {
		_, _ = c.cmd.Process.Wait()
		close(done)
	}()
	_ = proc.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = proc.Kill()
	}
	c.cmd = nil
	return nil
}

// IsRunning reports whether the subprocess is currently running.
func (c *CodeServer) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd != nil
}

// ProxyHandler returns an http.Handler that reverse-proxies all requests to the
// code-server backend, preserving WebSocket upgrades (used by code-server for
// live updates).
func (c *CodeServer) ProxyHandler() http.Handler {
	target := &url.URL{Scheme: "http", Host: c.addr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// The default director preserves the path; httputil already handles
	// Connection/Upgrade headers for WebSockets in Go 1.12+.
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		c.log.Warn("code-server proxy error", "err", err)
		http.Error(w, "code-server unavailable", http.StatusBadGateway)
	}
	return proxy
}

// waitForPort dials addr until it accepts a connection or the timeout elapses.
func waitForPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}

// logWriter adapts a slog.Logger to an io.Writer for subprocess output.
type logWriter struct {
	log    *slog.Logger
	source string
}

func (w logWriter) Write(p []byte) (int, error) {
	w.log.Debug("subprocess output", "source", w.source, "line", string(p))
	return len(p), nil
}
