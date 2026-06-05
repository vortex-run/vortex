package observability

import (
	"sync"
	"time"
)

// defaultSLOWindow is the rolling window used when none is configured.
const defaultSLOWindow = 30 * 24 * time.Hour

// SLOConfig configures a single service-level objective.
type SLOConfig struct {
	Name   string
	Route  string
	Target float64       // e.g. 0.999 for 99.9% success
	Window time.Duration // rolling window; default 30 days
}

// SLO tracks success/failure outcomes for a route and reports compliance, error
// budget, and burn rate against a target. It is safe for concurrent use.
type SLO struct {
	cfg     SLOConfig
	metrics *Metrics

	mu       sync.Mutex
	total    int64
	failures int64
	now      func() time.Time
	samples  []sample // for windowed burn-rate calculations
}

// sample records one outcome at a point in time for windowed burn-rate.
type sample struct {
	t       time.Time
	success bool
}

// NewSLO builds an SLO from cfg. metrics may be nil. Target is clamped to a sane
// range and Window defaults to 30 days.
func NewSLO(cfg SLOConfig, metrics *Metrics) *SLO {
	if cfg.Window <= 0 {
		cfg.Window = defaultSLOWindow
	}
	if cfg.Target <= 0 || cfg.Target >= 1 {
		cfg.Target = 0.999
	}
	return &SLO{cfg: cfg, metrics: metrics, now: time.Now}
}

// Record registers one request outcome.
func (s *SLO) Record(success bool) {
	s.mu.Lock()
	s.total++
	if !success {
		s.failures++
	}
	s.samples = append(s.samples, sample{t: s.now(), success: success})
	s.pruneLocked()
	s.mu.Unlock()
}

// Compliance returns the current success ratio in [0,1]. With no requests it
// reports perfect compliance (1.0).
func (s *SLO) Compliance() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total == 0 {
		return 1.0
	}
	return float64(s.total-s.failures) / float64(s.total)
}

// ErrorBudget returns the fraction of the error budget remaining, in [0,1]. The
// budget is (1-target) * total allowed failures; when it is exhausted (or there
// is no budget yet) it returns 0.
func (s *SLO) ErrorBudget() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total == 0 {
		return 1.0
	}
	budget := (1 - s.cfg.Target) * float64(s.total)
	if budget <= 0 {
		return 0
	}
	remaining := budget - float64(s.failures)
	if remaining <= 0 {
		return 0
	}
	return remaining / budget
}

// BurnRate returns the error rate over the given window relative to the
// tolerated rate (1-target): 1.0 means burning exactly at budget, >1 means
// burning faster than the budget allows. A zero/negative window uses all
// recorded samples.
func (s *SLO) BurnRate(window time.Duration) float64 {
	tolerated := 1 - s.cfg.Target
	if tolerated <= 0 {
		return 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var total, failures int64
	if window <= 0 {
		total, failures = s.total, s.failures
	} else {
		cutoff := s.now().Add(-window)
		for _, sm := range s.samples {
			if sm.t.Before(cutoff) {
				continue
			}
			total++
			if !sm.success {
				failures++
			}
		}
	}
	if total == 0 {
		return 0
	}
	actualErrorRate := float64(failures) / float64(total)
	return actualErrorRate / tolerated
}

// pruneLocked drops samples older than the SLO window. Caller holds the lock.
func (s *SLO) pruneLocked() {
	cutoff := s.now().Add(-s.cfg.Window)
	i := 0
	for i < len(s.samples) && s.samples[i].t.Before(cutoff) {
		i++
	}
	if i > 0 {
		s.samples = append(s.samples[:0], s.samples[i:]...)
	}
}
