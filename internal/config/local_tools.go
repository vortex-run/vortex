package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveLocalTools validates agents.local_tools against workDir (the coordinator
// working directory) and returns absolute paths for wiring agents.LocalFSConfig.
// trustedRoot is empty when unset; autoMode defaults false from schema.
func (lt LocalTools) ResolveLocalTools(workDir string) (trustedRoot string, autoMode bool, err error) {
	autoMode = lt.AutoMode
	raw := strings.TrimSpace(lt.TrustedRoot)
	if raw == "" {
		if autoMode {
			return "", false, fmt.Errorf("agents.local_tools.auto_mode requires agents.local_tools.trusted_root")
		}
		return "", false, nil
	}

	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		workDir, err = os.Getwd()
		if err != nil {
			return "", false, fmt.Errorf("agents.local_tools.trusted_root: resolve working directory: %w", err)
		}
	}

	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workDir, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", false, fmt.Errorf("agents.local_tools.trusted_root %q: %w", lt.TrustedRoot, err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", false, fmt.Errorf("agents.local_tools.trusted_root %q: %w", lt.TrustedRoot, err)
	}
	if !st.IsDir() {
		return "", false, fmt.Errorf("agents.local_tools.trusted_root %q is not a directory", abs)
	}

	workAbs, err := filepath.Abs(workDir)
	if err != nil {
		return "", false, fmt.Errorf("agents.local_tools.trusted_root: resolve working directory: %w", err)
	}
	// Shell tools use workDir as cwd while FS tools are confined to
	// trusted_root. Require workDir ⊆ trusted_root so the primary cwd is not
	// outside the trust boundary (disjoint or trusted⊂work would mislead).
	if !pathLexicallyContainedIn(abs, workAbs) {
		return "", false, fmt.Errorf("agents.local_tools.trusted_root %q must contain coordinator working directory %q (work dir must nest inside trusted_root)", abs, workAbs)
	}

	return abs, autoMode, nil
}

// pathLexicallyContainedIn reports whether target equals base or lies beneath
// it. filepath.Rel (rather than a raw HasPrefix) avoids sibling prefix traps
// (/var/tr vs /var/trusted) and matches Windows volume/case semantics; it is
// the same rule the agents package applies at runtime (pathWithinBase).
func pathLexicallyContainedIn(base, target string) bool {
	if base == target {
		return true
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
