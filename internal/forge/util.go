package forge

import (
	"os"
	"path/filepath"
	"strings"
)

// readFile reads a file's bytes.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // reading a sandboxed build artifact
}

// baseName returns the final path element.
func baseName(path string) string { return filepath.Base(path) }

// truncate shortens s to at most n bytes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// slug converts a description into a short URL-safe name.
func slug(desc string) string {
	desc = strings.ToLower(strings.TrimSpace(desc))
	var b strings.Builder
	for _, r := range desc {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "app"
	}
	return s
}
