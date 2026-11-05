package legacyenv

import (
	"os"
	"strings"
	"testing"
)

func TestApply_LegacyNameFillsNewName(t *testing.T) {
	t.Setenv("VORTEX_LEGACYTEST_A", "old")
	t.Setenv("VORTEX_LEGACYTEST_B", "old-b")
	t.Setenv("GUBERN_LEGACYTEST_B", "new-b") // new name wins
	os.Unsetenv("GUBERN_LEGACYTEST_A")
	t.Cleanup(func() { os.Unsetenv("GUBERN_LEGACYTEST_A") })

	applied := Apply()
	if got := os.Getenv("GUBERN_LEGACYTEST_A"); got != "old" {
		t.Errorf("GUBERN_LEGACYTEST_A = %q, want the legacy value", got)
	}
	if got := os.Getenv("GUBERN_LEGACYTEST_B"); got != "new-b" {
		t.Errorf("GUBERN_LEGACYTEST_B = %q: a set new name must not be overwritten", got)
	}
	found := map[string]bool{}
	for _, n := range applied {
		found[n] = true
	}
	if !found["VORTEX_LEGACYTEST_A"] || found["VORTEX_LEGACYTEST_B"] {
		t.Errorf("applied = %v", applied)
	}
	n := Notice([]string{"VORTEX_LEGACYTEST_A"})
	if !strings.Contains(n, "VORTEX_LEGACYTEST_A") || !strings.Contains(n, "GUBERN_LEGACYTEST_A") {
		t.Errorf("notice = %q", n)
	}
	if Notice(nil) != "" {
		t.Error("empty notice expected")
	}
}

func TestFill(t *testing.T) {
	env := map[string]string{"VORTEX_X": "1", "VORTEX_Y": "old", "GUBERN_Y": "new", "VORTEX_": "ignored", "OTHER": "z"}
	Fill(env)
	if env["GUBERN_X"] != "1" || env["GUBERN_Y"] != "new" {
		t.Errorf("env = %v", env)
	}
	if _, ok := env["GUBERN_"]; ok {
		t.Error("bare prefix must not be mapped")
	}
}
