package cmd

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// githubRepo is the source repository for VORTEX releases.
const githubRepo = "vortex-run/vortex"

// errSelfUpdate signals a self-update failure whose detail was already printed.
var errSelfUpdate = errors.New("self-update failed")

// ghRelease and ghAsset model the subset of the GitHub releases API we need.
type ghRelease struct {
	Tag    string    `json:"tag_name"`
	Assets []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// newSelfUpdateCommand builds `vortex self-update`.
func newSelfUpdateCommand() *cobra.Command {
	var (
		checkOnly bool
		assumeYes bool
	)
	c := &cobra.Command{
		Use:   "self-update",
		Short: "Update VORTEX to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSelfUpdate(cmd, checkOnly, assumeYes)
		},
	}
	c.Flags().BoolVar(&checkOnly, "check", false, "check for updates without downloading")
	c.Flags().BoolVar(&assumeYes, "yes", false, "skip the confirmation prompt")
	return c
}

func runSelfUpdate(cmd *cobra.Command, checkOnly, assumeYes bool) error {
	out := cmd.OutOrStdout()
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	rel, err := fetchLatestRelease(ctx)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
		return errSelfUpdate
	}

	if sameVersion(version, rel.Tag) {
		fmt.Fprintf(out, "Already on latest version (%s). Nothing to do.\n", rel.Tag)
		return nil
	}

	if checkOnly {
		fmt.Fprintf(out, "Update available: %s → %s\n", version, rel.Tag)
		fmt.Fprintf(out, "Run 'vortex self-update' to install it.\n")
		return nil
	}

	assetName := assetFileName(runtime.GOOS, runtime.GOARCH)
	asset := findAsset(rel, assetName)
	if asset == nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: no release asset %q for this platform\n", assetName)
		return errSelfUpdate
	}

	if !assumeYes {
		fmt.Fprintf(out, "This will download %s (%s) and replace the current binary.\n", asset.Name, rel.Tag)
		fmt.Fprintf(out, "Proceed? [y/N] ")
		if !confirmed(cmd.InOrStdin()) {
			fmt.Fprintln(out, "Aborted.")
			return nil
		}
	}

	// Fetch and parse checksums.txt for this asset.
	sums, err := fetchChecksums(ctx, rel)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
		return errSelfUpdate
	}
	wantSHA, ok := sums[asset.Name]
	if !ok {
		fmt.Fprintf(cmd.OutOrStderr(), "error: no checksum for %s in checksums.txt\n", asset.Name)
		return errSelfUpdate
	}

	tmp, err := os.CreateTemp("", "vortex-update-*")
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
		return errSelfUpdate
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpName) }()

	fmt.Fprintf(out, "Downloading %s ", asset.Name)
	if err := downloadVerify(ctx, asset.URL, tmpName, wantSHA, out); err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "\nerror: %v\n", err)
		return errSelfUpdate
	}
	fmt.Fprintln(out, " done")

	binInArchive := "vortex"
	if runtime.GOOS == "windows" {
		binInArchive = "vortex.exe"
	}
	extractDir, err := os.MkdirTemp("", "vortex-extract-*")
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
		return errSelfUpdate
	}
	defer func() { _ = os.RemoveAll(extractDir) }()

	newBin, err := extractBinary(tmpName, extractDir, binInArchive)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
		return errSelfUpdate
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: resolving own path: %v\n", err)
		return errSelfUpdate
	}

	if err := swapBinary(newBin, self, rel.Tag, out); err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
		return errSelfUpdate
	}
	return nil
}

// sameVersion reports whether two version strings refer to the same release,
// tolerating a leading "v" on either side.
func sameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// assetFileName returns the release archive name for a platform.
func assetFileName(goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("vortex_%s_%s.%s", goos, goarch, ext)
}

func findAsset(rel *ghRelease, name string) *ghAsset {
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			return &rel.Assets[i]
		}
	}
	return nil
}

