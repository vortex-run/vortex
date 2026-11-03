package api

import (
	"path/filepath"
	"strings"
	"testing"
)

// A session ID that passes validSessionID must be safe to use as a single
// file name under the session store: no separators, no traversal, no empty or
// over-long names.
func FuzzValidSessionID(f *testing.F) {
	for _, s := range []string{"abc", "a.b-c_1", ".", "..", "../x", `..\x`, "a/b", `a\b`, "", strings.Repeat("a", 65), "NUL", "x\x00y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, id string) {
		if !validSessionID(id) {
			return
		}
		if id == "" || len(id) > 64 || id == "." || id == ".." {
			t.Fatalf("accepted %q", id)
		}
		if strings.ContainsAny(id, `/\`+"\x00") {
			t.Fatalf("accepted %q with a separator or NUL", id)
		}
		if filepath.Base(id) != id {
			t.Fatalf("accepted %q, which is not a single path element", id)
		}
	})
}
