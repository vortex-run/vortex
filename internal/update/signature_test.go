package update

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// signTestServer serves checksums.txt and (optionally) checksums.txt.sig and
// returns a Release pointing at them.
func signTestServer(t *testing.T, sums, sig []byte, withSig bool) *Release {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(sums) })
	if withSig {
		mux.HandleFunc("/checksums.txt.sig", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(sig) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rel := &Release{Tag: "v1.0.0", Assets: []Asset{
		{Name: "checksums.txt", DownloadURL: srv.URL + "/checksums.txt"},
	}}
	if withSig {
		rel.Assets = append(rel.Assets, Asset{Name: "checksums.txt.sig", DownloadURL: srv.URL + "/checksums.txt.sig"})
	}
	return rel
}

func withPinnedKey(t *testing.T, pub ed25519.PublicKey) {
	t.Helper()
	old := ReleaseSigningPublicKey
	ReleaseSigningPublicKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { ReleaseSigningPublicKey = old })
}

func TestVerifyChecksumsSignature_DisabledWhenNoKey(t *testing.T) {
	old := ReleaseSigningPublicKey
	ReleaseSigningPublicKey = ""
	t.Cleanup(func() { ReleaseSigningPublicKey = old })

	rel := signTestServer(t, []byte("sums"), nil, false)
	if err := VerifyChecksumsSignature(context.Background(), rel); err != nil {
		t.Errorf("with no pinned key, verification should be a no-op: %v", err)
	}
}

func TestVerifyChecksumsSignature_ValidSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	withPinnedKey(t, pub)

	sums := []byte("abc123  vortex_linux_amd64.tar.gz\n")
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)) + "\n")

	rel := signTestServer(t, sums, sig, true)
	if err := VerifyChecksumsSignature(context.Background(), rel); err != nil {
		t.Errorf("valid signature should verify: %v", err)
	}
}

func TestVerifyChecksumsSignature_TamperedChecksums(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	withPinnedKey(t, pub)

	signed := []byte("original")
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signed)))
	// Serve DIFFERENT checksums than what was signed.
	rel := signTestServer(t, []byte("tampered"), sig, true)

	if err := VerifyChecksumsSignature(context.Background(), rel); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered checksums should fail with ErrBadSignature, got %v", err)
	}
}

func TestVerifyChecksumsSignature_MissingSigWhenKeyPinned(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	withPinnedKey(t, pub)

	rel := signTestServer(t, []byte("sums"), nil, false)
	if err := VerifyChecksumsSignature(context.Background(), rel); !errors.Is(err, ErrNoSignature) {
		t.Errorf("pinned key + no signature should fail with ErrNoSignature, got %v", err)
	}
}

func TestVerifyChecksumsSignature_WrongKey(t *testing.T) {
	// Sign with one key, pin a different one.
	_, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	withPinnedKey(t, otherPub)

	sums := []byte("sums")
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)))
	rel := signTestServer(t, sums, sig, true)

	if err := VerifyChecksumsSignature(context.Background(), rel); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong key should fail with ErrBadSignature, got %v", err)
	}
}

func TestDecodeSigningKey_RejectsBadKey(t *testing.T) {
	if _, err := decodeSigningKey("not-base64!!"); err == nil {
		t.Error("invalid base64 key should error")
	}
	if _, err := decodeSigningKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Error("wrong-size key should error")
	}
}

func TestDecodeSignature_RejectsBadSig(t *testing.T) {
	if _, err := decodeSignature([]byte("@@@")); err == nil {
		t.Error("invalid base64 signature should error")
	}
	if _, err := decodeSignature([]byte(strings.Repeat("A", 10))); err == nil {
		t.Error("wrong-size signature should error")
	}
}
