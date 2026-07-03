package perf

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestCPUSampler_FirstCallPrimesAndReturnsZero(t *testing.T) {
	s := NewCPUSampler()
	if got := s.Utilization(); got != 0 {
		t.Errorf("first Utilization() = %v, want 0 (priming call)", got)
	}
}

func TestCPUSampler_ReportsBusyCPUWithinBounds(t *testing.T) {
	s := NewCPUSampler()
	s.Utilization() // prime the baseline

	// Burn CPU on a few goroutines so the interval has real user time.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x := 0
			for {
				select {
				case <-stop:
					_ = x
					return
				default:
					x++
				}
			}
		}()
	}
	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()

	got := s.Utilization()
	if got <= 0 || got > 100 {
		t.Errorf("Utilization() after busy interval = %v, want in (0, 100]", got)
	}
}

func TestCPUSampler_ConcurrentCallsStayInRange(t *testing.T) {
	s := NewCPUSampler()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if got := s.Utilization(); got < 0 || got > 100 {
					t.Errorf("Utilization() = %v, want in [0, 100]", got)
					return
				}
			}
		}()
	}
	wg.Wait()
}
