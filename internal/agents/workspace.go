package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrWorkspaceEscape is returned when a local-tool path resolves outside the
// configured TrustedRoot. Auto Mode never expands this boundary; only an
// explicit TrustedRoot (or no trust root) defines the allowed scope.
var ErrWorkspaceEscape = fmt.Errorf("agents: path escapes trusted workspace")

// LocalFSConfig fields for workspace trust are documented on the struct in
// local_tools.go (TrustedRoot, AutoMode).

// containInTrustedRoot resolves abs and ensures it stays within TrustedRoot
// after symlink evaluation (same containment model as sandboxResolve). An empty
// TrustedRoot disables this check — callers that need confinement must set it.
func containInTrustedRoot(trustedRoot, abs string) (string, error) {
	if trustedRoot == "" {
		return abs, nil
	}
	base, err := filepath.Abs(trustedRoot)
	if err != nil {
		return "", fmt.Errorf("agents: resolve trusted root: %w", err)
	}
	target, err := filepath.Abs(abs)
	if err != nil {
		return "", err
	}

	// Lexical containment before symlink resolution (blocks ../ and prefix traps).
	// IsLocal also rejects reserved Windows device names such as NUL. Rebuilding
	// target from the checked relative part means nothing below can touch the
	// unvalidated input.
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("agents: resolve trusted root symlinks: %w", err)
	}
	// The target may be written against the root as configured or against
	// its real path (this function returns the real form, and callers pass
	// such results back in). Either is fine lexically; the symlink checks
	// below are always made against realBase.
	rel, err := filepath.Rel(base, target)
	if err == nil && filepath.IsLocal(rel) {
		target = filepath.Join(base, rel)
	} else if rel, err = filepath.Rel(realBase, target); err == nil && filepath.IsLocal(rel) {
		target = filepath.Join(realBase, rel)
	} else {
		return "", fmt.Errorf("%w: %q is outside trusted workspace %q", ErrWorkspaceEscape, target, base)
	}

	if _, lerr := os.Lstat(target); lerr == nil {
		resolved, rerr := filepath.EvalSymlinks(target)
		if rerr != nil {
			return "", fmt.Errorf("agents: resolve path symlinks: %w", rerr)
		}
		if !pathWithinBase(resolved, realBase) {
			return "", fmt.Errorf("%w: %q resolves outside trusted workspace", ErrWorkspaceEscape, abs)
		}
		return resolved, nil
	}

	// Non-existent target (a file about to be created): walk up to the
	// nearest EXISTING ancestor and require that it resolves inside the
	// trusted root. Checking only the immediate parent is not enough — with
	// create_dirs, `<root>/link/newdir/f.txt` (newdir absent, link → outside)
	// would pass because Lstat(parent) fails, and MkdirAll would then follow
	// the symlink out of the workspace. base exists (EvalSymlinks succeeded)
	// and target is lexically inside it, so this loop always stops at or
	// below base.
	anc := filepath.Dir(target)
	for {
		if _, lerr := os.Lstat(anc); lerr == nil {
			realAnc, rerr := filepath.EvalSymlinks(anc)
			if rerr != nil {
				return "", fmt.Errorf("agents: resolve parent symlinks: %w", rerr)
			}
			if !pathWithinBase(realAnc, realBase) {
				return "", fmt.Errorf("%w: parent of %q resolves outside trusted workspace", ErrWorkspaceEscape, abs)
			}
			// Return the resolved form, as for an existing target, so callers
			// always get a path under the real root. When the root itself is
			// reached through a link or an alias (macOS /var -> /private/var,
			// a Windows 8.3 short name such as RUNNER~1), the lexical target
			// would not be under realBase. Found by the macOS/Windows CI jobs.
			if remainder, err := filepath.Rel(anc, target); err == nil && filepath.IsLocal(remainder) {
				return filepath.Join(realAnc, remainder), nil
			}
			break
		}
		next := filepath.Dir(anc)
		if next == anc {
			break // reached the volume root
		}
		anc = next
	}
	return target, nil
}

