package tcp

import (
	"errors"
	"sync"
	"testing"
)

func TestWRR_EmptyReturnsError(t *testing.T) {
	if _, err := NewWeightedRR(nil); !errors.Is(err, ErrNoBackends) {
		t.Errorf("NewWeightedRR(nil) err = %v, want ErrNoBackends", err)
	}
	if _, err := NewWeightedRR([]BackendAddr{}); !errors.Is(err, ErrNoBackends) {
		t.Errorf("NewWeightedRR([]) err = %v, want ErrNoBackends", err)
	}
}

func TestWRR_SingleBackend(t *testing.T) {
	w, err := NewWeightedRR([]BackendAddr{{Addr: "a:1", Weight: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		b, err := w.Next()
		if err != nil {
			t.Fatal(err)
		}
		if b.Addr != "a:1" {
			t.Errorf("Next() = %s, want a:1", b.Addr)
		}
	}
	if w.Len() != 1 {
		t.Errorf("Len() = %d, want 1", w.Len())
	}
}

func TestWRR_EqualWeightsAlternate(t *testing.T) {
	w, _ := NewWeightedRR([]BackendAddr{{Addr: "a", Weight: 1}, {Addr: "b", Weight: 1}})
	want := []string{"a", "b", "a", "b", "a", "b"}
	for i, exp := range want {
		b, _ := w.Next()
		if b.Addr != exp {
			t.Errorf("call %d = %s, want %s", i, b.Addr, exp)
		}
	}
}

func TestWRR_ExactCounts511(t *testing.T) {
	w, _ := NewWeightedRR([]BackendAddr{
		{Addr: "a", Weight: 5}, {Addr: "b", Weight: 1}, {Addr: "c", Weight: 1},
	})
	counts := map[string]int{}
	for i := 0; i < 7; i++ { // one full cycle = sum of weights
		b, _ := w.Next()
		counts[b.Addr]++
	}
	if counts["a"] != 5 || counts["b"] != 1 || counts["c"] != 1 {
		t.Errorf("counts = %v, want a=5 b=1 c=1", counts)
	}
}

func TestWRR_Distribution31(t *testing.T) {
	w, _ := NewWeightedRR([]BackendAddr{{Addr: "a", Weight: 3}, {Addr: "b", Weight: 1}})
	counts := map[string]int{}
	const n = 100
	for i := 0; i < n; i++ {
		b, _ := w.Next()
		counts[b.Addr]++
	}
	// Expect ~75/25, allow ±5%.
	if counts["a"] < 70 || counts["a"] > 80 {
		t.Errorf("a = %d/100, want ~75 (±5)", counts["a"])
	}
	if counts["b"] < 20 || counts["b"] > 30 {
		t.Errorf("b = %d/100, want ~25 (±5)", counts["b"])
	}
}

func TestWRR_SmoothNoBurst(t *testing.T) {
	w, _ := NewWeightedRR([]BackendAddr{
		{Addr: "a", Weight: 5}, {Addr: "b", Weight: 1}, {Addr: "c", Weight: 1},
	})
	// The canonical Nginx smooth-WRR sequence for weights {5,1,1} is
	// a a b a c a a — within a single cycle the heavy backend never appears more
	// than twice in a row (contrast naive WRR's a a a a a b c burst). Across
	// cycle boundaries a longer run is expected and correct, so we assert the
	// exact intra-cycle sequence, which is the strongest smoothness check.
	want := []string{"a", "a", "b", "a", "c", "a", "a"}
	for i, exp := range want {
		b, _ := w.Next()
		if b.Addr != exp {
			t.Fatalf("call %d = %s, want %s (smooth WRR sequence)", i, b.Addr, exp)
		}
	}

	// Verify max consecutive run within the cycle is 2 (no burst).
	maxRun, run := 1, 1
	for i := 1; i < len(want); i++ {
		if want[i] == want[i-1] {
			run++
			if run > maxRun {
				maxRun = run
			}
		} else {
			run = 1
		}
	}
	if maxRun > 2 {
		t.Errorf("intra-cycle max run = %d, want <= 2", maxRun)
	}
}

func TestWRR_UpdateReplacesBackends(t *testing.T) {
	w, _ := NewWeightedRR([]BackendAddr{{Addr: "a", Weight: 1}, {Addr: "b", Weight: 1}})
	if err := w.Update([]BackendAddr{{Addr: "x", Weight: 1}, {Addr: "y", Weight: 1}}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		b, _ := w.Next()
		seen[b.Addr] = true
	}
	if seen["a"] || seen["b"] {
		t.Errorf("old backends still selected after Update: %v", seen)
	}
	if !seen["x"] || !seen["y"] {
		t.Errorf("new backends not selected after Update: %v", seen)
	}
}

func TestWRR_UpdatePreservesWeights(t *testing.T) {
	w, _ := NewWeightedRR([]BackendAddr{{Addr: "a", Weight: 3}, {Addr: "b", Weight: 1}})
	// Advance a few times to build up CurrentWeight state.
	for i := 0; i < 4; i++ {
		_, _ = w.Next()
	}
	// Update keeping a, changing b's weight, adding c.
	if err := w.Update([]BackendAddr{
		{Addr: "a", Weight: 3}, {Addr: "b", Weight: 1}, {Addr: "c", Weight: 1},
	}); err != nil {
		t.Fatal(err)
	}
	// Distribution over a full new cycle should respect weights 3,1,1.
	counts := map[string]int{}
	for i := 0; i < 5; i++ {
		b, _ := w.Next()
		counts[b.Addr]++
	}
	if counts["a"] != 3 || counts["b"] != 1 || counts["c"] != 1 {
		t.Errorf("post-update counts = %v, want a=3 b=1 c=1", counts)
	}
}

func TestWRR_ZeroNegativeWeightNormalized(t *testing.T) {
	w, _ := NewWeightedRR([]BackendAddr{{Addr: "a", Weight: 0}, {Addr: "b", Weight: -5}})
	counts := map[string]int{}
	for i := 0; i < 10; i++ {
		b, _ := w.Next()
		counts[b.Addr]++
	}
	// Both normalized to weight 1 → 5/5 split.
	if counts["a"] != 5 || counts["b"] != 5 {
		t.Errorf("counts = %v, want a=5 b=5 (both normalized to 1)", counts)
	}
}

func TestWRR_ConcurrentNext(t *testing.T) {
	w, _ := NewWeightedRR([]BackendAddr{
		{Addr: "a", Weight: 2}, {Addr: "b", Weight: 1}, {Addr: "c", Weight: 1},
	})
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.Next(); err != nil {
				t.Errorf("Next: %v", err)
			}
		}()
	}
	wg.Wait()
}
