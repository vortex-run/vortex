package agents

import "testing"

func TestLocalFSFromRuntime_WiresTrustAndAutoMode(t *testing.T) {
	cfg := LocalFSFromRuntime("/work", "/work/trust", true)
	if cfg.Root != "/work" || cfg.TrustedRoot != "/work/trust" || !cfg.AutoMode {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLocalFSFromRuntime_DefaultsEmptyTrust(t *testing.T) {
	cfg := LocalFSFromRuntime("/work", "", false)
	if cfg.TrustedRoot != "" || cfg.AutoMode {
		t.Errorf("cfg = %+v", cfg)
	}
}
