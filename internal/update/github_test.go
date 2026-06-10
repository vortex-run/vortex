package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLatestReleaseNoReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	old := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = old })

	_, err := FetchLatestRelease(context.Background(), "vortex-run/vortex")
	if !errors.Is(err, ErrNoReleases) {
		t.Fatalf("expected ErrNoReleases for 404, got %v", err)
	}
}

func TestFetchReleaseByTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/vortex-run/vortex/releases/tags/v0.2.0" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0","assets":[{"name":"checksums.txt","browser_download_url":"https://x/sums"}]}`))
	}))
	defer srv.Close()

	old := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = old })

	rel, err := FetchReleaseByTag(context.Background(), "vortex-run/vortex", "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.2.0" {
		t.Errorf("Tag = %q, want v0.2.0", rel.Tag)
	}
	if len(rel.Assets) != 1 || rel.Assets[0].Name != "checksums.txt" {
		t.Errorf("unexpected assets: %+v", rel.Assets)
	}

	if _, err := FetchReleaseByTag(context.Background(), "vortex-run/vortex", "v9.9.9"); !errors.Is(err, ErrNoReleases) {
		t.Errorf("expected ErrNoReleases for unknown tag, got %v", err)
	}
}

func sampleRelease() *Release {
	return &Release{
		Tag: "v1.2.3",
		Assets: []Asset{
			{Name: "vortex_linux_amd64.tar.gz", DownloadURL: "https://x/linux"},
			{Name: "vortex_darwin_arm64.tar.gz", DownloadURL: "https://x/darwin"},
			{Name: "vortex_windows_amd64.zip", DownloadURL: "https://x/win"},
			{Name: "checksums.txt", DownloadURL: "https://x/sums"},
		},
	}
}

func TestAssetForPlatformLinuxAMD64(t *testing.T) {
	a, err := AssetForPlatform(sampleRelease(), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "vortex_linux_amd64.tar.gz" {
		t.Errorf("got %q", a.Name)
	}
}

func TestAssetForPlatformWindowsAMD64(t *testing.T) {
	a, err := AssetForPlatform(sampleRelease(), "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "vortex_windows_amd64.zip" {
		t.Errorf("got %q", a.Name)
	}
}

func TestAssetForPlatformDarwinARM64(t *testing.T) {
	a, err := AssetForPlatform(sampleRelease(), "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "vortex_darwin_arm64.tar.gz" {
		t.Errorf("got %q", a.Name)
	}
}

func TestAssetForPlatformUnknown(t *testing.T) {
	if _, err := AssetForPlatform(sampleRelease(), "plan9", "mips"); err == nil {
		t.Error("expected error for unknown platform")
	}
}

func TestAssetName(t *testing.T) {
	if got := AssetName("linux", "arm64"); got != "vortex_linux_arm64.tar.gz" {
		t.Errorf("got %q", got)
	}
	if got := AssetName("windows", "amd64"); got != "vortex_windows_amd64.zip" {
		t.Errorf("got %q", got)
	}
}

func TestParseChecksums(t *testing.T) {
	in := "abc123  vortex_linux_amd64.tar.gz\n" +
		"def456  vortex_windows_amd64.zip\n" +
		"\n"
	m := ParseChecksums(in)
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

func TestFetchChecksumsNoAsset(t *testing.T) {
	rel := &Release{Tag: "v1.0.0", Assets: []Asset{{Name: "vortex_linux_amd64.tar.gz"}}}
	if _, err := FetchChecksums(t.Context(), rel); err == nil {
		t.Error("expected error when release has no checksums.txt asset")
	}
}
