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
