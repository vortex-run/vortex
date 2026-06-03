package cmd

import (
	"bufio"
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

	binInArchive := "vortex"
	if runtime.GOOS == "windows" {
		binInArchive = "vortex.exe"
	}
	newBin, err := update.Extract(archivePath, extractDir, binInArchive)
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
