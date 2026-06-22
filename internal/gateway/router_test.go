package gateway

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// mockNotifier records alerts for assertions.
type mockNotifier struct {
	mu     sync.Mutex
	alerts []string
}

func (m *mockNotifier) Notify(title, body string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts = append(m.alerts, title+"\n"+body)
}

func (m *mockNotifier) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.alerts)
}

func (m *mockNotifier) last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.alerts) == 0 {
		return ""
	}
	return m.alerts[len(m.alerts)-1]
}

// routerFixture builds a store with the given slots plus a router over it.
func routerFixture(t *testing.T, mode BudgetMode, slots ...KeySlot) (*Router, *KeyStore, *mockNotifier) {
	t.Helper()
	store, err := NewKeyStore(filepath.Join(t.TempDir(), "keys.db"), testEncKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, s := range slots {
		if err := store.Add(s); err != nil {
			t.Fatal(err)
		}
	}
	notif := &mockNotifier{}
	r := NewRouter(RouterConfig{Store: store, Mode: mode, Notifier: notif})
	return r, store, notif
}

func slot(id, provider string, priority int) KeySlot {
	return KeySlot{ID: id, Provider: provider, APIKey: "k-" + id, Model: provider, Priority: priority, Enabled: true}
}

func TestRouter_SelectHighestScoreQuality(t *testing.T) {
	r, store, _ := routerFixture(t, ModeQuality,
		slot("slot-1", "deepseek", 1), slot("slot-2", "claude", 2))
	// Degrade slot-1.
	_ = store.UpdateHealth(KeyHealth{SlotID: "slot-1", ErrorsLast10: 6})
	got, err := r.SelectSlot(context.Background(), "complex")
	if err != nil {
		t.Fatalf("SelectSlot: %v", err)
	}
	if got.ID != "slot-2" {
		t.Errorf("quality mode chose %s, want slot-2 (higher score)", got.ID)
	}
}

func TestRouter_SelectCheapest(t *testing.T) {
	r, _, _ := routerFixture(t, ModeCheap,
		slot("slot-1", "claude", 1), slot("slot-2", "groq", 2))
	got, err := r.SelectSlot(context.Background(), "simple")
	if err != nil {
		t.Fatalf("SelectSlot: %v", err)
	}
	if got.Provider != "groq" {
		t.Errorf("cheap mode chose %s, want groq (cheapest)", got.Provider)
	}
}

func TestRouter_SelectFastest(t *testing.T) {
	r, store, _ := routerFixture(t, ModeFast,
		slot("slot-1", "deepseek", 1), slot("slot-2", "groq", 2))
	_ = store.UpdateHealth(KeyHealth{SlotID: "slot-1", AvgLatencyMs: 5000})
	_ = store.UpdateHealth(KeyHealth{SlotID: "slot-2", AvgLatencyMs: 400})
	got, _ := r.SelectSlot(context.Background(), "simple")
	if got.ID != "slot-2" {
		t.Errorf("fast mode chose %s, want slot-2 (lower latency)", got.ID)
	}
}

func TestRouter_SkipsLowScoreSlots(t *testing.T) {
	r, store, _ := routerFixture(t, ModeQuality,
		slot("slot-1", "deepseek", 1), slot("slot-2", "groq", 2))
	// slot-1 below the usable floor (rate-limited + errors → score < 30).
	_ = store.UpdateHealth(KeyHealth{SlotID: "slot-1", RateLimited: true, ErrorsLast10: 6, AvgLatencyMs: 31000})
	got, _ := r.SelectSlot(context.Background(), "complex")
	if got.ID != "slot-2" {
		t.Errorf("chose %s, want slot-2 (slot-1 below usable floor)", got.ID)
	}
}

func TestRouter_AutoFallsBackWhenPreferredUnavailable(t *testing.T) {
	// Only a mid-score slot exists; "complex" wants >70 but must still get a slot.
	r, store, _ := routerFixture(t, ModeAuto, slot("slot-1", "deepseek", 1))
	_ = store.UpdateHealth(KeyHealth{SlotID: "slot-1", ErrorsLast10: 3}) // score 85 ... still >70
	// Drop it to ~55 (above usable floor, below the complex gate of 70).
	_ = store.UpdateHealth(KeyHealth{SlotID: "slot-1", AvgLatencyMs: 11000, ErrorsLast10: 3}) // 100-10-15=75
	got, err := r.SelectSlot(context.Background(), "complex")
	if err != nil {
		t.Fatalf("SelectSlot: %v", err)
	}
	if got.ID != "slot-1" {
		t.Errorf("auto should fall back to the only working slot, got %v", got)
	}
}

func TestRouter_NoUsableSlotsErrors(t *testing.T) {
	r, store, _ := routerFixture(t, ModeAuto, slot("slot-1", "deepseek", 1))
	_ = store.UpdateHealth(KeyHealth{SlotID: "slot-1", RateLimited: true, ErrorsLast10: 6})
	if _, err := r.SelectSlot(context.Background(), "simple"); err == nil {
		t.Error("expected error when no slot is usable")
	}
}

func TestRouter_RecordErrorIncrementsAndRateLimits(t *testing.T) {
	r, store, notif := routerFixture(t, ModeAuto, slot("slot-1", "deepseek", 1))

	r.RecordError("slot-1", errors.New("boom"), false)
	h, _ := store.GetHealth("slot-1")
	if h.ErrorsLast10 != 1 || h.LastError != "boom" {
		t.Errorf("after error: %+v", h)
	}
	if notif.count() != 0 {
		t.Error("a non-rate-limit error should not alert")
	}

	r.RecordError("slot-1", errors.New("429"), true)
	h, _ = store.GetHealth("slot-1")
	if !h.RateLimited {
		t.Error("rate-limit error should set RateLimited")
	}
	if notif.count() != 1 {
		t.Fatalf("rate limit should alert once, got %d", notif.count())
	}
	if a := notif.last(); !contains(a, "rate limited") || !contains(a, "deepseek") {
		t.Errorf("alert = %q", a)
	}
}

func TestRouter_RateLimitBackoffGrows(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1700000000, 0)}
	store, _ := NewKeyStore(filepath.Join(t.TempDir(), "keys.db"), testEncKey)
	t.Cleanup(func() { _ = store.Close() })
	_ = store.Add(slot("slot-1", "deepseek", 1))
	r := NewRouter(RouterConfig{Store: store, Mode: ModeAuto, now: clock.now})

	r.RecordError("slot-1", nil, true)
	h, _ := store.GetHealth("slot-1")
	first := h.RateLimitedUntil.Sub(clock.t)
	r.RecordError("slot-1", nil, true)
	h, _ = store.GetHealth("slot-1")
	second := h.RateLimitedUntil.Sub(clock.t)
	if second <= first {
		t.Errorf("backoff should grow: first=%v second=%v", first, second)
	}
}

