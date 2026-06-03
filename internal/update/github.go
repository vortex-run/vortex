// Package update implements VORTEX's self-update machinery: querying the GitHub
// releases API, downloading and verifying release archives, and atomically
// hot-swapping the running binary. It is split from the CLI command so the
// logic is testable without a terminal and reusable by future agents (e.g. the
// DevOps self-healing agents in M14). Stdlib only — no external HTTP client.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// ErrNoReleases is returned by FetchLatestRelease when the repository has no
// published releases yet (the GitHub API answers 404). It is informational
// rather than a failure — callers should treat it as "nothing to update to".
var ErrNoReleases = errors.New("no releases published yet")

// userAgent is sent on every GitHub request; GitHub requires a User-Agent.
var userAgent = "vortex/dev"

// apiBaseURL is the GitHub API root. It is a var (not a const) so tests can
// point FetchLatestRelease at a local httptest server.
var apiBaseURL = "https://api.github.com"

// SetUserAgent sets the User-Agent string sent to GitHub (e.g. the build
// version). Safe to call once at startup.
func SetUserAgent(ua string) { userAgent = ua }

// Release is a typed subset of a GitHub release.
type Release struct {
	Tag        string
	Assets     []Asset
	Body       string
	PreRelease bool
	Draft      bool
}

// Asset is a single downloadable file attached to a release.
type Asset struct {
	Name        string
	DownloadURL string
	Size        int64
}

// ghRelease/ghAsset model the raw GitHub JSON.
type ghRelease struct {
	Tag        string    `json:"tag_name"`
	Body       string    `json:"body"`
	PreRelease bool      `json:"prerelease"`
	Draft      bool      `json:"draft"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// httpGet issues a GET with the VORTEX User-Agent under ctx.
func httpGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return http.DefaultClient.Do(req)
}

// FetchLatestRelease returns the latest published release for repo (e.g.
// "vortex-run/vortex"). It applies a 30s timeout derived from ctx.
func FetchLatestRelease(ctx context.Context, repo string) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	url := apiBaseURL + "/repos/" + repo + "/releases/latest"
	resp, err := httpGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetching latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoReleases
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
	}

	var gr ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, fmt.Errorf("parsing release JSON: %w", err)
	}
	rel := &Release{
		Tag:        gr.Tag,
		Body:       gr.Body,
		PreRelease: gr.PreRelease,
		Draft:      gr.Draft,
	}
	for _, a := range gr.Assets {
		rel.Assets = append(rel.Assets, Asset(a))
	}
	return rel, nil
}

// FetchChecksums downloads and parses the release's checksums.txt, returning a
// map of filename → SHA-256 hex.
func FetchChecksums(ctx context.Context, release *Release) (map[string]string, error) {
	asset := findAsset(release, "checksums.txt")
	if asset == nil {
		return nil, errors.New("release has no checksums.txt asset")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := httpGet(ctx, asset.DownloadURL)
	if err != nil {
		return nil, fmt.Errorf("downloading checksums.txt: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checksums.txt download returned %s", resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return ParseChecksums(string(b)), nil
}

// ParseChecksums turns "<hex>  <filename>" lines into a filename→hash map.
func ParseChecksums(s string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			m[fields[1]] = fields[0]
		}
	}
	return m
}

// AssetForPlatform returns the release archive Asset for the given platform.
// The naming follows GoReleaser's config: vortex_<goos>_<goarch>.tar.gz for
// linux/darwin and .zip for windows.
func AssetForPlatform(release *Release, goos, goarch string) (*Asset, error) {
	name := AssetName(goos, goarch)
	if a := findAsset(release, name); a != nil {
		return a, nil
	}
	return nil, fmt.Errorf("no release asset %q for %s/%s", name, goos, goarch)
}

// AssetName returns the archive filename for a platform.
func AssetName(goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("vortex_%s_%s.%s", goos, goarch, ext)
}

// CurrentPlatformAsset is a convenience wrapper for the running platform.
func CurrentPlatformAsset(release *Release) (*Asset, error) {
	return AssetForPlatform(release, runtime.GOOS, runtime.GOARCH)
}

func findAsset(release *Release, name string) *Asset {
	for i := range release.Assets {
		if release.Assets[i].Name == name {
			return &release.Assets[i]
		}
	}
	return nil
}
