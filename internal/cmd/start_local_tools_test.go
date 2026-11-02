package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/config"
)

func TestResolveLocalFSConfig_FromCue(t *testing.T) {
	trusted := t.TempDir()
	work := filepath.Join(trusted, "repo")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents: config.Agents{
			LocalTools: config.LocalTools{
				TrustedRoot: trusted,
				AutoMode:    true,
			},
		},
	}
	got, err := resolveLocalFSConfig(cfg, work)
	if err != nil {
		t.Fatalf("resolveLocalFSConfig: %v", err)
	}
	if got.Root != work {
		t.Errorf("Root = %q, want %q", got.Root, work)
	}
	wantTrust, _ := filepath.Abs(trusted)
	if got.TrustedRoot != wantTrust {
		t.Errorf("TrustedRoot = %q, want %q", got.TrustedRoot, wantTrust)
	}
	if !got.AutoMode {
		t.Error("AutoMode should be true")
	}
}
