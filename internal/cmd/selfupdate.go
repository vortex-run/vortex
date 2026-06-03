package cmd

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/update"
)

// githubRepo is the source repository for VORTEX releases.
const githubRepo = "vortex-run/vortex"

// errSelfUpdate signals a self-update failure whose detail was already printed.
var errSelfUpdate = errors.New("self-update failed")

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
	update.SetUserAgent("vortex/" + version)
	ctx := cmd.Context()

	rel, err := update.FetchLatestRelease(ctx, githubRepo)
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

	asset, err := update.AssetForPlatform(rel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
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
	sums, err := update.FetchChecksums(ctx, rel)
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
	if err := downloadVerify(ctx, asset.DownloadURL, tmpName, wantSHA, out); err != nil {
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
