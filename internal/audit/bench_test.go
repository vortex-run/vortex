package audit

import (
	"context"
	"path/filepath"
	"testing"
)

// BenchmarkAppend measures one audit entry, the cost added to every audited
// agent action and API mutation.
func BenchmarkAppend(b *testing.B) {
	for _, bc := range []struct {
		name   string
		detail map[string]any
	}{
		{"no-detail", nil},
		{"tool-detail", map[string]any{"tool": "run_terminal", "ok": false, "error": "exit status 1"}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			l, err := NewLog(filepath.Join(b.TempDir(), "audit.log"), []byte("bench-key"))
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := l.Append(ctx, "agent", "tool.execute", "run_terminal", bc.detail); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkVerify measures verifying a 1,000-entry chain (startup cost).
func BenchmarkVerify(b *testing.B) {
	l, err := NewLog(filepath.Join(b.TempDir(), "audit.log"), []byte("bench-key"))
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		_ = l.Append(context.Background(), "agent", "tool.execute", "x", map[string]any{"ok": true, "i": i})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := l.Verify(); err != nil {
			b.Fatal(err)
		}
	}
}
