package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/vortex-run/vortex/internal/audit"
	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/keyring"
	"github.com/vortex-run/vortex/internal/secrets"
)

// masterKeyPath resolves the on-disk location of the master key, honouring
// VORTEX_MASTER_KEY_FILE and otherwise defaulting to <user-config>/vortex/
// master.key. The config dir (not the cache dir) is used so the key survives
// cache clears. VORTEX_MASTER_KEY (inline) takes precedence inside keyring.
func masterKeyPath() string {
	if override := os.Getenv("VORTEX_MASTER_KEY_FILE"); override != "" {
		return override
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "vortex", "master.key")
}

// sharedKeyring loads (or creates on first run) the process master key once
// and caches it, so the secret store, TLS/mTLS stores, and audit log all
// derive their at-rest keys from the same root. Both `vortex start` and the
// `vortex secret`/`vortex audit` CLIs call this, so they agree on every key.
var (
	keyringOnce sync.Once
	keyringInst *keyring.Keyring
	keyringErr  error
)

func sharedKeyring() (*keyring.Keyring, error) {
	keyringOnce.Do(func() {
		keyringInst, keyringErr = keyring.LoadOrCreate(masterKeyPath())
		if keyringErr != nil {
			keyringErr = fmt.Errorf("loading master key: %w", keyringErr)
		}
	})
	return keyringInst, keyringErr
}

// deriveKey returns the at-rest key for a purpose, derived from the master
// key. Purposes are stable labels: "secrets", "tls-store", "mtls-store",
// "audit".
func deriveKey(purpose string) ([]byte, error) {
	kr, err := sharedKeyring()
	if err != nil {
		return nil, err
	}
	return kr.Subkey(purpose), nil
}

// pathExists reports whether a non-empty file or directory is present. It
// distinguishes "data exists that we could not migrate" (keep migration
// pending) from "nothing to migrate" (retire it) — without the distinction, a
// fresh install would hold the migration open on every boot forever.
func pathExists(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	if info.IsDir() {
		entries, rerr := os.ReadDir(p)
		return rerr == nil && len(entries) > 0
	}
	return info.Size() > 0
}

// migrationMarkerPath is the file whose presence records that the legacy
// cluster-name → master-key migration has already run.
func migrationMarkerPath() string {
	return masterKeyPath() + ".migrated"
}

// migrateLegacyKeys is a one-time migration (production audit C1) that
// re-keys the local secret store and the audit log from the old cluster-name-
// derived keys onto master-key-derived keys. It is a no-op when:
//   - the marker file already exists (migration done), or
//   - VORTEX_MASTER_KEY is set (operator manages the key; no legacy data
//     assumption), or
//   - the data already decrypts/verifies under the new key.
//
// It is best-effort: failures are logged but never block startup, and the
// marker is only written after a clean pass so a partial migration is retried.
func migrateLegacyKeys(cfg *config.Config, log *slog.Logger) {
	if os.Getenv(keyring.EnvMasterKey) != "" {
		return // operator-supplied key; no legacy on-disk derivation to migrate
	}
	if _, err := os.Stat(migrationMarkerPath()); err == nil {
		return // already migrated
	}

	legacy := cfg.Cluster.Name
	migrated := false
	// pending records that we found data the current key cannot read and the
	// legacy key could not read either, so migration is still owed. The marker
	// is withheld in that case so the next boot retries (see the end of this
	// function).
	pending := false

	// --- secret store ------------------------------------------------------
	if newKey, err := deriveKey("secrets"); err == nil {
		store, serr := secrets.NewSecretStore(secretStorePath(), newKey)
		if serr == nil && !store.CanDecrypt() {
			// Not on the new key — try the legacy key and re-encrypt.
			legacyStore, lerr := secrets.NewSecretStore(secretStorePath(), []byte(legacy+"-secrets"))
			if lerr == nil && legacyStore.CanDecrypt() {
				if rerr := legacyStore.Rekey(newKey); rerr != nil {
					log.Warn("secret store key migration failed", "err", rerr)
					pending = true
				} else {
					log.Info("migrated secret store to master-derived key")
					migrated = true
				}
			} else if pathExists(secretStorePath()) {
				// Data is present but readable by neither key — written under
				// some other cluster name. Keep migration pending rather than
				// retiring it. (An absent or empty store has nothing to
				// migrate, so it must NOT hold the migration open forever.)
				pending = true
			}
		}
	}

	// --- audit log ---------------------------------------------------------
	if newKey, err := deriveKey("audit"); err == nil {
		path := auditLogPath()
		newLog, nerr := audit.NewLog(path, newKey)
		if nerr == nil && !newLog.Verifies() {
			legacyLog, lerr := audit.NewLog(path, []byte(legacy+"-audit-key"))
			if lerr == nil && legacyLog.Verifies() {
				if rerr := legacyLog.Rekey(newKey); rerr != nil {
					log.Warn("audit log key migration failed", "err", rerr)
					pending = true
				} else {
					log.Info("migrated audit log to master-derived key")
					migrated = true
				}
			} else if pathExists(path) && errors.Is(newLog.Verify(), audit.ErrChainUnverified) {
				// The chain fails at entry 1 under both keys, which a wrong key
				// explains as readily as tampering. Stay pending so a corrected
				// cluster name can still re-key it; a genuinely tampered log
				// simply keeps failing, which is the correct outcome.
				pending = true
			}
		}
	}

	// Record completion (write the marker even when nothing needed migrating,
	// so subsequent boots skip the probe) — EXCEPT when something clearly
	// needed migrating and we could not do it.
	//
	// The legacy key is derived from the CURRENT cluster name, so if the log
	// was written under a different name (a rename, or a config restored from
	// elsewhere) the legacy probe misses. Writing the marker anyway retired
	// the only path that could ever re-key it: the audit log then failed
	// verification forever, reporting "entry modified" as though it had been
	// tampered with, recoverable only by deleting an undocumented marker file.
	// Leaving the marker unwritten costs two verification passes on the next
	// boot and keeps the migration reachable once the name is corrected.
	if pending {
		log.Warn("key migration incomplete: data exists that this key cannot read, "+
			"and the legacy cluster-name key did not match either — leaving migration "+
			"pending so it can retry",
			"cluster", legacy, "hint", "if the cluster was renamed, set it back to the original name and restart")
		return
	}
	if err := os.WriteFile(migrationMarkerPath(), []byte("1\n"), 0o600); err != nil {
		log.Warn("writing key migration marker failed", "err", err)
	} else if migrated {
		log.Info("key migration complete (legacy cluster-name keys retired)")
	}
}
