package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/secrets"
)

// secretStorePath resolves the on-disk path for the local encrypted store. It
// honours VORTEX_SECRET_STORE (used by tests and operators to relocate or share
// the store) and otherwise falls back to <user-cache>/vortex/secrets.
func secretStorePath() string {
	if override := os.Getenv("VORTEX_SECRET_STORE"); override != "" {
		return override
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "vortex", "secrets")
}

// buildAdapterConfig maps cfg.Secrets.Store (the backend kind) plus the
// provider-specific environment variables onto a secrets.AdapterConfig. Per
// Non-Negotiable Rule #2, only the backend *selection* lives in config; all
// credentials and endpoints come from the environment, never from config.
//
// For the local backend the encrypted on-disk store is opened here.
func buildAdapterConfig(cfg *config.Config) (secrets.AdapterConfig, error) {
	kind := cfg.Secrets.Store
	if kind == "" {
		kind = "local"
	}
	ac := secrets.AdapterConfig{Kind: kind}

	switch kind {
	case "local":
		store, err := secrets.NewSecretStore(secretStorePath(), []byte(cfg.Cluster.Name+"-secrets"))
		if err != nil {
			return ac, fmt.Errorf("opening secret store: %w", err)
		}
		ac.Local = store
	case "vault":
		ac.Vault = secrets.VaultConfig{
			Address:   os.Getenv("VAULT_ADDR"),
			Token:     os.Getenv("VAULT_TOKEN"),
			MountPath: os.Getenv("VAULT_MOUNT"),
			Prefix:    os.Getenv("VAULT_PREFIX"),
		}
	case "aws-ssm":
		ac.AWSSSM = secrets.AWSSSMConfig{
			Region:    os.Getenv("AWS_REGION"),
			Prefix:    os.Getenv("AWS_SSM_PREFIX"),
			AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		}
	case "gcp-sm":
		ac.GCPSM = secrets.GCPSMConfig{
			ProjectID: os.Getenv("GCP_PROJECT_ID"),
			Prefix:    os.Getenv("GCP_SM_PREFIX"),
			CredFile:  os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),
		}
	default:
		return ac, fmt.Errorf("unknown secret store backend %q", kind)
	}
	return ac, nil
}

// openSecretAdapter loads the config and constructs the configured secret
// Adapter (local/vault/aws-ssm/gcp-sm).
func openSecretAdapter() (secrets.Adapter, *config.Config, error) {
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return nil, nil, err
	}
	ac, err := buildAdapterConfig(cfg)
	if err != nil {
		return nil, cfg, err
	}
	a, err := secrets.NewAdapter(ac)
	if err != nil {
		return nil, cfg, err
	}
	return a, cfg, nil
}
