package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// installFixture installs a plugin with the given name/version and returns the
// wasm bytes used.
func installFixture(t *testing.T, r *Registry, name, version string) []byte {
	t.Helper()
	wasm := buildHookWASM([]byte(`{"allow":true}`))
	m := PluginManifest{
		Name: name, Version: version, Description: "test",
		HookTypes: []HookType{HookPreRequest},
		Checksum:  r.Checksum(wasm),
	}
	if err := r.Install(m, wasm); err != nil {
		t.Fatalf("Install %s@%s: %v", name, version, err)
	}
	return wasm
}

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegistry_InstallStores(t *testing.T) {
	r := newRegistry(t)
	installFixture(t, r, "redirect", "1.0.0")
	if got := len(r.List()); got != 1 {
		t.Errorf("List len = %d, want 1", got)
	}
}

func TestRegistry_GetByNameVersion(t *testing.T) {
	r := newRegistry(t)
	want := installFixture(t, r, "redirect", "1.2.3")
	wasm, m, err := r.Get("redirect", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "redirect" || m.Version != "1.2.3" {
		t.Errorf("manifest = %+v", m)
	}
	if len(wasm) != len(want) {
		t.Errorf("wasm len = %d, want %d", len(wasm), len(want))
	}
}

func TestRegistry_GetLatest(t *testing.T) {
	r := newRegistry(t)
	installFixture(t, r, "p", "1.0.0")
	installFixture(t, r, "p", "1.10.0")
	installFixture(t, r, "p", "1.2.0")
	installFixture(t, r, "p", "2.0.0")

	_, m, err := r.Get("p", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "2.0.0" {
		t.Errorf("latest = %s, want 2.0.0", m.Version)
	}
}

func TestRegistry_ListAllInstalled(t *testing.T) {
	r := newRegistry(t)
	installFixture(t, r, "a", "1.0.0")
	installFixture(t, r, "b", "1.0.0")
	installFixture(t, r, "b", "2.0.0")
	if got := len(r.List()); got != 3 {
		t.Errorf("List len = %d, want 3", got)
	}
}

func TestRegistry_Remove(t *testing.T) {
	r := newRegistry(t)
	installFixture(t, r, "gone", "1.0.0")
	if err := r.Remove("gone", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Get("gone", "1.0.0"); err == nil {
		t.Error("Get after Remove should fail")
	}
	// Removing a missing plugin errors.
	if err := r.Remove("nope", "1.0.0"); err == nil {
		t.Error("Remove of missing plugin should error")
	}
}

func TestRegistry_ChecksumMatchesSHA256(t *testing.T) {
	r := newRegistry(t)
	wasm := []byte("hello wasm")
	got := r.Checksum(wasm)

	// Cross-check against a direct crypto/sha256 computation.
	sum := sha256.Sum256(wasm)
	if got != hex.EncodeToString(sum[:]) {
		t.Errorf("Checksum = %s, does not match crypto/sha256", got)
	}
	if len(got) != 64 {
		t.Errorf("checksum length = %d, want 64 hex chars", len(got))
	}
}

func TestRegistry_InstallChecksumMismatch(t *testing.T) {
	r := newRegistry(t)
	wasm := buildHookWASM([]byte(`{"allow":true}`))
	m := PluginManifest{
		Name: "bad", Version: "1.0.0",
		Checksum: "deadbeef", // wrong
	}
	if err := r.Install(m, wasm); err == nil {
		t.Error("Install with mismatched checksum should fail")
	}
}
