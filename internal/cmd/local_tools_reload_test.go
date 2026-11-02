package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/config"
)

// Local FS tools are registered once at startup (buildAgentRuntime →
// RegisterLocalTools). SIGHUP reload updates config.Holder but does not rebuild
// tool registries. This test documents that resolveLocalFSConfig — the same
// helper used at startup — reflects a swapped Holder, which is necessary but
// not sufficient for live local-tool behavior.
func TestResolveLocalFSConfig_FollowsConfigHolderSwap(t *testing.T) {
	trusted := t.TempDir()
	work := filepath.Join(trusted, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}

	holder := config.NewHolder(&config.Config{
		Agents: config.Agents{
			LocalTools: config.LocalTools{TrustedRoot: trusted, AutoMode: false},
		},
	})

	first, err := resolveLocalFSConfig(holder.Get(), work)
	if err != nil {
		t.Fatalf("first resolveLocalFSConfig: %v", err)
	}
	want, _ := filepath.Abs(trusted)
	if first.TrustedRoot != want || first.AutoMode {
		t.Fatalf("first = %+v, want TrustedRoot=%q AutoMode=false", first, want)
	}

	holder.Store(&config.Config{
		Agents: config.Agents{
			LocalTools: config.LocalTools{TrustedRoot: trusted, AutoMode: true},
		},
	})

	second, err := resolveLocalFSConfig(holder.Get(), work)
	if err != nil {
		t.Fatalf("second resolveLocalFSConfig after holder swap: %v", err)
	}
	if second.TrustedRoot != want || !second.AutoMode {
		t.Fatalf("second = %+v, want TrustedRoot=%q AutoMode=true", second, want)
	}
}
