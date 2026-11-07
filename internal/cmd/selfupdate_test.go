package cmd

import "testing"

func TestSelfUpdateRegisters(t *testing.T) {
	if newSelfUpdateCommand().Use != "self-update" {
		t.Error("self-update command Use should be 'self-update'")
	}
}

func TestSelfUpdateFlags(t *testing.T) {
	c := newSelfUpdateCommand()
	for _, name := range []string{"check", "yes"} {
		if c.Flags().Lookup(name) == nil {
			t.Errorf("--%s flag not registered", name)
		}
	}
}

func TestSameVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.2.3", "v1.2.3", true},
		{"1.2.3", "v1.2.3", true},
		{"v1.2.3", "1.2.3", true},
		{"v1.2.3", "v1.2.4", false},
	}
	for _, c := range cases {
		if got := sameVersion(c.a, c.b); got != c.want {
			t.Errorf("sameVersion(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// Note: download/SHA-256/extract behaviour is covered in internal/update
// (download_test.go). This file covers only the command surface.

// Audit F-15: self-update must not install a release that is not newer, and
// must check the downloaded binary's own version against the tag.
func TestVersionNewer(t *testing.T) {
	cases := []struct {
		cand, cur string
		want      bool
	}{
		{"v0.5.1", "0.5.0", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.5.0", "0.5.0", false},
		{"v0.1.0", "0.5.0", false}, // rollback
		{"v0.5.0", "0.0.0-dev", true},
		{"v0.2.0", "v0.2.0-3-gabc1234", true}, // git-describe build of the same line
		{"latest", "0.5.0", false},
		{"v0.6.0", "not-a-version", true},
	}
	for _, c := range cases {
		if got := versionNewer(c.cand, c.cur); got != c.want {
			t.Errorf("versionNewer(%q, %q) = %v, want %v", c.cand, c.cur, got, c.want)
		}
	}
}

func TestParseVersionOutput(t *testing.T) {
	v, err := parseVersionOutput("Version:    0.1.0\nCommit:     abc\nBuilt:      x\n")
	if err != nil || v != "0.1.0" {
		t.Fatalf("got %q, %v", v, err)
	}
	if !sameVersion(v, "v0.1.0") || sameVersion(v, "v9.9.9") {
		t.Error("an old binary replayed under tag v9.9.9 must not match")
	}
	if _, err := parseVersionOutput("garbage"); err == nil {
		t.Error("output without a Version line must be an error")
	}
}
