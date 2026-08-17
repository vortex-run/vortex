package update

import (
	"fmt"
	"io"
)

// Size limits for everything the self-update path reads from a release host.
//
// The whole premise of the signed-release work (production audit H4) is that
// the release host is not trusted: a compromised CI token, an account
// takeover, or a tampered mirror can serve whatever it likes. Yet the update
// path read every artifact without a ceiling, and two of those reads happen
// BEFORE any signature can be checked — the signature and the checksum file
// have to be fetched before they can be verified, so no amount of signing
// protects them.
//
// A hostile response therefore had a free hand:
//   - checksums.txt / the .sig read into memory with io.ReadAll: unbounded heap
//   - the archive entry expanded to disk with io.Copy: unbounded disk
//
// The archive case was explicitly assumed safe by a "size bounded by release
// archive" comment. It is not: a zip entry decompresses to as much as its
// author wants. Measured with a trivially compressible entry, a 0.19 MB
// archive wrote 200 MB — a 1028x expansion, and a real bomb does far better.
//
// The limits below are chosen against what these artifacts actually are, with
// generous headroom, so a legitimate release is never refused.
const (
	// maxMetadataBytes bounds checksums.txt and the detached signature. A
	// checksums file for a full platform matrix is a few KB; the signature is
	// 64 bytes. 1 MiB is orders of magnitude more than either will ever need.
	maxMetadataBytes = 1 << 20

	// maxArtifactBytes bounds the downloaded archive and each file extracted
	// from it. The vortex binary is ~71 MB, so 512 MiB leaves room for it to
	// grow several times over while still capping a hostile stream.
	maxArtifactBytes = 512 << 20
)

// errTooLarge reports an artifact that exceeded its ceiling. It names the
// limit so an operator hitting it on a legitimately large release knows what
// to raise, rather than seeing an unexplained failure.
func errTooLarge(what string, limit int64) error {
	return fmt.Errorf("update: %s exceeds the %d-byte limit; refusing to continue "+
		"(a release host that serves more than this is either broken or hostile)", what, limit)
}

// readLimited reads at most limit bytes, and fails if there is more.
//
// It reads limit+1 and checks for the extra byte rather than silently
// truncating: a truncated checksums.txt or signature would fail verification
// later with a confusing mismatch, when the real problem is an oversized
// response. Failing here names the actual cause.
func readLimited(r io.Reader, limit int64, what string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge(what, limit)
	}
	return b, nil
}

// copyLimited copies at most limit bytes, and fails if the source has more.
// Used where the destination is a file, so the ceiling protects disk rather
// than heap — the decompression-bomb case.
func copyLimited(dst io.Writer, src io.Reader, limit int64, what string) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, errTooLarge(what, limit)
	}
	return n, nil
}
