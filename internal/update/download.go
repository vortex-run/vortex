package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// progressBytes is how many bytes pass through before progress is reported.
const progressBytes = 1024 * 1024

// Download streams url to the file at dest, verifying its SHA-256 against
// expectedSHA256 (hex). progress, if non-nil, is called with the cumulative
// byte count roughly every 1MB. On a checksum mismatch or a cancelled context,
// the partial dest file is removed and an error is returned.
func Download(ctx context.Context, url, dest, expectedSHA256 string, progress func(n int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
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
	pw := &progressWriter{fn: progress}
	_, copyErr := io.Copy(io.MultiWriter(f, h, pw), resp.Body)
	closeErr := f.Close()

	if copyErr != nil {
		_ = os.Remove(dest)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("writing download: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return closeErr
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedSHA256 {
		_ = os.Remove(dest)
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedSHA256, got)
	}
	return nil
}

// progressWriter reports cumulative bytes written every progressBytes.
type progressWriter struct {
	fn    func(n int64)
	total int64
	since int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.total += int64(n)
	p.since += int64(n)
	if p.fn != nil {
		for p.since >= progressBytes {
			p.fn(p.total)
			p.since -= progressBytes
		}
	}
	return n, nil
}

// Extract pulls the single entry named filename out of a .tar.gz or .zip
// archive into destDir, returning the path to the extracted file. The archive
// format is detected from the archive's extension. Entries whose path escapes
// destDir (zip-slip: containing ".." or absolute paths) are rejected.
func Extract(archive, destDir, filename string) (string, error) {
	switch {
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(archive, destDir, filename)
	case strings.HasSuffix(archive, ".tar.gz"), strings.HasSuffix(archive, ".tgz"):
		return extractTarGz(archive, destDir, filename)
	default:
		return "", fmt.Errorf("unsupported archive format: %s", archive)
	}
}

// safeEntry rejects archive entry names that would escape the destination.
func safeEntry(name string) bool {
	clean := filepath.ToSlash(name)
	return !strings.Contains(clean, "..") && !filepath.IsAbs(name) && !strings.HasPrefix(clean, "/")
}

func extractTarGz(archive, destDir, filename string) (string, error) {
	f, err := os.Open(archive)
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
		if !safeEntry(hdr.Name) {
			return "", fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		if filepath.Base(hdr.Name) == filename {
			return writeEntry(tr, destDir, filename)
		}
	}
	return "", fmt.Errorf("%s not found in archive", filename)
}

func extractZip(archive, destDir, filename string) (string, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer func() { _ = zr.Close() }()
	for _, zf := range zr.File {
		if !safeEntry(zf.Name) {
			return "", fmt.Errorf("unsafe path in archive: %q", zf.Name)
		}
		if filepath.Base(zf.Name) == filename {
			rc, err := zf.Open()
			if err != nil {
				return "", err
			}
			path, werr := writeEntry(rc, destDir, filename)
			_ = rc.Close()
			return path, werr
		}
	}
	return "", fmt.Errorf("%s not found in archive", filename)
}

func writeEntry(r io.Reader, destDir, name string) (string, error) {
	dest := filepath.Join(destDir, name)
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, r); err != nil { //nolint:gosec // size bounded by release archive
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dest, nil
}

// AtomicReplace replaces the binary at targetPath with newBin: the existing
// target is moved to targetPath+".bak", then newBin is copied into place with
// 0755 permissions. On any failure it restores the .bak. The caller is
// responsible for removing the .bak after verifying the new binary.
func AtomicReplace(newBin, targetPath string) error {
	bak := targetPath + ".bak"
	if err := os.Rename(targetPath, bak); err != nil {
		return fmt.Errorf("moving current binary aside: %w", err)
	}
	if err := copyFileMode(newBin, targetPath, 0o755); err != nil {
		_ = os.Rename(bak, targetPath) // restore
		return fmt.Errorf("installing new binary: %w", err)
	}
	return nil
}

// Rollback restores targetPath+".bak" back to targetPath. It is idempotent: if
// no .bak exists it returns nil.
func Rollback(targetPath string) error {
	bak := targetPath + ".bak"
	if _, err := os.Stat(bak); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	_ = os.Remove(targetPath)
	return os.Rename(bak, targetPath)
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
