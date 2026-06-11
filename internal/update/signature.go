package update

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ReleaseSigningPublicKey is the Ed25519 public key (base64, 32 bytes) that
// signs release checksums.txt files. It is empty until a signing key is
// provisioned (see scripts/sign.sh and the release workflow); when empty,
// signature verification is skipped and only the SHA-256 integrity check
// applies. Pinning the key in the binary is what makes the auto-update path
// authenticity-checked rather than merely integrity-checked (production audit
// H4): a compromised release that swaps both the binary and its checksum still
// cannot forge a valid signature without this key's secret half.
//
// To enable: generate a key (scripts/sign.sh keygen), set this constant to the
// public key, and configure the release workflow to sign checksums.txt with
// the private key, publishing checksums.txt.sig alongside it.
var ReleaseSigningPublicKey = ""

// ErrNoSignature is returned when signature verification is requested but the
// release has no checksums.txt.sig asset.
var ErrNoSignature = errors.New("update: release has no checksums.txt signature")

// ErrBadSignature is returned when the signature does not verify against the
// pinned public key — a strong signal of a tampered or forged release.
var ErrBadSignature = errors.New("update: checksums signature verification failed")

// signingEnabled reports whether a public key is pinned.
func signingEnabled() bool { return strings.TrimSpace(ReleaseSigningPublicKey) != "" }

// VerifyChecksumsSignature downloads checksums.txt and checksums.txt.sig from
// the release and verifies the Ed25519 signature over the checksums bytes
// against the pinned ReleaseSigningPublicKey. It returns:
//   - nil if signing is disabled (no pinned key) — integrity-only mode, or if
//     the signature verifies;
//   - ErrNoSignature if a key is pinned but the release has no .sig asset;
//   - ErrBadSignature if the signature does not verify.
func VerifyChecksumsSignature(ctx context.Context, release *Release) error {
	if !signingEnabled() {
		return nil // integrity-only mode; SHA-256 still enforced elsewhere
	}
	pub, err := decodeSigningKey(ReleaseSigningPublicKey)
	if err != nil {
		return err
	}

	sums := findAsset(release, "checksums.txt")
	sig := findAsset(release, "checksums.txt.sig")
	if sums == nil {
		return errors.New("update: release has no checksums.txt to verify")
	}
	if sig == nil {
		return ErrNoSignature
	}

	sumsBytes, err := fetchBytes(ctx, sums.DownloadURL)
	if err != nil {
		return fmt.Errorf("update: downloading checksums.txt: %w", err)
	}
	sigBytes, err := fetchBytes(ctx, sig.DownloadURL)
	if err != nil {
		return fmt.Errorf("update: downloading checksums.txt.sig: %w", err)
	}
	rawSig, err := decodeSignature(sigBytes)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, sumsBytes, rawSig) {
		return ErrBadSignature
	}
	return nil
}

// decodeSigningKey parses the pinned base64 Ed25519 public key.
func decodeSigningKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("update: decoding signing public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("update: signing public key must be %d bytes, got %d", ed25519.PublicKeySize, len(b))
	}
	return ed25519.PublicKey(b), nil
}

// decodeSignature parses a signature file that is either raw base64 or
// base64 with surrounding whitespace/newlines.
func decodeSignature(b []byte) ([]byte, error) {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("update: decoding signature: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("update: signature must be %d bytes, got %d", ed25519.SignatureSize, len(sig))
	}
	return sig, nil
}

// fetchBytes GETs a URL with the VORTEX User-Agent and returns its body.
func fetchBytes(ctx context.Context, url string) ([]byte, error) {
	resp, err := httpGet(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
