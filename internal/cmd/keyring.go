package cmd

import (
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

	// --- secret store ------------------------------------------------------
	if newKey, err := deriveKey("secrets"); err == nil {
		store, serr := secrets.NewSecretStore(secretStorePath(), newKey)
		if serr == nil && !store.CanDecrypt() {
			// Not on the new key — try the legacy key and re-encrypt.
			legacyStore, lerr := secrets.NewSecretStore(secretStorePath(), []byte(legacy+"-secrets"))
			if lerr == nil && legacyStore.CanDecrypt() {
				if rerr := legacyStore.Rekey(newKey); rerr != nil {
					log.Warn("secret store key migration failed", "err", rerr)
				} else {
					log.Info("migrated secret store to master-derived key")
					migrated = true
				}
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
				} else {
					log.Info("migrated audit log to master-derived key")
					migrated = true
				}
			}
		}
	}

	// Record completion (write the marker even when nothing needed migrating,
	// so subsequent boots skip the probe). Only skip on a hard marker-write
	// failure, which is itself logged.
	if err := os.WriteFile(migrationMarkerPath(), []byte("1\n"), 0o600); err != nil {
		log.Warn("writing key migration marker failed", "err", err)
	} else if migrated {
		log.Info("key migration complete (legacy cluster-name keys retired)")
	}
}
