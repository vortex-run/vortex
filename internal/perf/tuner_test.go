package perf

import (
	"runtime"
	"testing"
)

func TestTuner_DetectOSNonEmpty(t *testing.T) {
	os := DetectOS()
	if os == "" {
		t.Error("DetectOS returned empty string")
	}
	if os != runtime.GOOS {
		t.Errorf("DetectOS = %q, want %q", os, runtime.GOOS)
	}
}

func TestTuner_RecommendedSysctlByOS(t *testing.T) {
	m := RecommendedSysctl()
	switch runtime.GOOS {
	case "linux":
		if len(m) == 0 {
			t.Error("Linux should have sysctl recommendations")
		}
		if m["net.core.somaxconn"] != "65535" {
			t.Errorf("somaxconn = %q, want 65535", m["net.core.somaxconn"])
		}
	case "windows":
		if len(m) != 0 {
			t.Errorf("Windows should have no sysctl recommendations, got %v", m)
		}
	}
}

func TestTuner_ApplyDryRunNoChanges(t *testing.T) {
	res := Apply(true)
	// Dry run applies nothing; everything is reported as skipped (or there is
	// nothing to do on Windows).
	if len(res.Applied) != 0 {
		t.Errorf("dry run should apply nothing, got %v", res.Applied)
	}
	if len(res.Errors) != 0 {
		t.Errorf("dry run should produce no errors, got %v", res.Errors)
	}
	// On an OS with recommendations, dry run lists them as skipped.
	if len(RecommendedSysctl()) > 0 && len(res.Skipped) == 0 {
		t.Error("dry run should report skipped settings when recommendations exist")
	}
}

func TestTuner_MaxGOMAXPROCS(t *testing.T) {
	if got := MaxGOMAXPROCS(); got < 1 {
		t.Errorf("MaxGOMAXPROCS = %d, want >= 1", got)
	}
}

func TestTuner_RecommendedBufferSize(t *testing.T) {
	size := RecommendedBufferSize()
	valid := map[int]bool{16 * 1024: true, 32 * 1024: true, 64 * 1024: true}
	if !valid[size] {
		t.Errorf("RecommendedBufferSize = %d, want one of 16/32/64 KB", size)
	}
}
