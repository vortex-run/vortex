package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// vaultTimeout bounds each Vault HTTP request.
const vaultTimeout = 10 * time.Second

// VaultAdapter is a secret Adapter backed by HashiCorp Vault's KV v2 engine,
// implemented directly over the HTTP API (no Vault SDK).
type VaultAdapter struct {
	cfg     VaultConfig
	client  *http.Client
	baseURL string // <Address>/v1/<mount>/data/<prefix>
	metaURL string // <Address>/v1/<mount>/metadata/<prefix>
}

// NewVaultAdapter builds a VaultAdapter. Address is required; MountPath defaults
// to "secret" and Prefix to "vortex/".
func NewVaultAdapter(cfg VaultConfig) (Adapter, error) {
	if cfg.Address == "" {
		return nil, errors.New("secrets: vault adapter requires an Address")
	}
	if cfg.MountPath == "" {
		cfg.MountPath = "secret"
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "vortex/"
	}
	addr := strings.TrimRight(cfg.Address, "/")
	return &VaultAdapter{
		cfg:     cfg,
		client:  &http.Client{Timeout: vaultTimeout},
		baseURL: addr + "/v1/" + cfg.MountPath + "/data/" + cfg.Prefix,
		metaURL: addr + "/v1/" + cfg.MountPath + "/metadata/" + cfg.Prefix,
	}, nil
}

// do issues an authenticated request to Vault.
func (v *VaultAdapter) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", v.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	return v.client.Do(req)
}

// Get returns the value stored at name (the "value" field of the KV v2 entry).
func (v *VaultAdapter) Get(ctx context.Context, name string) (string, error) {
	resp, err := v.do(ctx, http.MethodGet, v.baseURL+name, nil)
	if err != nil {
		return "", fmt.Errorf("secrets: vault get %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", os.ErrNotExist
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("secrets: vault get %s returned %s", name, resp.Status)
	}

	var out struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("secrets: decoding vault response: %w", err)
	}
	val, ok := out.Data.Data["value"]
	if !ok {
		return "", fmt.Errorf("secrets: vault entry %s has no 'value' field", name)
	}
	return val, nil
}

// Set writes value at name as the KV v2 "value" field.
func (v *VaultAdapter) Set(ctx context.Context, name, value string) error {
	body := map[string]any{"data": map[string]string{"value": value}}
	resp, err := v.do(ctx, http.MethodPost, v.baseURL+name, body)
	if err != nil {
		return fmt.Errorf("secrets: vault set %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("secrets: vault set %s returned %s", name, resp.Status)
	}
	return nil
}

// List returns the secret names under the prefix.
func (v *VaultAdapter) List(ctx context.Context) ([]string, error) {
	// KV v2 lists keys under the metadata path with the LIST method.
	resp, err := v.do(ctx, "LIST", v.metaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: vault list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return []string{}, nil // no secrets stored yet
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("secrets: vault list returned %s", resp.Status)
	}
	var out struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("secrets: decoding vault list: %w", err)
	}
	return out.Data.Keys, nil
}

// Delete removes name. It is idempotent (404 is not an error).
func (v *VaultAdapter) Delete(ctx context.Context, name string) error {
	resp, err := v.do(ctx, http.MethodDelete, v.metaURL+name, nil)
	if err != nil {
		return fmt.Errorf("secrets: vault delete %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("secrets: vault delete %s returned %s", name, resp.Status)
	}
	return nil
}

// Ping checks Vault health. 200 (active) and 429 (standby) both mean reachable.
func (v *VaultAdapter) Ping(ctx context.Context) error {
	url := strings.TrimRight(v.cfg.Address, "/") + "/v1/sys/health"
	resp, err := v.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("secrets: vault ping: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusTooManyRequests {
		return nil
	}
	return fmt.Errorf("secrets: vault unhealthy: %s", resp.Status)
}
