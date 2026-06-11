package keyring

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreate_GeneratesAndPersists(t *testing.T) {
	t.Setenv(EnvMasterKey, "")
	path := filepath.Join(t.TempDir(), "sub", "master.key")

	kr, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	// The file is created with the generated key.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("master key file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("master key file is empty")
	}

	// Reloading yields the same derived keys (key is stable on disk).
	kr2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(kr.Subkey("secrets")) != hex.EncodeToString(kr2.Subkey("secrets")) {
		t.Error("reloaded keyring derives a different subkey")
	}
}

func TestLoadOrCreate_EnvOverrideHex(t *testing.T) {
	key := make([]byte, MasterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMasterKey, hex.EncodeToString(key))
	path := filepath.Join(t.TempDir(), "master.key")

	kr, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	// Env override must NOT write a file.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("env override should not persist a key file")
	}
	// Matches a keyring built directly from the same master.
	direct, _ := FromMaster(key)
	if hex.EncodeToString(kr.Subkey("audit")) != hex.EncodeToString(direct.Subkey("audit")) {
		t.Error("env-keyed subkey differs from direct master subkey")
	}
}

func TestLoadOrCreate_EnvOverrideBase64(t *testing.T) {
	key := make([]byte, MasterKeySize)
	_, _ = io.ReadFull(rand.Reader, key)
	t.Setenv(EnvMasterKey, base64.StdEncoding.EncodeToString(key))

	kr, err := LoadOrCreate(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	direct, _ := FromMaster(key)
	if hex.EncodeToString(kr.Subkey("tls-store")) != hex.EncodeToString(direct.Subkey("tls-store")) {
		t.Error("base64 env key derives a different subkey")
	}
}

func TestLoadOrCreate_RejectsBadEnvKey(t *testing.T) {
	t.Setenv(EnvMasterKey, "not-a-valid-key")
	if _, err := LoadOrCreate(filepath.Join(t.TempDir(), "master.key")); err == nil {
		t.Error("expected error for malformed VORTEX_MASTER_KEY")
	}
}

func TestSubkey_PurposeIsolationAndDeterminism(t *testing.T) {
	key := make([]byte, MasterKeySize)
	_, _ = io.ReadFull(rand.Reader, key)
	kr, _ := FromMaster(key)

	secrets := kr.Subkey("secrets")
	audit := kr.Subkey("audit")

	if len(secrets) != 32 || len(audit) != 32 {
		t.Fatalf("subkeys must be 32 bytes, got %d/%d", len(secrets), len(audit))
	}
	if hex.EncodeToString(secrets) == hex.EncodeToString(audit) {
		t.Error("distinct purposes must yield distinct keys")
	}
	if hex.EncodeToString(secrets) != hex.EncodeToString(kr.Subkey("secrets")) {
		t.Error("same purpose must yield the same key")
	}
}

func TestFromMaster_RejectsWrongSize(t *testing.T) {
	if _, err := FromMaster([]byte("short")); err == nil {
		t.Error("expected error for non-32-byte master key")
	}
}

func TestSubkey_DiffersByMaster(t *testing.T) {
	a := make([]byte, MasterKeySize)
	b := make([]byte, MasterKeySize)
	_, _ = io.ReadFull(rand.Reader, a)
	_, _ = io.ReadFull(rand.Reader, b)
	ka, _ := FromMaster(a)
	kb, _ := FromMaster(b)
	if hex.EncodeToString(ka.Subkey("secrets")) == hex.EncodeToString(kb.Subkey("secrets")) {
		t.Error("different masters must derive different subkeys")
	}
}
