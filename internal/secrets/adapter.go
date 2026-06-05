package secrets

import (
	"context"
	"errors"
)

// Adapter is a pluggable secret backend. The local store and the external
// providers (Vault, AWS SSM, GCP Secret Manager) all satisfy it, so the rest of
// VORTEX resolves secrets through one interface regardless of where they live.
type Adapter interface {
	Get(ctx context.Context, name string) (string, error)
	Set(ctx context.Context, name, value string) error
	List(ctx context.Context) ([]string, error)
	Delete(ctx context.Context, name string) error
	// Ping checks connectivity to the backend, returning nil when healthy.
	Ping(ctx context.Context) error
}

// VaultConfig configures the HashiCorp Vault KV v2 adapter.
type VaultConfig struct {
	Address   string // e.g. https://vault.example.com
	Token     string // Vault token (typically from env VAULT_TOKEN)
	MountPath string // KV mount path; default "secret"
	Prefix    string // key prefix; default "vortex/"
}

// AWSSSMConfig configures the AWS SSM Parameter Store adapter.
type AWSSSMConfig struct {
	Region    string // AWS region
	Prefix    string // parameter path prefix; default "/vortex/"
	AccessKey string // from env AWS_ACCESS_KEY_ID
	SecretKey string // from env AWS_SECRET_ACCESS_KEY
}

// GCPSMConfig configures the GCP Secret Manager adapter.
type GCPSMConfig struct {
	ProjectID string // GCP project ID
	Prefix    string // secret name prefix; default "vortex-"
	CredFile  string // path to a service account JSON, or "" for ADC/metadata
}

// AdapterConfig selects and configures a secret backend.
type AdapterConfig struct {
	Kind   string // "local" | "vault" | "aws-ssm" | "gcp-sm"
	Local  *SecretStore
	Vault  VaultConfig
	AWSSSM AWSSSMConfig
	GCPSM  GCPSMConfig
}

// NewAdapter constructs the Adapter selected by cfg.Kind, validating that the
// fields required for that backend are present.
func NewAdapter(cfg AdapterConfig) (Adapter, error) {
	switch cfg.Kind {
	case "local", "":
		if cfg.Local == nil {
			return nil, errors.New("secrets: local adapter requires a SecretStore")
		}
		return NewLocalAdapter(cfg.Local), nil
	case "vault":
		// Validated here; the VaultAdapter constructor (vault.go) does the rest.
		if cfg.Vault.Address == "" {
			return nil, errors.New("secrets: vault adapter requires an Address")
		}
		return newExternalAdapter(cfg)
	case "aws-ssm":
		if cfg.AWSSSM.Region == "" {
			return nil, errors.New("secrets: aws-ssm adapter requires a Region")
		}
		return newExternalAdapter(cfg)
	case "gcp-sm":
		if cfg.GCPSM.ProjectID == "" {
			return nil, errors.New("secrets: gcp-sm adapter requires a ProjectID")
		}
		return newExternalAdapter(cfg)
	default:
		return nil, errors.New("secrets: unknown adapter kind: " + cfg.Kind)
	}
}

// newExternalAdapter constructs the Vault/SSM/GCP adapter for cfg.Kind. The
// concrete constructors are implemented in vault.go, awsssm.go, and gcpsm.go,
// which is why this dispatch lives in its own seam.
func newExternalAdapter(cfg AdapterConfig) (Adapter, error) {
	switch cfg.Kind {
	case "vault":
		return NewVaultAdapter(cfg.Vault)
	case "aws-ssm":
		return NewAWSSSMAdapter(cfg.AWSSSM)
	case "gcp-sm":
		return NewGCPSMAdapter(cfg.GCPSM)
	default:
		return nil, errors.New("secrets: unknown external adapter kind: " + cfg.Kind)
	}
}

// LocalAdapter adapts the on-disk encrypted SecretStore to the Adapter
// interface. It is always available, so Ping never fails.
type LocalAdapter struct {
	store *SecretStore
}

// NewLocalAdapter wraps store in an Adapter.
func NewLocalAdapter(store *SecretStore) *LocalAdapter {
	return &LocalAdapter{store: store}
}

func (a *LocalAdapter) Get(_ context.Context, name string) (string, error) {
	return a.store.Get(name)
}

func (a *LocalAdapter) Set(_ context.Context, name, value string) error {
	return a.store.Set(name, value)
}

func (a *LocalAdapter) List(_ context.Context) ([]string, error) {
	return a.store.List()
}

func (a *LocalAdapter) Delete(_ context.Context, name string) error {
	return a.store.Delete(name)
}

// Ping always succeeds: the local store is on the same host.
func (a *LocalAdapter) Ping(_ context.Context) error { return nil }

// The external-provider constructors below are implemented in their own files
// (vault.go in M3.3 File 2, awsssm.go in File 3, gcpsm.go in File 4). Until each
// lands they are stubbed here so the package builds; each later file removes its
// stub and provides the real constructor.

// NewVaultAdapter is implemented in vault.go (M3.3 File 2).
func NewVaultAdapter(VaultConfig) (Adapter, error) {
	return nil, errors.New("secrets: vault adapter not yet implemented")
}

// NewAWSSSMAdapter is implemented in awsssm.go (M3.3 File 3).
func NewAWSSSMAdapter(AWSSSMConfig) (Adapter, error) {
	return nil, errors.New("secrets: aws-ssm adapter not yet implemented")
}

// NewGCPSMAdapter is implemented in gcpsm.go (M3.3 File 4).
func NewGCPSMAdapter(GCPSMConfig) (Adapter, error) {
	return nil, errors.New("secrets: gcp-sm adapter not yet implemented")
}
