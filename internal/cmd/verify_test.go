package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyRegisters(t *testing.T) {
	if newVerifyCommand().Use != "verify" {
		t.Error("verify command Use should be 'verify'")
	}
}

func TestVerifyFlags(t *testing.T) {
	if newVerifyCommand().Flags().Lookup("release") == nil {
		t.Error("--release flag not registered")
	}
}

func TestHashFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	content := []byte("vortex verify test")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(content)
	got, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("hashFile = %q, want %q", got, hex.EncodeToString(want[:]))
	}
}

func TestHashFileMissing(t *testing.T) {
	if _, err := hashFile(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestIsDevVersion(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"0.0.0-dev", true},
		{"v0.2.0-3-gabc1234", true},
		{"v0.2.0-3-gabc1234-dirty", true},
		{"", true},
		{"v0.2.0", false},
		{"0.2.0", false},
	}
	for _, c := range cases {
		if got := isDevVersion(c.v); got != c.want {
			t.Errorf("isDevVersion(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}

// Note: release fetching, checksum parsing, download verification, and
// extraction are covered in internal/update. This file covers only the
// command surface and local helpers.
