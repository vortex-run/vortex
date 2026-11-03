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
	"runtime"
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
	Port         int    // Windows only: loopback port (default: a free one)
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
//
// code-server runs with --auth none (VORTEX authenticates /studio/), so
// whoever can connect to it gets an IDE and a shell. On Linux and macOS it
// therefore listens on a Unix socket in a fresh 0700 directory, which only
// this user can reach. A loopback TCP port would be open to every local user
// and to anything VORTEX itself forwards there: with the old fixed :8080, a
// route whose backend is 127.0.0.1:8080 (the README example) published the
// IDE to the internet. On Windows, where Node can't listen on a socket path,
// it falls back to a free loopback port.
type CodeServer struct {
	cfg       CodeServerConfig
	log       *slog.Logger
	addr      string // 127.0.0.1:<port>, when socket is empty
	socket    string // Unix socket path
	socketDir string // removed on Stop

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
	return &CodeServer{cfg: cfg, log: cfg.Logger}, nil
}

// Start launches the code-server subprocess and waits (up to 10s) for it to
// accept connections.
func (c *CodeServer) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		return nil // already running
	}
	if err := c.chooseListener(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, c.cfg.BinaryPath, c.args()...)
	cmd.Stdout = logWriter{c.log, "code-server"}
	cmd.Stderr = logWriter{c.log, "code-server"}
	if err := cmd.Start(); err != nil {
		c.removeSocketDir()
		return fmt.Errorf("studio: starting code-server: %w", err)
	}
	c.cmd = cmd

	if err := c.waitReady(10 * time.Second); err != nil {
		_ = c.stopLocked()
		return fmt.Errorf("studio: code-server did not become ready: %w", err)
	}
	c.log.Info("code-server started", "listen", c.listenDesc(), "workspace", c.cfg.WorkspaceDir)
	return nil
}

// chooseListener picks where code-server listens: a Unix socket in a new
// private directory, or (Windows) a free loopback port.
func (c *CodeServer) chooseListener() error {
	if runtime.GOOS != "windows" {
		dir, err := os.MkdirTemp("", "vortex-code-server-") // 0700
		if err != nil {
			return fmt.Errorf("studio: code-server socket dir: %w", err)
		}
		c.socketDir = dir
		c.socket = filepath.Join(dir, "code-server.sock")
		return nil
	}
	port := c.cfg.Port
	if port == 0 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("studio: picking a code-server port: %w", err)
		}
		port = l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
	}
	c.addr = fmt.Sprintf("127.0.0.1:%d", port)
	return nil
}

// args returns code-server's command line for the chosen listener.
func (c *CodeServer) args() []string {
	var args []string
	if c.socket != "" {
		args = []string{"--socket", c.socket}
	} else {
		args = []string{"--bind-addr", c.addr}
	}
	args = append(args, "--auth", "none")
	if c.cfg.DataDir != "" {
		args = append(args, "--user-data-dir", c.cfg.DataDir)
	}
	if c.cfg.WorkspaceDir != "" {
		args = append(args, c.cfg.WorkspaceDir)
	}
	return args
}

// listenDesc names the listener for logs.
func (c *CodeServer) listenDesc() string {
	if c.socket != "" {
		return "unix:" + c.socket
	}
	return c.addr
}

// dial connects to code-server's listener.
func (c *CodeServer) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	if c.socket != "" {
		return d.DialContext(ctx, "unix", c.socket)
	}
	return d.DialContext(ctx, "tcp", c.addr)
}

// removeSocketDir deletes the private socket directory, if any.
func (c *CodeServer) removeSocketDir() {
	if c.socketDir != "" {
		_ = os.RemoveAll(c.socketDir)
		c.socketDir, c.socket = "", ""
	}
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
	c.removeSocketDir()
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
	// The URL host is only a label: every connection goes through dial.
	target := &url.URL{Scheme: "http", Host: "code-server"}
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		// SetURL sets Host to the target's; keep the client's, which
		// code-server's WebSocket origin check compares against.
		pr.Out.Host = pr.In.Host
		pr.SetXForwarded()
		// The caller's VORTEX key is not code-server's business (or its
		// extensions').
		pr.Out.Header.Del("Authorization")
		pr.Out.Header.Del("X-API-Key")
	}}
	// No Proxy field: HTTP_PROXY must not apply to a local backend.
	proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return c.dial(ctx)
		},
		MaxIdleConns:    16,
		IdleConnTimeout: 90 * time.Second,
	}
	// httputil handles Connection/Upgrade headers for WebSockets.
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		c.log.Warn("code-server proxy error", "err", err)
		http.Error(w, "code-server unavailable", http.StatusBadGateway)
	}
	return proxy
}

// waitReady dials code-server until it accepts a connection or the timeout
// elapses.
func (c *CodeServer) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		conn, err := c.dial(ctx)
		cancel()
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", c.listenDesc())
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
