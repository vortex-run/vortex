package agents

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Git tools give the agent read access (status/diff) and approval-gated write
// access (add/commit) to the working-directory git repo. They reuse
// LocalFSConfig.Root as the repo directory.

// gitTimeout bounds a single git invocation.
const gitTimeout = 30 * time.Second

// runGit runs `git args...` in dir and returns combined trimmed stdout (stderr
// folded into the error on failure).
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// strSliceParam extracts a []string param (accepts []string or []any of strings).
func strSliceParam(params map[string]any, key string) []string {
	switch v := params[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// --- GitStatusTool (read-only) ----------------------------------------------

// GitStatusTool reports the repo status. Read-only, no approval.
type GitStatusTool struct{ cfg LocalFSConfig }

// Name returns the tool name.
func (GitStatusTool) Name() string { return "git_status" }

// Description returns a human-readable summary.
func (GitStatusTool) Description() string { return "Show git status" }

// Execute runs `git status --porcelain` and the current branch.
func (t GitStatusTool) Execute(ctx context.Context, _ map[string]any) (any, error) {
	dir := t.cfg.Root
	branch, _ := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	porcelain, err := runGit(ctx, dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var staged, unstaged, untracked []string
	for _, line := range strings.Split(porcelain, "\n") {
		if len(line) < 3 {
			continue
		}
		x, y, path := line[0], line[1], strings.TrimSpace(line[3:])
		switch {
		case x == '?' && y == '?':
			untracked = append(untracked, path)
		default:
			if x != ' ' && x != '?' {
				staged = append(staged, path)
			}
			if y != ' ' && y != '?' {
				unstaged = append(unstaged, path)
			}
		}
	}
	return map[string]any{
		"branch":    branch,
		"clean":     strings.TrimSpace(porcelain) == "",
		"staged":    staged,
		"unstaged":  unstaged,
		"untracked": untracked,
	}, nil
}

// --- GitDiffTool (read-only) ------------------------------------------------

// GitDiffTool shows the working-tree diff. Read-only, no approval.
type GitDiffTool struct{ cfg LocalFSConfig }

// Name returns the tool name.
func (GitDiffTool) Name() string { return "git_diff" }

// Description returns a human-readable summary.
func (GitDiffTool) Description() string { return "Show git diff" }

// Execute runs `git diff [file]`.
func (t GitDiffTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	args := []string{"diff"}
	if file, _ := params["file"].(string); file != "" {
		args = append(args, "--", file)
	}
	diff, err := runGit(ctx, t.cfg.Root, args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"diff": diff}, nil
}

// --- GitAddTool (approval) --------------------------------------------------

// GitAddTool stages files. Requires approval.
type GitAddTool struct {
	cfg             LocalFSConfig
	RequireApproval bool
}

// Name returns the tool name.
func (GitAddTool) Name() string { return "git_add" }

// Description returns a human-readable summary.
func (GitAddTool) Description() string { return "Stage files (approval required)" }

// Execute runs `git add files...`.
func (t GitAddTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	files := strSliceParam(params, "files")
	if len(files) == 0 {
		return nil, fmt.Errorf("agents: git_add requires files")
	}
	if t.RequireApproval {
		return nil, &ApprovalError{Request: ApprovalRequest{
			Tool:        t.Name(),
			Description: "git add " + strings.Join(files, " "),
			Preview:     "git add " + strings.Join(files, " "),
			Params:      params,
		}}
	}
	if _, err := runGit(ctx, t.cfg.Root, append([]string{"add"}, files...)...); err != nil {
		return nil, err
	}
	return map[string]any{"staged": files}, nil
}

// --- GitCommitTool (approval) -----------------------------------------------

// GitCommitTool stages + commits. Requires approval.
type GitCommitTool struct {
	cfg             LocalFSConfig
	RequireApproval bool
}

// Name returns the tool name.
func (GitCommitTool) Name() string { return "git_commit" }

// Description returns a human-readable summary.
func (GitCommitTool) Description() string { return "Commit changes (approval required)" }

// Execute stages params["files"] (if any) then commits with params["message"].
func (t GitCommitTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	message, err := strParam(params, "message")
	if err != nil {
		return nil, err
	}
	files := strSliceParam(params, "files")
	if t.RequireApproval {
		preview := "git commit -m " + fmt.Sprintf("%q", message)
		if len(files) > 0 {
			preview = "git add " + strings.Join(files, " ") + "\n" + preview
		}
		return nil, &ApprovalError{Request: ApprovalRequest{
			Tool:        t.Name(),
			Description: "commit: " + message,
			Preview:     preview,
			Params:      params,
		}}
	}
	if len(files) > 0 {
		if _, aerr := runGit(ctx, t.cfg.Root, append([]string{"add"}, files...)...); aerr != nil {
			return nil, aerr
		}
	}
	out, err := runGit(ctx, t.cfg.Root, "commit", "-m", message)
	if err != nil {
		return nil, err
	}
	return map[string]any{"message": message, "output": out}, nil
}

// gitTools returns the git toolset bound to cfg (write tools approval-gated).
func gitTools(cfg LocalFSConfig) []Tool {
	return []Tool{
		GitStatusTool{cfg: cfg},
		GitDiffTool{cfg: cfg},
		GitAddTool{cfg: cfg, RequireApproval: true},
		GitCommitTool{cfg: cfg, RequireApproval: true},
	}
}