// confirmed reads a line and reports whether it is an affirmative y/yes.
func confirmed(r io.Reader) bool {
	s := bufio.NewScanner(r)
	if !s.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(s.Text())) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func httpGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "vortex/"+version)
	return http.DefaultClient.Do(req)
}

func fetchLatestRelease(ctx context.Context) (*ghRelease, error) {
	url := "https://api.github.com/repos/" + githubRepo + "/releases/latest"
	resp, err := httpGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetching latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("parsing release JSON: %w", err)
	}
	return &rel, nil
}

func fetchChecksums(ctx context.Context, rel *ghRelease) (map[string]string, error) {
	asset := findAsset(rel, "checksums.txt")
	if asset == nil {
		return nil, errors.New("release has no checksums.txt asset")
	}
	resp, err := httpGet(ctx, asset.URL)
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
	return parseChecksums(string(b)), nil
}

// parseChecksums turns "<hex>  <filename>" lines into a filename→hash map.
func parseChecksums(s string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			m[fields[1]] = fields[0]
		}
	}
	return m
}

// downloadVerify streams url to dest and checks its SHA-256, printing a dot per
// megabyte. It removes dest on any failure.
func downloadVerify(ctx context.Context, url, dest, wantSHA string, progress io.Writer) error {
	resp, err := httpGet(ctx, url)
	if err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_ = os.Remove(dest)
		return fmt.Errorf("download returned %s", resp.Status)
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	cw := &dotWriter{w: progress, every: 1024 * 1024}
	if _, err := io.Copy(io.MultiWriter(f, h, cw), resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(dest)
		return fmt.Errorf("writing download: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantSHA {
		_ = os.Remove(dest)
		return fmt.Errorf("checksum mismatch: expected %s, got %s", wantSHA, got)
	}
	return nil
}

// dotWriter prints a dot to w every `every` bytes copied through it.
type dotWriter struct {
	w     io.Writer
	every int64
	acc   int64
}

func (d *dotWriter) Write(p []byte) (int, error) {
	d.acc += int64(len(p))
	for d.acc >= d.every {
		fmt.Fprint(d.w, ".")
		d.acc -= d.every
	}
	return len(p), nil
}

// extractBinary extracts the named file from a .tar.gz or .zip archive into
// destDir and returns its path. It rejects unsafe paths (zip-slip).
func extractBinary(archivePath, destDir, name string) (string, error) {
	if strings.HasSuffix(archivePath, ".zip") || isZip(archivePath) {
		return extractFromZip(archivePath, destDir, name)
	}
	return extractFromTarGz(archivePath, destDir, name)
}

// isZip sniffs the file header for the PK zip magic, since the temp file has no
// extension.
func isZip(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	var magic [2]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return magic[0] == 'P' && magic[1] == 'K'
}

func safeName(name string) bool {
	return !strings.Contains(name, "..") && !filepath.IsAbs(name) && !strings.HasPrefix(name, "/")
}

func extractFromTarGz(archivePath, destDir, name string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if !safeName(hdr.Name) {
			return "", fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		if filepath.Base(hdr.Name) == name {
			return writeExtracted(tr, destDir, name)
		}
	}
	return "", fmt.Errorf("%s not found in archive", name)
}

func extractFromZip(archivePath, destDir, name string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = zr.Close() }()
	for _, zf := range zr.File {
		if !safeName(zf.Name) {
			return "", fmt.Errorf("unsafe path in archive: %q", zf.Name)
		}
		if filepath.Base(zf.Name) == name {
			rc, err := zf.Open()
			if err != nil {
				return "", err
			}
			defer func() { _ = rc.Close() }()
			return writeExtracted(rc, destDir, name)
		}
	}
	return "", fmt.Errorf("%s not found in archive", name)
}

func writeExtracted(r io.Reader, destDir, name string) (string, error) {
	dest := filepath.Join(destDir, name)
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dest, nil
}
