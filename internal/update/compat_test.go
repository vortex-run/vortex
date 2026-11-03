package update

import "testing"

// TestCompat_AssetNames pins the archive names that already-installed
// binaries (v0.1.0–v0.3.0) look for when they self-update. A rename must keep
// publishing these names for at least one bridge release, or existing
// installs can't update. See the Gubern migration design.
func TestCompat_AssetNames(t *testing.T) {
	for _, c := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "vortex_linux_amd64.tar.gz"},
		{"linux", "arm64", "vortex_linux_arm64.tar.gz"},
		{"darwin", "arm64", "vortex_darwin_arm64.tar.gz"},
		{"darwin", "amd64", "vortex_darwin_amd64.tar.gz"},
		{"windows", "amd64", "vortex_windows_amd64.zip"},
	} {
		if got := AssetName(c.goos, c.goarch); got != c.want {
			t.Errorf("AssetName(%s, %s) = %q, want %q", c.goos, c.goarch, got, c.want)
		}
	}
}
