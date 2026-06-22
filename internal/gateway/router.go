package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// BudgetMode selects how the router chooses among healthy slots.
type BudgetMode string

// Budget modes.
const (
	ModeAuto     BudgetMode = "auto"     // VORTEX decides per request type
	ModeCheap    BudgetMode = "cheap"    // cheapest available
	ModeFast     BudgetMode = "fast"     // lowest latency
	ModeQuality  BudgetMode = "quality"  // highest score
	ModeBalanced BudgetMode = "balanced" // round-robin across healthy slots
)

// minUsableScore is the floor below which a slot is never selected.
const minUsableScore = 30

// rateLimitBackoff is the initial recovery window for a rate-limited slot.
const rateLimitBackoff = 60 * time.Second

// Notifier sends an out-of-band alert (Telegram et al). messaging.Router
// satisfies it via a thin adapter, keeping gateway free of a messaging import
// (which would cycle once messaging.AIGateway imports gateway).
type Notifier interface {
	Notify(title, body string)
}

// providerCost is a rough USD-per-1K-token ranking used by ModeCheap and the
// "simple" auto route. Lower is cheaper; Ollama (local) is free.
var providerCost = map[string]float64{
	"ollama":   0.0,
	"groq":     0.0001,
	"deepseek": 0.001,
	"gemini":   0.003,
	"openai":   0.005,
	"claude":   0.015,
}

// visionProviders can handle image inputs.
var visionProviders = map[string]bool{
	"claude": true, "gemini": true, "openai": true,
}

// RouterConfig configures a Router.
type RouterConfig struct {
	Store    *KeyStore
	Mode     BudgetMode
	Notifier Notifier
	now      func() time.Time // injectable clock (tests)
	logger   *slog.Logger
}

// Router selects key slots and records their outcomes, driving health-based
// failover across slots.
type Router struct {
	store    *KeyStore
	notifier Notifier
	now      func() time.Time
	log      *slog.Logger

	mu      sync.Mutex
	mode    BudgetMode
	rrIndex int            // round-robin cursor (ModeBalanced)
	active  string         // currently selected slot id (for Status)
	backoff map[string]int // consecutive rate limits per slot (exponential backoff)
	latency map[string][]int64
}

// NewRouter constructs a Router.
func NewRouter(cfg RouterConfig) *Router {
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	log := cfg.logger
	if log == nil {
		log = slog.Default()
	}
	mode := cfg.Mode
	if mode == "" {
		mode = ModeAuto
	}
	return &Router{
		store:    cfg.Store,
		notifier: cfg.Notifier,
		now:      now,
		log:      log,
		mode:     mode,
		backoff:  map[string]int{},
		latency:  map[string][]int64{},
	}
}

// Mode returns the current budget mode.
func (r *Router) Mode() BudgetMode {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mode
}

// SetMode changes the budget mode.
func (r *Router) SetMode(m BudgetMode) {
	r.mu.Lock()
	r.mode = m
	r.mu.Unlock()
}

// candidate pairs a slot with its current health/score for ranking.
type candidate struct {
	slot   KeySlot
	health KeyHealth
	score  int
}

// usableCandidates returns enabled slots with score >= minUsableScore, each
// with its live health.
func (r *Router) usableCandidates() ([]candidate, error) {
	slots, err := r.store.List()
	if err != nil {
		return nil, err
	}
	var out []candidate
	for _, sl := range slots {
		if !sl.Enabled {
			continue
		}
		h, _ := r.store.GetHealth(sl.ID)
		if h.Score < minUsableScore {
			continue
		}
		out = append(out, candidate{slot: sl, health: *h, score: h.Score})
	}
	return out, nil
}

// SelectSlot picks the best slot for a request type under the active mode. It
// always skips slots below the usable floor, and falls back to any working
// slot when the preferred class is unavailable.
func (r *Router) SelectSlot(_ context.Context, requestType string) (*KeySlot, error) {
	cands, err := r.usableCandidates()
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("gateway: no usable key slots (all disabled, rate-limited, or unhealthy)")
	}

	mode := r.Mode()
	var chosen *KeySlot
	switch mode {
	case ModeCheap:
		chosen = pickCheapest(cands)
	case ModeFast:
		chosen = pickFastest(cands)
	case ModeQuality:
		chosen = pickHighestScore(cands)
	case ModeBalanced:
		chosen = r.pickRoundRobin(cands)
	default: // ModeAuto
		chosen = r.pickAuto(cands, requestType)
	}
	if chosen == nil {
		chosen = pickHighestScore(cands) // fallback: any working slot
	}
	r.mu.Lock()
	r.active = chosen.ID
	r.mu.Unlock()
	return chosen, nil
}

