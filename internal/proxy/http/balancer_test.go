package proxyhttp

import (
	"testing"
	"time"
)

func TestBalancer_UnknownKind(t *testing.T) {
	if _, err := NewBalancer("magic", []BackendAddr{{Addr: "a"}}); err == nil {
		t.Error("unknown kind should return an error")
	}
}

func TestBalancer_EmptyBackends(t *testing.T) {
	if _, err := NewBalancer("round-robin", nil); err == nil {
		t.Error("round-robin with no backends should error")
	}
	if _, err := NewBalancer("least-conn", nil); err == nil {
		t.Error("least-conn with no backends should error")
	}
}

func TestRoundRobin_EvenDistribution(t *testing.T) {
	b, err := NewBalancer("round-robin", []BackendAddr{
		{Addr: "a", Weight: 1}, {Addr: "b", Weight: 1}, {Addr: "c", Weight: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for i := 0; i < 300; i++ {
		be, _ := b.Next(nil)
		counts[be.Addr]++
	}
	for _, addr := range []string{"a", "b", "c"} {
		if counts[addr] < 90 || counts[addr] > 110 {
			t.Errorf("%s = %d, want ~100 (±10)", addr, counts[addr])
		}
	}
}

func TestRoundRobin_WeightedDistribution(t *testing.T) {
	b, _ := NewBalancer("round-robin", []BackendAddr{
		{Addr: "a", Weight: 3}, {Addr: "b", Weight: 1},
	})
	counts := map[string]int{}
	for i := 0; i < 400; i++ {
		be, _ := b.Next(nil)
		counts[be.Addr]++
	}
	if counts["a"] < 280 || counts["a"] > 320 {
		t.Errorf("a = %d, want 300 (±20)", counts["a"])
	}
	if counts["b"] < 80 || counts["b"] > 120 {
		t.Errorf("b = %d, want 100 (±20)", counts["b"])
	}
}

func TestLeastConn_PicksFewest(t *testing.T) {
	lc, err := NewLeastConnBalancer([]BackendAddr{
		{Addr: "a"}, {Addr: "b"}, {Addr: "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Manually load up a and b; c has zero active and should be picked.
	lc.counter("a").Store(5)
	lc.counter("b").Store(2)
	lc.counter("c").Store(0)

	be, _ := lc.Next(nil)
	if be.Addr != "c" {
		t.Errorf("Next picked %s, want c (fewest active)", be.Addr)
	}
}

func TestLeastConn_TieBreakOrder(t *testing.T) {
	lc, _ := NewLeastConnBalancer([]BackendAddr{{Addr: "a"}, {Addr: "b"}})
	// All zero → tie → first configured (a) wins.
	be, _ := lc.Next(nil)
	if be.Addr != "a" {
		t.Errorf("tie should pick first backend a, got %s", be.Addr)
	}
}

func TestRoundRobin_RecordResultNoOp(t *testing.T) {
	b, err := NewBalancer("round-robin", []BackendAddr{{Addr: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	// RecordResult is a no-op for round-robin; it must not panic and Next must
	// keep working afterward.
	b.RecordResult("a", true, 5*time.Millisecond)
	b.RecordResult("a", false, 0)
	if _, err := b.Next(nil); err != nil {
		t.Errorf("Next after RecordResult: %v", err)
	}
}

func TestLeastConn_RecordResultDecrements(t *testing.T) {
	lc, _ := NewLeastConnBalancer([]BackendAddr{{Addr: "a"}})
	// Next increments to 1.
	_, _ = lc.Next(nil)
	if got := lc.counter("a").Load(); got != 1 {
		t.Fatalf("active after Next = %d, want 1", got)
	}
	lc.RecordResult("a", true, time.Millisecond)
	if got := lc.counter("a").Load(); got != 0 {
		t.Errorf("active after RecordResult = %d, want 0", got)
	}
	// Never goes negative.
	lc.RecordResult("a", true, time.Millisecond)
	if got := lc.counter("a").Load(); got != 0 {
		t.Errorf("active should not go negative, got %d", got)
	}
}