func TestRouter_RecordCostBudgetAlert(t *testing.T) {
	s := slot("slot-1", "deepseek", 1)
	s.DailyBudget = 1.0
	r, _, notif := routerFixture(t, ModeAuto, s)

	r.RecordCost("slot-1", 0.5) // under budget, no alert
	if notif.count() != 0 {
		t.Error("under-budget cost should not alert")
	}
	r.RecordCost("slot-1", 0.6) // crosses $1.00
	if notif.count() != 1 {
		t.Fatalf("crossing budget should alert once, got %d", notif.count())
	}
	if a := notif.last(); !contains(a, "budget") {
		t.Errorf("budget alert = %q", a)
	}
	// Already over budget: no duplicate alert.
	r.RecordCost("slot-1", 0.1)
	if notif.count() != 1 {
		t.Errorf("should not re-alert once over budget, got %d", notif.count())
	}
}

func TestRouter_RecordSuccessUpdatesLatencyAvg(t *testing.T) {
	r, store, _ := routerFixture(t, ModeAuto, slot("slot-1", "deepseek", 1))
	r.RecordSuccess("slot-1", 1000, 0.001)
	r.RecordSuccess("slot-1", 3000, 0.001)
	h, _ := store.GetHealth("slot-1")
	if h.AvgLatencyMs != 2000 {
		t.Errorf("avg latency = %d, want 2000", h.AvgLatencyMs)
	}
	if h.RequestsToday != 2 {
		t.Errorf("requests = %d, want 2", h.RequestsToday)
	}
	if h.SpentTodayUSD < 0.0019 {
		t.Errorf("spend = %v, want ~0.002", h.SpentTodayUSD)
	}
	// Success resets the error streak.
	r.RecordError("slot-1", errors.New("x"), false)
	r.RecordSuccess("slot-1", 1000, 0)
	h, _ = store.GetHealth("slot-1")
	if h.ErrorsLast10 != 0 {
		t.Errorf("success should reset error streak, got %d", h.ErrorsLast10)
	}
}

func TestRouter_HealthMonitorRecoversRateLimited(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1700000000, 0)}
	store, _ := NewKeyStore(filepath.Join(t.TempDir(), "keys.db"), testEncKey)
	t.Cleanup(func() { _ = store.Close() })
	_ = store.Add(slot("slot-1", "deepseek", 1))
	r := NewRouter(RouterConfig{Store: store, Mode: ModeAuto, now: clock.now})

	r.RecordError("slot-1", nil, true) // rate-limited until +60s
	if h, _ := store.GetHealth("slot-1"); !h.RateLimited {
		t.Fatal("slot should be rate-limited")
	}
	// Advance past the backoff window and sweep.
	clock.t = clock.t.Add(2 * time.Minute)
	r.sweep()
	if h, _ := store.GetHealth("slot-1"); h.RateLimited {
		t.Error("monitor should have cleared the elapsed rate limit")
	}
	if h, _ := store.GetHealth("slot-1"); h.Score < minUsableScore {
		t.Errorf("recovered slot score = %d, want usable", h.Score)
	}
}

func TestRouter_StatusMarksActive(t *testing.T) {
	r, _, _ := routerFixture(t, ModeQuality,
		slot("slot-1", "deepseek", 1), slot("slot-2", "groq", 2))
	_, _ = r.SelectSlot(context.Background(), "simple")
	st := r.Status()
	if len(st) != 2 {
		t.Fatalf("Status = %d slots, want 2", len(st))
	}
	active := ""
	for _, s := range st {
		if s.Active {
			active = s.Slot.ID
		}
	}
	if active != r.ActiveSlotID() || active == "" {
		t.Errorf("Status active=%q, ActiveSlotID=%q", active, r.ActiveSlotID())
	}
}

func TestRouter_ConcurrentSelect(t *testing.T) {
	r, _, _ := routerFixture(t, ModeBalanced,
		slot("slot-1", "deepseek", 1), slot("slot-2", "groq", 2),
		slot("slot-3", "ollama", 3), slot("slot-4", "claude", 4))
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.SelectSlot(context.Background(), "simple"); err != nil {
				t.Errorf("SelectSlot: %v", err)
			}
		}()
	}
	wg.Wait()
}

// fakeClock is a controllable time source.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// contains is a small substring helper (avoids importing strings everywhere).
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
