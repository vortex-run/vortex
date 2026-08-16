package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/audit"
	"github.com/vortex-run/vortex/internal/config"
)

// isolateKeyPaths points the master key, audit log, and migration marker at a
// temp dir so migration tests never touch the developer's real state.
func isolateKeyPaths(t *testing.T) (auditPath string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("VORTEX_MASTER_KEY_FILE", filepath.Join(dir, "master.key"))
	t.Setenv("VORTEX_AUDIT_LOG", filepath.Join(dir, "audit.log"))
	// Must be isolated too: migrateLegacyKeys probes the secret store as well,
	// and pointing at the developer's real store made these tests depend on
	// whatever happened to be on that machine.
	t.Setenv("VORTEX_SECRET_STORE", filepath.Join(dir, "secrets"))
	// Ensure no inline key short-circuits migrateLegacyKeys.
	t.Setenv("VORTEX_MASTER_KEY", "")
	return filepath.Join(dir, "audit.log")
}

// seedLegacyAuditLog writes an audit log keyed the old way: clusterName + "-audit-key".
func seedLegacyAuditLog(t *testing.T, path, clusterName string) {
	t.Helper()
	l, err := audit.NewLog(path, []byte(clusterName+"-audit-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(context.Background(), "cli", "secret.set", "K", nil); err != nil {
		t.Fatal(err)
	}
}

// TestMigrateLegacyKeys_RetriesWhenClusterRenamed is the regression that
// matters. The legacy key is derived from the CURRENT cluster name, so a log
// written under a different name cannot be re-keyed. Previously the marker was
// written anyway, permanently retiring the only path that could ever fix it —
// leaving the audit log failing verification forever and reporting it as
// tampering. Migration must stay pending instead.
func TestMigrateLegacyKeys_RetriesWhenClusterRenamed(t *testing.T) {
	auditPath := isolateKeyPaths(t)
	seedLegacyAuditLog(t, auditPath, "original-name")

	// Boot under a DIFFERENT cluster name: neither the master-derived key nor
	// "renamed-audit-key" can verify the log.
	cfg := &config.Config{}
	cfg.Cluster.Name = "renamed"
	migrateLegacyKeys(cfg, quietLogger())

	if _, err := os.Stat(migrationMarkerPath()); err == nil {
		t.Error("migration marker was written even though the log could not be re-keyed; " +
			"the migration is now permanently retired and the audit log can never be recovered")
	}
}

// TestMigrateLegacyKeys_SucceedsAndMarksWhenNameMatches proves the retry logic
// did not break the working path: with the correct cluster name the log is
// re-keyed and the marker IS written, so later boots skip the probe.
func TestMigrateLegacyKeys_SucceedsAndMarksWhenNameMatches(t *testing.T) {
	auditPath := isolateKeyPaths(t)
	seedLegacyAuditLog(t, auditPath, "prod-cluster")

	cfg := &config.Config{}
	cfg.Cluster.Name = "prod-cluster"
	migrateLegacyKeys(cfg, quietLogger())

	if _, err := os.Stat(migrationMarkerPath()); err != nil {
		t.Fatalf("marker not written after a successful migration: %v", err)
	}
	// The log must now verify under the master-derived key.
	key, err := deriveKey("audit")
	if err != nil {
		t.Fatal(err)
	}
	l, err := audit.NewLog(auditPath, key)
	if err != nil {
		t.Fatal(err)
	}
	if verr := l.Verify(); verr != nil {
		t.Errorf("log does not verify under the master-derived key after migration: %v", verr)
	}
}

// TestMigrateLegacyKeys_RenameThenCorrectRecovers is the payoff: because the
// marker was withheld, restoring the original cluster name lets a later boot
// complete the migration that the renamed boot could not.
func TestMigrateLegacyKeys_RenameThenCorrectRecovers(t *testing.T) {
	auditPath := isolateKeyPaths(t)
	seedLegacyAuditLog(t, auditPath, "original-name")

	wrong := &config.Config{}
	wrong.Cluster.Name = "renamed"
	migrateLegacyKeys(wrong, quietLogger())

	// Operator notices and restores the original name.
	right := &config.Config{}
	right.Cluster.Name = "original-name"
	migrateLegacyKeys(right, quietLogger())

	key, err := deriveKey("audit")
	if err != nil {
		t.Fatal(err)
	}
	l, err := audit.NewLog(auditPath, key)
	if err != nil {
		t.Fatal(err)
	}
	if verr := l.Verify(); verr != nil {
		t.Errorf("recovery failed: log still does not verify after correcting the name: %v", verr)
	}
	if _, err := os.Stat(migrationMarkerPath()); err != nil {
		t.Errorf("marker should be written once migration finally succeeded: %v", err)
	}
}