// pickAuto routes by request type, with a minimum score gate per class and a
// graceful fallback to the highest-score slot.
func (r *Router) pickAuto(cands []candidate, requestType string) *KeySlot {
	switch requestType {
	case "simple":
		if c := cheapestAbove(cands, 50); c != nil {
			return c
		}
	case "coding":
		if c := qualityAbove(cands, 60); c != nil {
			return c
		}
	case "complex":
		if c := qualityAbove(cands, 70); c != nil {
			return c
		}
	case "vision":
		if c := firstVision(cands); c != nil {
			return c
		}
	}
	return pickHighestScore(cands)
}

func pickCheapest(cands []candidate) *KeySlot {
	best := &cands[0]
	for i := range cands {
		if providerCost[cands[i].slot.Provider] < providerCost[best.slot.Provider] {
			best = &cands[i]
		}
	}
	return &best.slot
}

// cheapestAbove returns the cheapest slot whose score exceeds threshold, or nil.
func cheapestAbove(cands []candidate, threshold int) *KeySlot {
	var best *candidate
	for i := range cands {
		if cands[i].score <= threshold {
			continue
		}
		if best == nil || providerCost[cands[i].slot.Provider] < providerCost[best.slot.Provider] {
			best = &cands[i]
		}
	}
	if best == nil {
		return nil
	}
	return &best.slot
}

func pickFastest(cands []candidate) *KeySlot {
	best := &cands[0]
	for i := range cands {
		if cands[i].health.AvgLatencyMs < best.health.AvgLatencyMs {
			best = &cands[i]
		}
	}
	return &best.slot
}

func pickHighestScore(cands []candidate) *KeySlot {
	best := &cands[0]
	for i := range cands {
		// Tie-break by priority (lower number = higher priority).
		if cands[i].score > best.score ||
			(cands[i].score == best.score && cands[i].slot.Priority < best.slot.Priority) {
			best = &cands[i]
		}
	}
	return &best.slot
}

