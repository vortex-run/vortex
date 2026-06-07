package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// GitAuditLogger records destructive Git operations. Satisfied by *audit.Log.
type GitAuditLogger interface {
	Append(ctx context.Context, actor, action, resource string, detail map[string]any) error
}

// GitPanelConfig configures the Git panel.
type GitPanelConfig struct {
	RepoPath string
	AuditLog GitAuditLogger
	Logger   *slog.Logger
}

// GitPanel serves Git operations under /studio/git/ via the git binary.
type GitPanel struct {
	cfg GitPanelConfig
	log *slog.Logger
}

// NewGitPanel constructs the panel. It returns an error if the git binary is
// missing or RepoPath is not a git repository.
func NewGitPanel(cfg GitPanelConfig) (*GitPanel, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("studio: git binary not found: %w", err)
	}
	gp := &GitPanel{cfg: cfg, log: cfg.Logger}
	if _, err := gp.git(context.Background(), "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("studio: %q is not a git repository: %w", cfg.RepoPath, err)
	}
	return gp, nil
}

// git runs a git command in the repo directory and returns trimmed stdout.
func (g *GitPanel) git(ctx context.Context, args ...string) (string, error) {
	out, err := g.gitRaw(ctx, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is like git but does not trim stdout — used for porcelain output where
// the leading status columns (which can be spaces) are significant.
func (g *GitPanel) gitRaw(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.cfg.RepoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Handler returns the Git panel HTTP handler.
func (g *GitPanel) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /studio/git/status", g.handleStatus)
	mux.HandleFunc("GET /studio/git/log", g.handleLog)
	mux.HandleFunc("GET /studio/git/diff", g.handleDiff)
	mux.HandleFunc("POST /studio/git/commit", g.handleCommit)
	mux.HandleFunc("POST /studio/git/push", g.handlePush)
	return mux
}

// handleStatus returns the branch, clean flag, and staged/unstaged file lists.
func (g *GitPanel) handleStatus(w http.ResponseWriter, r *http.Request) {
	branch, err := g.git(r.Context(), "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out, err := g.gitRaw(r.Context(), "status", "--porcelain")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	staged, unstaged := parsePorcelain(out)
	writeJSON(w, http.StatusOK, map[string]any{
		"branch":   branch,
		"clean":    strings.TrimSpace(out) == "",
		"staged":   staged,
		"unstaged": unstaged,
	})
}

// parsePorcelain splits `git status --porcelain` output into staged (index) and
// unstaged (work-tree) file lists.
func parsePorcelain(out string) (staged, unstaged []string) {
	staged, unstaged = []string{}, []string{}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 3 {
			continue
		}
		x, y, path := line[0], line[1], strings.TrimSpace(line[3:])
		if x != ' ' && x != '?' {
			staged = append(staged, path)
		}
		if y != ' ' {
			unstaged = append(unstaged, path)
		}
	}
	return staged, unstaged
}

// gitCommit is one entry from `git log --oneline`.
type gitCommit struct {
	Hash    string `json:"hash"`
	Message string `json:"message"`
}

// handleLog returns recent commits (limit via ?limit=, default 20).
func (g *GitPanel) handleLog(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	out, err := g.git(r.Context(), "log", "--oneline", "-n", strconv.Itoa(limit))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	commits := []gitCommit{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		c := gitCommit{Hash: parts[0]}
		if len(parts) > 1 {
			c.Message = parts[1]
		}
		commits = append(commits, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"commits": commits})
}

// handleDiff returns the diff for a file (?file=) or the whole working tree.
func (g *GitPanel) handleDiff(w http.ResponseWriter, r *http.Request) {
	args := []string{"diff"}
	if f := r.URL.Query().Get("file"); f != "" {
		args = append(args, "--", f)
	}
	out, err := g.git(r.Context(), args...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"diff": out})
}

// commitRequest is the POST /studio/git/commit body.
type commitRequest struct {
	Message string   `json:"message"`
	Files   []string `json:"files"`
}

// handleCommit stages the given files and commits with the message. It is a
// destructive operation, so it is audit-logged.
func (g *GitPanel) handleCommit(w http.ResponseWriter, r *http.Request) {
	var req commitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "commit message required"})
		return
	}
	addArgs := append([]string{"add", "--"}, req.Files...)
	if len(req.Files) == 0 {
		addArgs = []string{"add", "-A"}
	}
	if _, err := g.git(r.Context(), addArgs...); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if _, err := g.git(r.Context(), "commit", "-m", req.Message); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	g.audit(r.Context(), "studio.git.commit", req.Message)
	hash, _ := g.git(r.Context(), "rev-parse", "--short", "HEAD")
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "hash": hash})
}

// handlePush pushes the current branch to origin. Destructive → audit-logged.
func (g *GitPanel) handlePush(w http.ResponseWriter, r *http.Request) {
	branch, err := g.git(r.Context(), "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out, err := g.git(r.Context(), "push", "origin", branch)
	if err != nil {
		g.audit(r.Context(), "studio.git.push.failed", branch)
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "output": err.Error()})
		return
	}
	g.audit(r.Context(), "studio.git.push", branch)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "output": out})
}

// audit records a destructive git operation.
func (g *GitPanel) audit(ctx context.Context, action, detail string) {
	if g.cfg.AuditLog == nil {
		return
	}
	_ = g.cfg.AuditLog.Append(ctx, "studio", action, g.cfg.RepoPath, map[string]any{"detail": detail})
}
