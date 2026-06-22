package gateway

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// testEncKey is a fixed 32-byte key for tests.
var testEncKey = bytes.Repeat([]byte{0x42}, 32)

func newTestStore(t *testing.T) *KeyStore {
	t.Helper()
	s, err := NewKeyStore(filepath.Join(t.TempDir(), "keys.db"), testEncKey)
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleSlot(id, provider string) KeySlot {
	return KeySlot{
		ID: id, Provider: provider, APIKey: "sk-secret-" + id, Model: provider + "-model",
		Priority: 1, DailyBudget: 10, Enabled: true, Label: "Test " + id,
	}
}

func TestKeyStore_RejectsBadEncKey(t *testing.T) {
	if _, err := NewKeyStore(filepath.Join(t.TempDir(), "k.db"), []byte("short")); err == nil {
		t.Error("expected error for non-32-byte key")
	}
}

func TestKeyStore_AddStoresEncrypted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.db")
	s, err := NewKeyStore(path, testEncKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(sampleSlot("slot-1", "deepseek")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	_ = s.Close()

	// The raw API key must NOT appear anywhere in the DB file.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("sk-secret-slot-1")) {
		t.Error("plaintext API key found in database file")
	}
}

func TestKeyStore_GetDecryptedRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if err := s.Add(sampleSlot("slot-1", "deepseek")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// List returns the key still encrypted.
	slots, _ := s.List()
	if len(slots) != 1 || slots[0].APIKey == "sk-secret-slot-1" {
		t.Errorf("List should return encrypted key, got %q", slots[0].APIKey)
	}
	// GetDecrypted returns the original.
	dec, err := s.GetDecrypted("slot-1")
	if err != nil {
		t.Fatalf("GetDecrypted: %v", err)
	}
	if dec.APIKey != "sk-secret-slot-1" {
		t.Errorf("decrypted key = %q, want sk-secret-slot-1", dec.APIKey)
	}
}

func TestCalcScore(t *testing.T) {
	if got := CalcScore(KeyHealth{}); got != 100 {
		t.Errorf("healthy score = %d, want 100", got)
	}
	if got := CalcScore(KeyHealth{RateLimited: true}); got != 50 {
		t.Errorf("rate-limited score = %d, want 50", got)
	}
	if got := CalcScore(KeyHealth{ErrorsLast10: 6}); got != 70 {
		t.Errorf("high-error score = %d, want 70", got)
	}
	if got := CalcScore(KeyHealth{ErrorsLast10: 3}); got != 85 {
		t.Errorf("mid-error score = %d, want 85", got)
	}
	if got := CalcScore(KeyHealth{AvgLatencyMs: 31000}); got != 80 {
		t.Errorf("high-latency score = %d, want 80", got)
	}
	if got := CalcScore(KeyHealth{AvgLatencyMs: 11000}); got != 90 {
		t.Errorf("mid-latency score = %d, want 90", got)
	}
	// Compounding: rate-limited + high errors + high latency floors at 0.
	if got := CalcScore(KeyHealth{RateLimited: true, ErrorsLast10: 6, AvgLatencyMs: 31000}); got != 0 {
		t.Errorf("compounded penalties = %d, want 0", got)
	}
}

func TestScoreFor_BudgetPenalty(t *testing.T) {
	h := KeyHealth{SpentTodayUSD: 10}
	if got := ScoreFor(h, 10); got != 60 {
		t.Errorf("budget-exceeded score = %d, want 60", got)
	}
	if got := ScoreFor(h, 0); got != 100 {
		t.Errorf("unlimited budget score = %d, want 100", got)
	}
	if got := ScoreFor(KeyHealth{SpentTodayUSD: 5}, 10); got != 100 {
		t.Errorf("under-budget score = %d, want 100", got)
	}
}

func TestKeyStore_BestSlot(t *testing.T) {
	s := newTestStore(t)
	a := sampleSlot("slot-1", "deepseek")
	b := sampleSlot("slot-2", "groq")
	b.Priority = 2
	for _, sl := range []KeySlot{a, b} {
		if err := s.Add(sl); err != nil {
			t.Fatal(err)
		}
	}
	// Degrade slot-1 below slot-2.
	if err := s.UpdateHealth(KeyHealth{SlotID: "slot-1", RateLimited: true}); err != nil {
		t.Fatal(err)
	}
	best, err := s.BestSlot()
	if err != nil {
		t.Fatalf("BestSlot: %v", err)
	}
	if best.ID != "slot-2" {
		t.Errorf("BestSlot = %s, want slot-2 (slot-1 is rate-limited)", best.ID)
	}
}

func TestKeyStore_BestSlotSkipsDisabled(t *testing.T) {
	s := newTestStore(t)
	a := sampleSlot("slot-1", "deepseek")
	a.Enabled = false
	b := sampleSlot("slot-2", "groq")
	for _, sl := range []KeySlot{a, b} {
		if err := s.Add(sl); err != nil {
			t.Fatal(err)
		}
	}
	best, err := s.BestSlot()
	if err != nil {
		t.Fatalf("BestSlot: %v", err)
	}
	if best.ID != "slot-2" {
		t.Errorf("BestSlot = %s, want slot-2 (slot-1 disabled)", best.ID)
	}
}

func TestKeyStore_BestSlotAllDisabled(t *testing.T) {
	s := newTestStore(t)
	a := sampleSlot("slot-1", "deepseek")
	a.Enabled = false
	if err := s.Add(a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BestSlot(); err == nil {
		t.Error("BestSlot should error when all slots disabled")
	}
}

func TestKeyStore_UpdateHealthPersists(t *testing.T) {
	s := newTestStore(t)
	if err := s.Add(sampleSlot("slot-1", "deepseek")); err != nil {
		t.Fatal(err)
	}
	h := KeyHealth{
		SlotID: "slot-1", RequestsToday: 42, ErrorsLast10: 1,
		AvgLatencyMs: 2100, SpentTodayUSD: 0.023, LastUsed: time.Now(),
		LastError: "boom", LastErrorAt: time.Now(),
	}
	if err := s.UpdateHealth(h); err != nil {
		t.Fatalf("UpdateHealth: %v", err)
	}
	got, err := s.GetHealth("slot-1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if got.RequestsToday != 42 || got.AvgLatencyMs != 2100 || got.LastError != "boom" {
		t.Errorf("persisted health = %+v", got)
	}
	// Score is recomputed against the slot's budget on write.
	if got.Score != 100 {
		t.Errorf("score = %d, want 100 (healthy under budget)", got.Score)
	}
}

func TestKeyStore_UpdateHealthBudgetScore(t *testing.T) {
	s := newTestStore(t)
	slot := sampleSlot("slot-1", "deepseek")
	slot.DailyBudget = 1.0
	if err := s.Add(slot); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateHealth(KeyHealth{SlotID: "slot-1", SpentTodayUSD: 1.5}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetHealth("slot-1")
	if got.Score != 60 {
		t.Errorf("over-budget score = %d, want 60", got.Score)
	}
}

func TestKeyStore_ResetDailyStats(t *testing.T) {
	s := newTestStore(t)
	if err := s.Add(sampleSlot("slot-1", "deepseek")); err != nil {
		t.Fatal(err)
	}
	_ = s.UpdateHealth(KeyHealth{
		SlotID: "slot-1", RequestsToday: 100, SpentTodayUSD: 5,
		ErrorsLast10: 4, RateLimited: true,
	})
	if err := s.ResetDailyStats(); err != nil {
		t.Fatalf("ResetDailyStats: %v", err)
	}
	got, _ := s.GetHealth("slot-1")
	if got.RequestsToday != 0 || got.SpentTodayUSD != 0 || got.ErrorsLast10 != 0 || got.RateLimited {
		t.Errorf("daily stats not reset: %+v", got)
	}
	if got.Score != 100 {
		t.Errorf("score after reset = %d, want 100", got.Score)
	}
}

func TestKeyStore_RemoveAndList(t *testing.T) {
	s := newTestStore(t)
	for _, p := range []string{"slot-1", "slot-2"} {
		if err := s.Add(sampleSlot(p, "deepseek")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Remove("slot-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	slots, _ := s.List()
	if len(slots) != 1 || slots[0].ID != "slot-2" {
		t.Errorf("after Remove, List = %+v", slots)
	}
	if err := s.Remove("no-such"); err == nil {
		t.Error("Remove of missing slot should error")
	}
}

func TestKeyStore_AllHealth(t *testing.T) {
	s := newTestStore(t)
	for _, p := range []string{"slot-1", "slot-2"} {
		if err := s.Add(sampleSlot(p, "deepseek")); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.AllHealth()
	if err != nil {
		t.Fatalf("AllHealth: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("AllHealth = %d rows, want 2", len(all))
	}
}

func TestKeyStore_ConcurrentUpdateHealth(t *testing.T) {
	s := newTestStore(t)
	for i := 1; i <= 4; i++ {
		if err := s.Add(sampleSlot(fmt.Sprintf("slot-%d", i), "deepseek")); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			_ = s.UpdateHealth(KeyHealth{SlotID: fmt.Sprintf("slot-%d", n%4+1), RequestsToday: int64(n)})
		}(i)
		go func() {
			defer wg.Done()
			_, _ = s.BestSlot()
		}()
	}
	wg.Wait()
	if all, _ := s.AllHealth(); len(all) != 4 {
		t.Errorf("AllHealth after concurrency = %d, want 4", len(all))
	}
}
