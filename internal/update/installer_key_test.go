package update

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestInstallerPinsReleaseKey: scripts/install.sh verifies checksums.txt.sig
// with its own copy of the release key. If the two drift (a key rotation that
// forgets the installer), the installer would reject every genuine release.
func TestInstallerPinsReleaseKey(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^RELEASE_PUBKEY="([^"]*)"`).FindSubmatch(data)
	if m == nil {
		t.Fatal(`scripts/install.sh has no RELEASE_PUBKEY="..." line`)
	}
	if got := string(m[1]); got != ReleaseSigningPublicKey {
		t.Errorf("install.sh RELEASE_PUBKEY = %q, want ReleaseSigningPublicKey %q", got, ReleaseSigningPublicKey)
	}
}