// pathWithinBase reports whether path is base or lies strictly beneath it.
// filepath.Rel avoids naive HasPrefix traps (e.g. /var/tr vs /var/trusted)
// and gives the platform semantics we want for free: on Windows it compares
// case-insensitively (EqualFold) and refuses to relate paths on different
// volumes (C:\ vs D:\, \\srv\a vs \\srv\b, \\?\-prefixed vs plain), which
// here means "deny". Trailing separators and `.` segments are cleaned away
// (rel == "." counts as inside). Both arguments are expected to be absolute.
func pathWithinBase(path, base string) bool {
	if path == base {
		return true
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// waivesApproval reports whether Auto Mode may skip a per-operation approval
// prompt for a path already verified inside TrustedRoot. Release builds still
// force approval via approvalAlwaysRequired on shell tools; this helper only
// covers the LocalFS Auto Mode waiver for routine in-scope file mutations.
func (cfg LocalFSConfig) waivesApproval(abs string) bool {
	if !cfg.AutoMode || cfg.TrustedRoot == "" {
		return false
	}
	resolved, err := containInTrustedRoot(cfg.TrustedRoot, abs)
	return err == nil && !autoModeSensitive(cfg.TrustedRoot, resolveExistingPrefix(resolved))
}

// resolveExistingPrefix evaluates symlinks in the part of p that exists and
// keeps the rest as written. A file about to be created stays unresolved in
// containInTrustedRoot, which would hide a link in its parent directories.
func resolveExistingPrefix(p string) string {
	rest := ""
	for dir := p; ; dir = filepath.Dir(dir) {
		if evaluated, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(evaluated, rest)
		}
		if filepath.Dir(dir) == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// autoModeSensitiveDirs hold files that run as code on a later, unrelated
// action. A git hook written without approval would run on the next approved
// `git commit`, bypassing the shell gate Auto Mode promises to keep. GIT~1 is
// the Windows short name for .git.
var autoModeSensitiveDirs = map[string]bool{
	".git": true, "git~1": true, ".githooks": true, ".husky": true,
}

// autoModeSensitive reports whether a path inside trustedRoot must keep its
// approval prompt under Auto Mode: hook directories, and gubern.cue, which
// could widen trusted_root or auto_mode on the next restart. Paths it cannot
// relate to the root count as sensitive.
func autoModeSensitive(trustedRoot, resolved string) bool {
	rel, ok := relToTrustedRoot(trustedRoot, resolved)
	if !ok {
		return true
	}
	return sensitiveParts(strings.Split(filepath.ToSlash(rel), "/"))
}

// sensitiveParts reports whether a path, split into components, names a hook
// directory anywhere or ends in gubern.cue / vortex.cue.
func sensitiveParts(parts []string) bool {
	for i, part := range parts {
		// Windows ignores trailing dots and spaces, and ".git::$INDEX_ALLOCATION"
		// names the .git directory itself.
		part, _, _ = strings.Cut(part, ":")
		part = strings.ToLower(strings.TrimRight(part, ". "))
		if autoModeSensitiveDirs[part] || (i == len(parts)-1 && (part == "gubern.cue" || part == "vortex.cue")) {
			return true
		}
	}
	return false
}

// needsApproval is the single approval decision for mutating local tools.
// toolRequires is the tool's own RequireApproval; Auto Mode may waive it for
// routine in-scope paths. Independently, GateSensitive (set for the approval-
// free team specialists) keeps a prompt on paths that run as code on a later
// action — .git (config, hooks), hook dirs, gubern.cue — so a specialist
// cannot plant core.fsmonitor or a hook that git then executes with no shell
// approval (audit F-05).
func (cfg LocalFSConfig) needsApproval(toolRequires bool, abs string) bool {
	if toolRequires && !cfg.waivesApproval(abs) {
		return true
	}
	return cfg.GateSensitive && pathIsSensitive(abs)
}

// pathIsSensitive checks every component of abs, both as written and with
// existing symlinks resolved, so a link into .git counts as .git.
func pathIsSensitive(abs string) bool {
	for _, p := range []string{abs, resolveExistingPrefix(abs)} {
		if sensitiveParts(strings.Split(filepath.ToSlash(filepath.Clean(p)), "/")) {
			return true
		}
	}
	return false
}

// relToTrustedRoot returns resolved relative to the root, trying the root as
// configured and with symlinks evaluated, since containInTrustedRoot can return
// either form.
func relToTrustedRoot(trustedRoot, resolved string) (string, bool) {
	base, err := filepath.Abs(trustedRoot)
	if err != nil {
		return "", false
	}
	if rel, err := filepath.Rel(base, resolved); err == nil && filepath.IsLocal(rel) {
		return rel, true
	}
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", false
	}
	if rel, err := filepath.Rel(realBase, resolved); err == nil && filepath.IsLocal(rel) {
		return rel, true
	}
	return "", false
}
