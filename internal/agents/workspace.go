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
	if !pathWithinBase(target, base) {
		return "", fmt.Errorf("%w: %q is outside trusted workspace %q", ErrWorkspaceEscape, target, base)
	}

	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("agents: resolve trusted root symlinks: %w", err)
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
	_, err := containInTrustedRoot(cfg.TrustedRoot, abs)
	return err == nil
}
