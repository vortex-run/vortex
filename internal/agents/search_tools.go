package agents

import (
	"bufio"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Search/find tools give the agent read-only code navigation: search_files
// (grep-like content search) and find_files (name/glob search). Both walk under
// LocalFSConfig.Root (or an explicit path) and need no approval.

// maxSearchResults caps search_files output.
const maxSearchResults = 100

// SearchFilesTool searches file contents for a pattern. Read-only, no approval.
type SearchFilesTool struct{ cfg LocalFSConfig }

// Name returns the tool name.
func (SearchFilesTool) Name() string { return "search_files" }

// Description returns a human-readable summary.
func (SearchFilesTool) Description() string { return "Search file contents for a pattern" }

// Execute walks params["path"] (default Root) and returns matches for
// params["pattern"], optionally filtered by params["extension"] and
// params["case_sensitive"].
func (t SearchFilesTool) Execute(_ context.Context, params map[string]any) (any, error) {
	pattern, err := strParam(params, "pattern")
	if err != nil {
		return nil, err
	}
	root, err := t.cfg.resolveLocal(strParamOr(params, "path", "."))
	if err != nil {
		return nil, err
	}
	ext := strings.TrimPrefix(strParamOr(params, "extension", ""), ".")
	caseSensitive, _ := params["case_sensitive"].(bool)
	needle := pattern
	if !caseSensitive {
		needle = strings.ToLower(pattern)
	}

	var results []map[string]any
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		if skipDir(path) {
			return nil
		}
		if ext != "" && !strings.EqualFold(strings.TrimPrefix(filepath.Ext(path), "."), ext) {
			return nil
		}
		f, oerr := os.Open(path) //nolint:gosec // user-approved local read
		if oerr != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		ln := 0
		for sc.Scan() {
			ln++
			line := sc.Text()
			hay := line
			if !caseSensitive {
				hay = strings.ToLower(line)
			}
			if strings.Contains(hay, needle) {
				results = append(results, map[string]any{
					"file": path, "line_number": ln, "line_content": strings.TrimSpace(line),
				})
				if len(results) >= maxSearchResults {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != fs.SkipAll {
		return nil, walkErr
	}
	return map[string]any{"matches": results, "count": len(results)}, nil
}

// FindFilesTool finds files by name/glob. Read-only, no approval.
type FindFilesTool struct{ cfg LocalFSConfig }

// Name returns the tool name.
func (FindFilesTool) Name() string { return "find_files" }

// Description returns a human-readable summary.
func (FindFilesTool) Description() string { return "Find files by name pattern" }

// Execute walks params["path"] (default Root) returning files whose base name
// matches params["name_pattern"] (glob).
func (t FindFilesTool) Execute(_ context.Context, params map[string]any) (any, error) {
	namePattern, err := strParam(params, "name_pattern")
	if err != nil {
		return nil, err
	}
	root, err := t.cfg.resolveLocal(strParamOr(params, "path", "."))
	if err != nil {
		return nil, err
	}
	var matches []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() || skipDir(path) {
			return nil
		}
		if ok, _ := filepath.Match(namePattern, d.Name()); ok {
			matches = append(matches, path)
			if len(matches) >= maxSearchResults {
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != fs.SkipAll {
		return nil, walkErr
	}
	return map[string]any{"files": matches, "count": len(matches)}, nil
}

// skipDir reports whether a path is inside a directory we never search (vendor
// dirs, VCS, build output) — keeps results relevant and walks fast.
func skipDir(path string) bool {
	lower := strings.ToLower(path)
	for _, seg := range []string{
		string(os.PathSeparator) + ".git" + string(os.PathSeparator),
		string(os.PathSeparator) + "node_modules" + string(os.PathSeparator),
		string(os.PathSeparator) + "vendor" + string(os.PathSeparator),
		string(os.PathSeparator) + ".vortex" + string(os.PathSeparator),
	} {
		if strings.Contains(lower, seg) {
			return true
		}
	}
	return false
}

// searchTools returns the read-only search/find toolset bound to cfg.
func searchTools(cfg LocalFSConfig) []Tool {
	return []Tool{
		SearchFilesTool{cfg: cfg},
		FindFilesTool{cfg: cfg},
	}
}
