package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// PluginManifest describes an installed plugin.
type PluginManifest struct {
	Name        string     `json:"name"`
	Version     string     `json:"version"` // semver: MAJOR.MINOR.PATCH
	Description string     `json:"description"`
	HookTypes   []HookType `json:"hook_types"`
	Checksum    string     `json:"checksum"`  // SHA-256 hex of the WASM bytes
	Signature   string     `json:"signature"` // ed25519 signature (future use)
}

// Registry stores installed plugins on disk under storePath, laid out as
// <name>/<version>/{plugin.wasm,manifest.json}.
type Registry struct {
	storePath string
}

// NewRegistry opens (creating if needed) a plugin store at storePath.
func NewRegistry(storePath string) (*Registry, error) {
	if storePath == "" {
		return nil, errors.New("plugins: registry storePath must not be empty")
	}
	if err := os.MkdirAll(storePath, 0o700); err != nil {
		return nil, fmt.Errorf("plugins: creating plugin store: %w", err)
	}
	return &Registry{storePath: storePath}, nil
}

// Checksum returns the SHA-256 hex digest of wasm.
func (r *Registry) Checksum(wasm []byte) string {
	sum := sha256.Sum256(wasm)
	return hex.EncodeToString(sum[:])
}

// Install validates the manifest checksum against wasm and stores both. A
// mismatched checksum is rejected.
func (r *Registry) Install(manifest PluginManifest, wasm []byte) error {
	if manifest.Name == "" || manifest.Version == "" {
		return errors.New("plugins: manifest requires name and version")
	}
	if got := r.Checksum(wasm); got != manifest.Checksum {
		return fmt.Errorf("plugins: checksum mismatch: manifest %s, computed %s", manifest.Checksum, got)
	}

	dir := filepath.Join(r.storePath, manifest.Name, manifest.Version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("plugins: creating plugin dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), wasm, 0o600); err != nil {
		return fmt.Errorf("plugins: writing plugin.wasm: %w", err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("plugins: encoding manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		return fmt.Errorf("plugins: writing manifest: %w", err)
	}
	return nil
}

// Get loads the WASM bytes and manifest for name@version. The special version
// "latest" resolves to the highest installed semver.
func (r *Registry) Get(name, version string) ([]byte, *PluginManifest, error) {
	if version == "latest" || version == "" {
		v, err := r.latestVersion(name)
		if err != nil {
			return nil, nil, err
		}
		version = v
	}
	dir := filepath.Join(r.storePath, name, version)

	wasm, err := os.ReadFile(filepath.Join(dir, "plugin.wasm")) //nolint:gosec // path from registry layout
	if err != nil {
		return nil, nil, fmt.Errorf("plugins: reading plugin %s@%s: %w", name, version, err)
	}
	manifest, err := readManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, nil, err
	}
	return wasm, manifest, nil
}

// List returns the manifests of all installed plugin versions.
func (r *Registry) List() []PluginManifest {
	var out []PluginManifest
	names, err := os.ReadDir(r.storePath)
	if err != nil {
		return nil
	}
	for _, n := range names {
		if !n.IsDir() {
			continue
		}
		versions, verr := os.ReadDir(filepath.Join(r.storePath, n.Name()))
		if verr != nil {
			continue
		}
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			m, merr := readManifest(filepath.Join(r.storePath, n.Name(), v.Name(), "manifest.json"))
			if merr == nil {
				out = append(out, *m)
			}
		}
	}
	return out
}

// Remove deletes the given plugin version from the store.
func (r *Registry) Remove(name, version string) error {
	dir := filepath.Join(r.storePath, name, version)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("plugins: %s@%s not installed", name, version)
	}
	return os.RemoveAll(dir)
}

// latestVersion returns the highest semver installed for name.
func (r *Registry) latestVersion(name string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(r.storePath, name))
	if err != nil {
		return "", fmt.Errorf("plugins: no versions for %s: %w", name, err)
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("plugins: no versions installed for %s", name)
	}
	sort.Slice(versions, func(i, j int) bool {
		return compareSemver(versions[i], versions[j]) < 0
	})
	return versions[len(versions)-1], nil
}

// readManifest reads and decodes a manifest.json file.
func readManifest(path string) (*PluginManifest, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path from registry layout
	if err != nil {
		return nil, fmt.Errorf("plugins: reading manifest: %w", err)
	}
	var m PluginManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("plugins: decoding manifest: %w", err)
	}
	return &m, nil
}

// compareSemver compares two MAJOR.MINOR.PATCH versions, returning -1, 0, or 1.
// Non-numeric or missing components are treated as 0.
func compareSemver(a, b string) int {
	pa, pb := semverParts(a), semverParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// semverParts splits a version into its three numeric components.
func semverParts(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	var parts [3]int
	for i, s := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n, _ := strconv.Atoi(strings.SplitN(s, "-", 2)[0]) // drop pre-release suffix
		parts[i] = n
	}
	return parts
}
