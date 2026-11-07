package update

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Audit F-12: the checksum map that gates an install must come from the bytes
// the signature covers. A host that serves the signed checksums.txt on the
// first request and different content afterwards must not get its unsigned
// hash used. Before the fix runSelfUpdate verified one download and parsed a
// second one.
func TestVerifiedChecksums_UsesTheVerifiedBytes(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	withPinnedKey(t, pub)

	good := []byte("1111111111111111111111111111111111111111111111111111111111111111  gubern_linux_amd64.tar.gz\n")
	evil := []byte("2222222222222222222222222222222222222222222222222222222222222222  gubern_linux_amd64.tar.gz\n")
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, good)))

	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			_, _ = w.Write(good)
			return
		}
		_, _ = w.Write(evil)
	})
	mux.HandleFunc("/checksums.txt.sig", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(sig) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	rel := &Release{Tag: "v2.0.0", Assets: []Asset{
		{Name: "checksums.txt", DownloadURL: srv.URL + "/checksums.txt"},
		{Name: "checksums.txt.sig", DownloadURL: srv.URL + "/checksums.txt.sig"},
	}}

	sums, err := VerifiedChecksums(context.Background(), rel)
	if err != nil {
		t.Fatalf("VerifiedChecksums: %v", err)
	}
	if got := sums["gubern_linux_amd64.tar.gz"]; got[0] != '1' {
		t.Fatalf("hash = %s, want the signed one", got)
	}
	if hits.Load() != 1 {
		t.Errorf("checksums.txt fetched %d times, want exactly 1", hits.Load())
	}
	// FetchChecksums (used by `gubern verify`) is the verified path too: the
	// second body no longer matches the signature, so it must be refused.
	if _, err := FetchChecksums(context.Background(), rel); !errors.Is(err, ErrBadSignature) {
		t.Errorf("FetchChecksums on unsigned bytes = %v, want ErrBadSignature", err)
	}
}

func TestVerifiedChecksums_RequiresSignatureWhenPinned(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	withPinnedKey(t, pub)
	rel := signTestServer(t, []byte("abc  f\n"), nil, false)
	if _, err := VerifiedChecksums(context.Background(), rel); !errors.Is(err, ErrNoSignature) {
		t.Errorf("missing .sig = %v, want ErrNoSignature", err)
	}
}
