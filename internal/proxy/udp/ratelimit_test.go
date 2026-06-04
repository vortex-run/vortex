package proxyudp

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimiter_ZeroRateError(t *testing.T) {
	if _, err := NewRateLimiter(0, 10); err == nil {
		t.Error("expected error for rate <= 0")
	}
}

func TestRateLimiter_ZeroBurstError(t *testing.T) {
	if _, err := NewRateLimiter(10, 0); err == nil {
		t.Error("expected error for burst <= 0")
	}
}

func TestRateLimiter_FirstPacketAllowed(t *testing.T) {
	rl, _ := NewRateLimiter(10, 5)
	if !rl.Allow("1.2.3.4") {
		t.Error("first packet should be allowed (bucket starts full)")
	}
}

func TestRateLimiter_BurstExhausted(t *testing.T) {
	// rate=1/s, burst=3 → after 3 rapid packets the 4th must be dropped.
	rl, _ := NewRateLimiter(1, 3)
	allowed := 0
	for i := 0; i < 4; i++ {
		if rl.Allow("5.5.5.5") {
			allowed++
		}
	}
	if allowed != 3 {
		t.Errorf("allowed %d of 4 rapid packets, want exactly 3 (burst)", allowed)
	}
}

func TestRateLimiter_RefillAfterWait(t *testing.T) {
	// rate=100/s → a token refills in ~10ms.
	rl, _ := NewRateLimiter(100, 1)
	if !rl.Allow("6.6.6.6") {
		t.Fatal("first packet should be allowed")
	}
	if rl.Allow("6.6.6.6") {
		t.Fatal("second immediate packet should be dropped (burst=1)")
	}
	time.Sleep(30 * time.Millisecond) // > 1/rate
	if !rl.Allow("6.6.6.6") {
		t.Error("packet after refill window should be allowed")
	}
}

func TestRateLimiter_IndependentIPs(t *testing.T) {
	rl, _ := NewRateLimiter(1, 1)
	if !rl.Allow("10.0.0.1") {
		t.Fatal("A first packet should be allowed")
	}
	if rl.Allow("10.0.0.1") {
		t.Fatal("A second packet should be dropped")
	}
	// A different IP has its own full bucket.
	if !rl.Allow("10.0.0.2") {
		t.Error("B should be allowed independently of A")
	}
}

func TestRateLimiter_ConcurrentSameIP(t *testing.T) {
	rl, err := NewRateLimiter(1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var allowed atomic.Int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rl.Allow("7.7.7.7") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait() // must not panic or race
	// With burst 1000 and 100 packets, all should be allowed.
	if got := allowed.Load(); got != 100 {
		t.Errorf("allowed %d of 100 concurrent packets, want 100", got)
	}
}
