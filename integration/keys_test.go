//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/gateway"
	"github.com/vortex-run/vortex/internal/testutil"
)

// keyEncKey is a fixed 32-byte key for the keystore tests.
var keyEncKey = bytes.Repeat([]byte{0x11}, 32)

func TestKeys_StoreAndRetrieve(t *testing.T) {
	store, err := gateway.NewKeyStore(filepath.Join(t.TempDir(), "keys.db"), keyEncKey)
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	slot := gateway.KeySlot{
		ID: "slot-1", Provider: "deepseek", APIKey: "sk-integration-secret",
		Model: "deepseek-chat", Priority: 1, DailyBudget: 5, Enabled: true, Label: "Primary",
	}
	if err := store.Add(slot); err != nil {
		t.Fatalf("Add: %v", err)
	}

	dec, err := store.GetDecrypted("slot-1")
	if err != nil {
		t.Fatalf("GetDecrypted: %v", err)
	}
	if dec.APIKey != "sk-integration-secret" {
		t.Errorf("decrypted key = %q, want the original", dec.APIKey)
	}
	best, err := store.BestSlot()
	if err != nil {
		t.Fatalf("BestSlot: %v", err)
	}
	if best.ID != "slot-1" {
		t.Errorf("BestSlot = %s, want slot-1", best.ID)
	}
}

func TestKeys_HealthScoring(t *testing.T) {
	store, err := gateway.NewKeyStore(filepath.Join(t.TempDir(), "keys.db"), keyEncKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	router := gateway.NewRouter(gateway.RouterConfig{Store: store, Mode: gateway.ModeQuality})
	if err := store.Add(gateway.KeySlot{ID: "slot-1", Provider: "deepseek", APIKey: "k1", Model: "m", Priority: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	// Degrade slot-1: many errors plus a rate limit → score drops below 50
	// (CalcScore: -30 for >5 errors, -50 for rate-limited).
	for i := 0; i < 6; i++ {
		router.RecordError("slot-1", errors.New("boom"), false)
	}
	router.RecordError("slot-1", errors.New("429"), true)
	h, _ := store.GetHealth("slot-1")
	if h.Score >= 50 {
		t.Errorf("after errors + rate limit score = %d, want < 50", h.Score)
	}

	// Add a second healthy slot; BestSlot should prefer it.
	if err := store.Add(gateway.KeySlot{ID: "slot-2", Provider: "groq", APIKey: "k2", Model: "m", Priority: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	best, err := store.BestSlot()
	if err != nil {
		t.Fatalf("BestSlot: %v", err)
	}
	if best.ID != "slot-2" {
		t.Errorf("BestSlot = %s, want slot-2 (slot-1 degraded)", best.ID)
	}
}

func TestKeys_APIEndpoint(t *testing.T) {
	adminSecret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// GET /api/keys/status requires a key.
	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/api/keys/status", nil)
	req.Header.Set("X-API-Key", adminSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/keys/status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (body=%s)", resp.StatusCode, body)
	}
	var out struct {
		Mode  string `json:"mode"`
		Slots []struct {
			ID string `json:"id"`
		} `json:"slots"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// No slots configured in the test env → single-provider mode with an empty
	// (non-nil) slot array.
	if out.Slots == nil {
		t.Error("slots should be an empty array, not null")
	}
}

func TestKeys_APIEndpointRequiresAuth(t *testing.T) {
	_ = seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// No credential → 401.
	resp, err := http.Get(p.APIAddr + "/api/keys/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status without key = %d, want 401", resp.StatusCode)
	}
}
