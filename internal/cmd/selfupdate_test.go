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

func TestAssetFileName(t *testing.T) {
	cases := map[[2]string]string{
		{"linux", "amd64"}:   "vortex_linux_amd64.tar.gz",
		{"linux", "arm64"}:   "vortex_linux_arm64.tar.gz",
		{"darwin", "arm64"}:  "vortex_darwin_arm64.tar.gz",
		{"darwin", "amd64"}:  "vortex_darwin_amd64.tar.gz",
		{"windows", "amd64"}: "vortex_windows_amd64.zip",
	}
	for k, want := range cases {
		if got := assetFileName(k[0], k[1]); got != want {
			t.Errorf("assetFileName(%q,%q) = %q, want %q", k[0], k[1], got, want)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	in := "abc123  vortex_linux_amd64.tar.gz\n" +
		"def456  vortex_windows_amd64.zip\n" +
		"\n" // trailing blank line tolerated
	m := parseChecksums(in)
	if m["vortex_linux_amd64.tar.gz"] != "abc123" {
		t.Errorf("linux sum = %q", m["vortex_linux_amd64.tar.gz"])
	}
	if m["vortex_windows_amd64.zip"] != "def456" {
		t.Errorf("windows sum = %q", m["vortex_windows_amd64.zip"])
	}
	if len(m) != 2 {
		t.Errorf("expected 2 entries, got %d", len(m))
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

func TestFindAsset(t *testing.T) {
	rel := &ghRelease{Assets: []ghAsset{
		{Name: "vortex_linux_amd64.tar.gz"},
		{Name: "checksums.txt"},
	}}
	if findAsset(rel, "checksums.txt") == nil {
		t.Error("should find checksums.txt")
	}
	if findAsset(rel, "nope") != nil {
		t.Error("should not find a missing asset")
	}
}
