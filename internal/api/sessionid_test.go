package api

import (
	"strings"
	"testing"
)

func TestValidSessionID(t *testing.T) {
	valid := []string{
		"abc123", "session_1", "my-session", "a.b.c",
		"01234567890123456789012345678901", // 32
		"S", "9",
	}
	for _, id := range valid {
		if !validSessionID(id) {
			t.Errorf("validSessionID(%q) = false, want true", id)
		}
	}

	invalid := []string{
		"",                      // empty
		".",                     // self
		"..",                    // parent
		"../etc/passwd",         // unix traversal
		`..\windows`,            // windows traversal
		"a/b",                   // path separator
		`a\b`,                   // backslash separator
		"with space",            // space
		"name;rm",               // shell metachar
		"a*b",                   // glob
		strings.Repeat("a", 65), // too long
	}
	for _, id := range invalid {
		if validSessionID(id) {
			t.Errorf("validSessionID(%q) = true, want false", id)
		}
	}
}
