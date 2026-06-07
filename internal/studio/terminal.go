package studio

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// wsAcceptMagic is the RFC 6455 GUID used to compute Sec-WebSocket-Accept.
const wsAcceptMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// TerminalAuditLogger records terminal session lifecycle events. Satisfied by
// *audit.Log; kept as an interface so this package stays decoupled.
type TerminalAuditLogger interface {
	Append(ctx context.Context, actor, action, resource string, detail map[string]any) error
}

// TerminalConfig configures the browser terminal manager.
type TerminalConfig struct {
	Shell       string        // default: /bin/bash (Unix) or cmd.exe (Windows)
	WorkDir     string        // working directory for spawned shells
	MaxSessions int           // default 5
	IdleTimeout time.Duration // default 30m
	AuditLog    TerminalAuditLogger
	Logger      *slog.Logger
	// Authorize, when set, gates new sessions; it returns true to allow. When
	// nil, the manager allows any request that reaches it (the API layer is
	// expected to enforce auth before delegating here).
	Authorize func(r *http.Request) bool
}

// TerminalSession is one active shell session bound to a WebSocket.
type TerminalSession struct {
	ID        string
	StartedAt time.Time
	LastUsed  time.Time
	cmd       *exec.Cmd
}

// TerminalManager serves browser terminals over WebSocket.
type TerminalManager struct {
	cfg TerminalConfig
	log *slog.Logger

	mu       sync.Mutex
	sessions map[string]*TerminalSession
}

// NewTerminalManager constructs a manager with defaults applied.
func NewTerminalManager(cfg TerminalConfig) *TerminalManager {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 5
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	if cfg.Shell == "" {
		if runtime.GOOS == "windows" {
			cfg.Shell = "cmd.exe"
		} else {
			cfg.Shell = "/bin/bash"
		}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &TerminalManager{cfg: cfg, log: cfg.Logger, sessions: make(map[string]*TerminalSession)}
}

// SessionCount returns the number of active sessions.
func (m *TerminalManager) SessionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Handler returns the WebSocket terminal endpoint. It authenticates (when an
// Authorize hook is set), enforces the session cap, performs the WebSocket
// handshake, and pipes the WebSocket to a shell subprocess.
func (m *TerminalManager) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.cfg.Authorize != nil && !m.cfg.Authorize(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !isWebSocketUpgrade(r) {
			http.Error(w, "expected websocket upgrade", http.StatusBadRequest)
			return
		}

		m.mu.Lock()
		if len(m.sessions) >= m.cfg.MaxSessions {
			m.mu.Unlock()
			http.Error(w, "too many terminal sessions", http.StatusServiceUnavailable)
			return
		}
		m.mu.Unlock()

		conn, err := acceptWebSocket(w, r)
		if err != nil {
			m.log.Warn("terminal websocket accept failed", "err", err)
			return
		}
		defer func() { _ = conn.Close() }()

		m.serveSession(r.Context(), conn)
	})
}

// serveSession spawns a shell and relays bytes between the WebSocket and the
// shell until either side closes or the idle timeout elapses.
func (m *TerminalManager) serveSession(ctx context.Context, conn *wsConn) {
	id := newSessionID()
	cmd := exec.CommandContext(ctx, m.cfg.Shell)
	cmd.Dir = m.cfg.WorkDir
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout

	sess := &TerminalSession{ID: id, StartedAt: time.Now(), LastUsed: time.Now(), cmd: cmd}
	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()
	m.audit(ctx, "studio.terminal.start", id)
	m.log.Info("terminal session started", "id", id, "shell", m.cfg.Shell)

	defer func() {
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		m.audit(ctx, "studio.terminal.end", id)
		m.log.Info("terminal session ended", "id", id)
	}()

	if err := cmd.Start(); err != nil {
		_ = conn.WriteText([]byte("failed to start shell: " + err.Error()))
		return
	}

	// shell stdout → websocket
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if werr := conn.WriteText(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// websocket → shell stdin, with idle timeout
	for {
		_ = conn.SetReadDeadline(time.Now().Add(m.cfg.IdleTimeout))
		data, err := conn.ReadText()
		if err != nil {
			return
		}
		sess.LastUsed = time.Now()
		if _, err := stdin.Write(data); err != nil {
			return
		}
	}
}

// CloseSessions terminates all active terminal sessions.
func (m *TerminalManager) CloseSessions() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		delete(m.sessions, id)
	}
	return nil
}

// audit records a session event when an audit log is configured.
func (m *TerminalManager) audit(ctx context.Context, action, id string) {
	if m.cfg.AuditLog == nil {
		return
	}
	_ = m.cfg.AuditLog.Append(ctx, "studio", action, id, nil)
}

// --- minimal RFC 6455 server framing (text frames only) --------------------

// isWebSocketUpgrade reports whether r is a WebSocket upgrade request.
func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Key") == "" {
		return false
	}
	for _, tok := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
			return true
		}
	}
	return false
}

// acceptWebSocket completes the server handshake and returns a framed conn.
func acceptWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack support", http.StatusInternalServerError)
		return nil, errors.New("studio: ResponseWriter is not http.Hijacker")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	accept := computeAcceptKey(key)

	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &wsConn{conn: conn, rw: rw}, nil
}

// computeAcceptKey computes the Sec-WebSocket-Accept response value.
func computeAcceptKey(key string) string {
	h := sha1.New() //nolint:gosec // RFC 6455 mandates SHA-1 for the accept key
	_, _ = io.WriteString(h, key+wsAcceptMagic)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// newSessionID returns a random hex session id.
func newSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("term-%x", b)
}