// qualityAbove returns the highest-score slot whose score exceeds threshold, or nil.
func qualityAbove(cands []candidate, threshold int) *KeySlot {
	var filtered []candidate
	for _, c := range cands {
		if c.score > threshold {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return pickHighestScore(filtered)
}

// firstVision returns the highest-score vision-capable slot, or nil.
func firstVision(cands []candidate) *KeySlot {
	var filtered []candidate
	for _, c := range cands {
		if visionProviders[c.slot.Provider] {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return pickHighestScore(filtered)
}

// pickRoundRobin cycles deterministically across healthy slots (sorted by id).
func (r *Router) pickRoundRobin(cands []candidate) *KeySlot {
	sort.Slice(cands, func(i, j int) bool { return cands[i].slot.ID < cands[j].slot.ID })
	r.mu.Lock()
	idx := r.rrIndex % len(cands)
	r.rrIndex++
	r.mu.Unlock()
	return &cands[idx].slot
}

// RecordSuccess records a successful call: resets the error streak, updates the
// rolling latency average, adds the cost, and increments today's request count.
func (r *Router) RecordSuccess(slotID string, latencyMs int64, costUSD float64) {
	h, _ := r.store.GetHealth(slotID)
	r.mu.Lock()
	r.backoff[slotID] = 0
	lat := append(r.latency[slotID], latencyMs)
	if len(lat) > 10 {
		lat = lat[len(lat)-10:]
	}
	r.latency[slotID] = lat
	r.mu.Unlock()

	h.SlotID = slotID
	h.ErrorsLast10 = 0
	h.AvgLatencyMs = avg(lat)
	h.RequestsToday++
	h.SpentTodayUSD += costUSD
	h.RateLimited = false
	h.LastUsed = r.now()
	_ = r.store.UpdateHealth(*h)
}

// RecordError records a failed call. On a rate limit it marks the slot limited
// with exponential backoff and alerts; otherwise it bumps the error streak.
func (r *Router) RecordError(slotID string, err error, isRateLimit bool) {
	h, _ := r.store.GetHealth(slotID)
	h.SlotID = slotID
	h.ErrorsLast10++
	if h.ErrorsLast10 > 10 {
		h.ErrorsLast10 = 10
	}
	if err != nil {
		h.LastError = err.Error()
		h.LastErrorAt = r.now()
	}

	if isRateLimit {
		r.mu.Lock()
		r.backoff[slotID]++
		n := r.backoff[slotID]
		r.mu.Unlock()
		// Exponential backoff: 60s, 120s, 240s, ... capped at 1h.
		wait := rateLimitBackoff * time.Duration(1<<uint(min(n-1, 6)))
		if wait > time.Hour {
			wait = time.Hour
		}
		h.RateLimited = true
		h.RateLimitedUntil = r.now().Add(wait)
		slot, _ := r.store.Get(slotID)
		provider := slotID
		if slot != nil {
			provider = slot.Provider
		}
		r.alert("⚠️ VORTEX API Key Alert",
			fmt.Sprintf("Slot %s (%s) is rate limited\nSwitching to next available slot\nEstimated recovery: %.0f seconds",
				slotID, provider, wait.Seconds()))
	}
	_ = r.store.UpdateHealth(*h)
}

// RecordCost adds cost to a slot and, if it crosses the daily budget, marks the
// slot budget-exhausted and alerts.
func (r *Router) RecordCost(slotID string, usd float64) {
	h, _ := r.store.GetHealth(slotID)
	slot, err := r.store.Get(slotID)
	if err != nil {
		return
	}
	wasUnder := slot.DailyBudget <= 0 || h.SpentTodayUSD < slot.DailyBudget
	h.SlotID = slotID
	h.SpentTodayUSD += usd
	_ = r.store.UpdateHealth(*h) // recomputes score (budget penalty)

	if slot.DailyBudget > 0 && wasUnder && h.SpentTodayUSD >= slot.DailyBudget {
		r.alert("⚠️ VORTEX Budget Alert",
			fmt.Sprintf("Slot %s daily budget reached ($%.2f)\nSwitching to next slot\nReset at midnight",
				slotID, slot.DailyBudget))
	}
}

// alert sends a notification when a notifier is configured (best effort).
func (r *Router) alert(title, body string) {
	if r.notifier != nil {
		r.notifier.Notify(title, body)
	}
	r.log.Warn("key rotation alert", "title", title)
}

// SlotStatus is one slot's full status for the CLI/TUI/API.
type SlotStatus struct {
	Slot   KeySlot   `json:"slot"`
	Health KeyHealth `json:"health"`
	Score  int       `json:"score"`
	Active bool      `json:"active"`
}

// Status returns every slot's status, marking the currently active one.
func (r *Router) Status() []SlotStatus {
	slots, err := r.store.List()
	if err != nil {
		return nil
	}
	r.mu.Lock()
	active := r.active
	r.mu.Unlock()
	out := make([]SlotStatus, 0, len(slots))
	for _, sl := range slots {
		h, _ := r.store.GetHealth(sl.ID)
		out = append(out, SlotStatus{Slot: sl, Health: *h, Score: h.Score, Active: sl.ID == active})
	}
	return out
}

// ActiveSlotID returns the id of the currently selected slot ("" if none).
func (r *Router) ActiveSlotID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// StartHealthMonitor runs a background sweep every 30s: it clears rate limits
// whose backoff window has elapsed (so a recovered slot returns to service)
// and recomputes scores. It returns when ctx is cancelled.
func (r *Router) StartHealthMonitor(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep()
		}
	}
}

// sweep clears elapsed rate limits and refreshes scores once.
func (r *Router) sweep() {
	all, err := r.store.AllHealth()
	if err != nil {
		return
	}
	now := r.now()
	for _, h := range all {
		if h.RateLimited && !h.RateLimitedUntil.IsZero() && now.After(h.RateLimitedUntil) {
			h.RateLimited = false
			r.mu.Lock()
			r.backoff[h.SlotID] = 0
			r.mu.Unlock()
			r.log.Info("key slot recovered from rate limit", "slot", h.SlotID)
		}
		_ = r.store.UpdateHealth(h) // recomputes score
	}
}

// avg returns the integer mean of a latency sample (0 for empty).
func avg(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	var sum int64
	for _, x := range xs {
		sum += x
	}
	return sum / int64(len(xs))
}
