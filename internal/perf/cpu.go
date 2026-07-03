package perf

import (
	"runtime"
	"sync"
	"time"
)

// CPUSampler derives this process's CPU utilisation as a percentage of the
// machine's total capacity (busy CPU-seconds / (wall-clock × NumCPU)) between
// successive calls. It reads OS process CPU times (Getrusage on Unix,
// GetProcessTimes on Windows — see cpu_unix.go / cpu_windows.go), which are
// always fresh, giving the autoscaler a real load signal instead of a
// placeholder (production audit M4). runtime/metrics' /cpu/classes/* was
// rejected for this: those stats only refresh on a GC cycle, so a quiet
// interval reads as 0% CPU and would trigger spurious scale-downs.
// Safe for concurrent use.
type CPUSampler struct {
	mu       sync.Mutex
	lastBusy float64   // cumulative process CPU seconds at last call
	lastWall time.Time // wall clock at last call
	lastPct  float64   // last computed utilisation, reused for zero intervals
	primed   bool
}

// NewCPUSampler returns an unprimed sampler; the first Utilization call
// establishes the baseline and returns 0.
func NewCPUSampler() *CPUSampler { return &CPUSampler{} }

// Utilization returns the process CPU usage percentage (0–100, relative to
// all cores) over the interval since the previous call. The first call primes
// the baseline and returns 0; on platforms without process CPU times it
// always returns 0 (the autoscaler then never sees load and holds MinNodes).
func (c *CPUSampler) Utilization() float64 {
	busy, ok := processCPUSeconds()
	if !ok {
		return 0
	}
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	dBusy := busy - c.lastBusy
	dWall := now.Sub(c.lastWall).Seconds()
	primed := c.primed
	c.lastBusy, c.lastWall, c.primed = busy, now, true
	if !primed {
		return 0
	}
	if dWall <= 0 {
		// Back-to-back calls within clock resolution: no new interval to
		// measure, report the previous reading.
		return c.lastPct
	}
	pct := dBusy / (dWall * float64(runtime.NumCPU())) * 100
	switch {
	case pct < 0:
		pct = 0
	case pct > 100:
		pct = 100
	}
	c.lastPct = pct
	return pct
}
