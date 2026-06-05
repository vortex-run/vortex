package observability

import (
	"math"
	"testing"
	"time"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSLO_PerfectCompliance(t *testing.T) {
	s := NewSLO(SLOConfig{Target: 0.99}, nil)
	for i := 0; i < 100; i++ {
		s.Record(true)
	}
	if c := s.Compliance(); !approx(c, 1.0) {
		t.Errorf("Compliance = %v, want 1.0", c)
	}
}

func TestSLO_BelowTarget(t *testing.T) {
	s := NewSLO(SLOConfig{Target: 0.99}, nil)
	// 90 success, 10 fail → 90% compliance, below the 99% target.
	for i := 0; i < 90; i++ {
		s.Record(true)
	}
	for i := 0; i < 10; i++ {
		s.Record(false)
	}
	c := s.Compliance()
	if !approx(c, 0.90) {
		t.Errorf("Compliance = %v, want 0.90", c)
	}
	if c >= 0.99 {
		t.Error("compliance should be below the 0.99 target")
	}
}

func TestSLO_ErrorBudgetDecreases(t *testing.T) {
	s := NewSLO(SLOConfig{Target: 0.99}, nil) // 1% budget
	for i := 0; i < 1000; i++ {
		s.Record(true)
	}
	full := s.ErrorBudget()
	// Record a few failures; budget should drop.
	for i := 0; i < 5; i++ {
		s.Record(false)
	}
	after := s.ErrorBudget()
	if after >= full {
		t.Errorf("error budget should decrease after failures: full=%v after=%v", full, after)
	}
}

func TestSLO_ErrorBudgetExhausted(t *testing.T) {
	s := NewSLO(SLOConfig{Target: 0.99}, nil) // budget = 1% of total
	// 100 requests → budget = 1 failure. Record 5 failures → exhausted.
	for i := 0; i < 95; i++ {
		s.Record(true)
	}
	for i := 0; i < 5; i++ {
		s.Record(false)
	}
	if b := s.ErrorBudget(); b != 0 {
		t.Errorf("ErrorBudget = %v, want 0 (exhausted)", b)
	}
}

func TestSLO_BurnRateAtBudget(t *testing.T) {
	s := NewSLO(SLOConfig{Target: 0.99}, nil) // tolerated error rate = 1%
	// Error rate exactly 1% → burn rate 1.0.
	for i := 0; i < 99; i++ {
		s.Record(true)
	}
	s.Record(false)
	br := s.BurnRate(0) // all samples
	if !approx(br, 1.0) {
		t.Errorf("BurnRate = %v, want 1.0 when error rate == tolerated", br)
	}
}

func TestSLO_BurnRateAboveBudget(t *testing.T) {
	s := NewSLO(SLOConfig{Target: 0.99}, nil) // tolerated = 1%
	// 5% error rate → burn rate 5.0.
	for i := 0; i < 95; i++ {
		s.Record(true)
	}
	for i := 0; i < 5; i++ {
		s.Record(false)
	}
	br := s.BurnRate(0)
	if br <= 1.0 {
		t.Errorf("BurnRate = %v, want > 1.0 when error rate exceeds tolerance", br)
	}
	if !approx(br, 5.0) {
		t.Errorf("BurnRate = %v, want ~5.0 (5%% errors / 1%% tolerated)", br)
	}
}

func TestSLO_BurnRateWindowed(t *testing.T) {
	s := NewSLO(SLOConfig{Target: 0.99, Window: time.Hour}, nil)
	cur := time.Now()
	s.now = func() time.Time { return cur }

	// Old failures outside a 1-minute window should not count toward burn rate.
	for i := 0; i < 10; i++ {
		s.Record(false)
	}
	cur = cur.Add(10 * time.Minute)
	for i := 0; i < 100; i++ {
		s.Record(true)
	}
	// Over the last minute: all successes → burn rate 0.
	if br := s.BurnRate(time.Minute); !approx(br, 0) {
		t.Errorf("windowed BurnRate = %v, want 0 (recent window all success)", br)
	}
}
