package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/update"
)

// errVerify signals a verification failure whose detail was already printed.
var errVerify = errors.New("verification failed")

// newVerifyCommand builds `vortex verify` (build plan M19). It checks the
// running binary against the published release artifacts: checksums.txt only
// hashes the release *archives*, so the command downloads the platform archive
// (itself verified against checksums.txt), extracts the binary inside, and
// compares its SHA-256 with the running executable's.
func newVerifyCommand() *cobra.Command {
	var tag string
	c := &cobra.Command{
		Use:   "verify",
		Short: "Verify this binary against published release checksums",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runVerify(cmd, tag)
		},
	}
	c.Flags().StringVar(&tag, "release", "",
		"release tag to verify against (default: this build's version, else latest)")
	return c
}

func runVerify(cmd *cobra.Command, tag string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.OutOrStderr()
	update.SetUserAgent("vortex/" + version)
	ctx := cmd.Context()

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(errOut, "error: resolving own path: %v\n", err)
		return errVerify
	}
	selfSHA, err := hashFile(self)
	if err != nil {
		fmt.Fprintf(errOut, "error: hashing %s: %v\n", self, err)
		return errVerify
	}

	fmt.Fprintf(out, "Binary: %s\n", self)
	fmt.Fprintf(out, "SHA256: %s\n", selfSHA)

	rel, err := resolveVerifyRelease(ctx, tag)
	if errors.Is(err, update.ErrNoReleases) {
		fmt.Fprintf(out, "Status: ? No releases published yet for %s — nothing to verify against.\n", githubRepo)
		return nil
	}
	if err != nil {
		fmt.Fprintf(errOut, "error: %v\n", err)
		return errVerify
	}

	releaseSHA, err := fetchReleaseBinarySHA(ctx, rel)
	if err != nil {
		fmt.Fprintf(errOut, "error: %v\n", err)
		return errVerify
	}

	if releaseSHA == selfSHA {
		fmt.Fprintf(out, "Status: ✓ Verified against %s release\n", rel.Tag)
		return nil
	}
	fmt.Fprintf(out, "Status: ✗ Binary does not match release checksums\n")
	fmt.Fprintf(out, "         (may be a development build or modified)\n")
	return errVerify
}

// resolveVerifyRelease picks which release to verify against: an explicit
// --release tag wins; otherwise the tag matching this build's version; for dev
// builds (or when the version tag has no release) it falls back to latest.
func resolveVerifyRelease(ctx context.Context, tag string) (*update.Release, error) {
	if tag != "" {
		rel, err := update.FetchReleaseByTag(ctx, githubRepo, tag)
		if errors.Is(err, update.ErrNoReleases) {
			return nil, fmt.Errorf("release %s not found", tag)
		}
		return rel, err
	}
	if v := strings.TrimPrefix(version, "v"); v != "" && !isDevVersion(version) {
		rel, err := update.FetchReleaseByTag(ctx, githubRepo, "v"+v)
		if err == nil {
			return rel, nil
		}
		if !errors.Is(err, update.ErrNoReleases) {
			return nil, err
		}
		// No release for this exact version — fall through to latest.
	}
	return update.FetchLatestRelease(ctx, githubRepo)
}

// isDevVersion reports whether v looks like a non-release build: the compiled
// default, a git-describe suffix (v0.2.0-3-gabc1234), or a dirty tree.
func isDevVersion(v string) bool {
	return v == "" || strings.Contains(v, "-")
}

// fetchReleaseBinarySHA downloads the release archive for the running
// platform, verifies it against checksums.txt, extracts the binary inside,
// and returns that binary's SHA-256 hex digest.
func fetchReleaseBinarySHA(ctx context.Context, rel *update.Release) (string, error) {
	sums, err := update.FetchChecksums(ctx, rel)
	if err != nil {
		return "", err
	}
	asset, err := update.AssetForPlatform(rel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	wantSHA, ok := sums[asset.Name]
	if !ok {
		return "", fmt.Errorf("no checksum for %s in checksums.txt", asset.Name)
	}

	dir, err := os.MkdirTemp("", "vortex-verify-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	archive := filepath.Join(dir, asset.Name)
	if err := update.Download(ctx, asset.DownloadURL, archive, wantSHA, nil); err != nil {
		return "", err
	}

	binName := "vortex"
	if runtime.GOOS == "windows" {
		binName = "vortex.exe"
	}
	bin, err := update.Extract(archive, dir, binName)
	if err != nil {
		return "", err
	}
	return hashFile(bin)
}

// hashFile returns the SHA-256 hex digest of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
