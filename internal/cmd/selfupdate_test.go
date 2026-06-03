package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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

func TestDownloadVerifyChecksumMismatchRemovesFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("the actual payload"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "dl.bin")
	err := downloadVerify(context.Background(), srv.URL, dest, "0000deadbeef", io.Discard)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("dest file should be removed on mismatch, stat err = %v", statErr)
	}
}
