package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/sandeepbollavaram/vortex/internal/update"
)

// githubRepo is the source repository for Gubern releases.
const githubRepo = "sandeepbollavaram/vortex"

// errSelfUpdate signals a self-update failure whose detail was already printed.
var errSelfUpdate = errors.New("self-update failed")

// newSelfUpdateCommand builds `gubern self-update`.
func newSelfUpdateCommand() *cobra.Command {
	var (
		checkOnly      bool
		assumeYes      bool
		allowDowngrade bool
	)
	c := &cobra.Command{
		Use:   "self-update",
		Short: "Update Gubern to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSelfUpdate(cmd, checkOnly, assumeYes, allowDowngrade)
		},
	}
	c.Flags().BoolVar(&checkOnly, "check", false, "check for updates without downloading")
	c.Flags().BoolVar(&assumeYes, "yes", false, "skip the confirmation prompt")
	c.Flags().BoolVar(&allowDowngrade, "allow-downgrade", false, "install the latest release even when it is not newer than this binary")
	return c
}

func runSelfUpdate(cmd *cobra.Command, checkOnly, assumeYes, allowDowngrade bool) error {
	out := cmd.OutOrStdout()
	update.SetUserAgent("vortex/" + version)
	ctx := cmd.Context()

	rel, err := update.FetchLatestRelease(ctx, githubRepo)
	if errors.Is(err, update.ErrNoReleases) {
		fmt.Fprintf(out, "No releases published yet for %s.\n", githubRepo)
		fmt.Fprintln(out, "Create a release by running: task tag VERSION=v0.1.0")
		return nil
	}
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
		return errSelfUpdate
	}

	if sameVersion(version, rel.Tag) {
		fmt.Fprintf(out, "Already on latest version (%s). Nothing to do.\n", rel.Tag)
		return nil
	}
	// Rollback protection (audit F-15): the release tag is unsigned, so an
	// old signed release re-published (or re-marked latest) must not be
	// installed over a newer binary. The extracted binary's own embedded
	// version is checked against the tag below.
	if !versionNewer(rel.Tag, version) && !allowDowngrade {
		fmt.Fprintf(out, "Latest release %s is not newer than this binary (%s); not installing.\n", rel.Tag, version)
		fmt.Fprintln(out, "Pass --allow-downgrade to install it anyway.")
		return nil
	}

	if checkOnly {
		fmt.Fprintf(out, "Update available: %s → %s\n", version, rel.Tag)
		fmt.Fprintf(out, "Run 'gubern self-update' to install it.\n")
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

	// Authenticity: when a signing key is pinned into this binary, verify the
	// Ed25519 signature over checksums.txt before trusting any checksum — this
	// is what defeats a release that swaps both the binary and its checksum
	// (production audit H4). In integrity-only mode (no pinned key) this is a
	// no-op and only the SHA-256 check applies.
	// The checksum map comes from the same bytes the signature covers: one
	// download, verified, then parsed (audit F-12).
	sums, err := update.VerifiedChecksums(ctx, rel)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: release signature check failed: %v\n", err)
		fmt.Fprintln(cmd.OutOrStderr(), "refusing to install an unverified release")
		return errSelfUpdate
	}
	wantSHA, ok := sums[asset.Name]
	if !ok {
		fmt.Fprintf(cmd.OutOrStderr(), "error: no checksum for %s in checksums.txt\n", asset.Name)
		return errSelfUpdate
	}

	// Temp archive keeps the asset's extension so Extract can detect its format.
	extractDir, err := os.MkdirTemp("", "vortex-update-*")
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
		return errSelfUpdate
	}
	defer func() { _ = os.RemoveAll(extractDir) }()
	archivePath := filepath.Join(extractDir, asset.Name)

	fmt.Fprintf(out, "Downloading %s ", asset.Name)
	progress := func(int64) { fmt.Fprint(out, ".") }
	if err := update.Download(ctx, asset.DownloadURL, archivePath, wantSHA, progress); err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "\nerror: %v\n", err)
		return errSelfUpdate
	}
	fmt.Fprintln(out, " done")

	newBin, err := extractBinary(archivePath, extractDir)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
		return errSelfUpdate
	}

	// The archive is authenticated, so its binary's embedded version is
	// signed data. It must be the release the tag names; an old signed
	// release replayed under a newer tag reports its old version (F-15).
	binVer, err := binaryVersion(ctx, newBin)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "error: checking the downloaded binary: %v\n", err)
		return errSelfUpdate
	}
	if !sameVersion(binVer, rel.Tag) {
		fmt.Fprintf(cmd.OutOrStderr(), "error: release %s contains a %s binary; refusing to install\n", rel.Tag, binVer)
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

// versionNewer reports whether candidate is a strictly newer semantic version
// than current. A current version that is not valid semver (a dev build) is
// treated as older than any release; an invalid candidate is never newer.
func versionNewer(candidate, current string) bool {
	c := "v" + strings.TrimPrefix(candidate, "v")
	if !semver.IsValid(c) {
		return false
	}
	cur := "v" + strings.TrimPrefix(current, "v")
	if !semver.IsValid(cur) {
		return true
	}
	return semver.Compare(c, cur) > 0
}

// binaryVersion runs `<bin> version` and returns the "Version:" field. Every
// release has the version subcommand (swapBinary relies on it too).
func binaryVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return "", fmt.Errorf("running %s version: %w", filepath.Base(bin), err)
	}
	return parseVersionOutput(string(out))
}

// parseVersionOutput extracts the version from `gubern version` output.
func parseVersionOutput(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Version:"); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v, nil
			}
		}
	}
	return "", errors.New("binary did not report a version")
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
