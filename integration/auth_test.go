//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/testutil"
)

// seedAdminKey issues an admin API key, saves the key store to a temp file, and
// points VORTEX_APIKEY_STORE at it so the started server loads it. It returns
// the plaintext admin secret.
func seedAdminKey(t *testing.T) string {
	t.Helper()
	store := auth.NewAPIKeyStore()
	_, secret, err := store.Issue("admin", "default", []auth.Role{auth.RoleAdmin}, "integration admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "apikeys.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VORTEX_APIKEY_STORE", path)
	return secret
}

func TestAuth_HealthPublic(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// /health is always public — no credential supplied.
	resp, err := http.Get(p.APIAddr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/health status = %d, want 200", resp.StatusCode)
	}

	if logs := p.Logs(); !contains(logs, "auth middleware enabled") {
		t.Errorf("startup log should report auth middleware enabled:\n%s", logs)
	}
}

func TestAuth_KeysEndpointRequiresAuth(t *testing.T) {
	_ = seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// GET /api/keys without a credential must be rejected (admin-only).
	resp, err := http.Get(p.APIAddr + "/api/keys")
	if err != nil {
		t.Fatalf("GET /api/keys: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/keys without auth = %d, want 401", resp.StatusCode)
	}
}

func TestAuth_APIKeyCreateAndUse(t *testing.T) {
	adminSecret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// Create a new operator key using the admin key.
	body, _ := json.Marshal(map[string]any{
		"user_id": "ci", "org_id": "default", "roles": []string{"operator"}, "description": "ci",
	})
	req, _ := http.NewRequest(http.MethodPost, p.APIAddr+"/api/keys", bytes.NewReader(body))
	req.Header.Set("X-API-Key", adminSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/keys: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/keys = %d, want 201; body=%s", resp.StatusCode, raw)
	}
	var created struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Secret == "" {
		t.Fatalf("created key missing id/secret: %+v", created)
	}

	// The admin key can list keys and see the newly created one.
	lreq, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/api/keys?org=default", nil)
	lreq.Header.Set("X-API-Key", adminSecret)
	lresp, err := http.DefaultClient.Do(lreq)
	if err != nil {
		t.Fatalf("GET /api/keys: %v", err)
	}
	defer func() { _ = lresp.Body.Close() }()
	if lresp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/keys = %d, want 200", lresp.StatusCode)
	}
	raw, _ := io.ReadAll(lresp.Body)
	if !contains(string(raw), created.ID) {
		t.Errorf("list response missing created key %s:\n%s", created.ID, raw)
	}
}

// contains is a tiny substring helper to avoid importing strings in every test.
func contains(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
