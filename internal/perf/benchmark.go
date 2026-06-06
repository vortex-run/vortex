// Package perf implements VORTEX's performance harness (build plan M9): a
// benchmark suite for continuous throughput/latency tracking with regression
// detection, OS-level tuning recommendations, and horizontal autoscale triggers.
// Standard library only.
package perf

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// BenchmarkResult captures one benchmark run's headline metrics.
type BenchmarkResult struct {
	Name          string    `json:"name"`
	Timestamp     time.Time `json:"timestamp"`
	ReqPerSec     float64   `json:"req_per_sec"`
	P50Ms         float64   `json:"p50_ms"`
	P95Ms         float64   `json:"p95_ms"`
	P99Ms         float64   `json:"p99_ms"`
	ThroughputMBs float64   `json:"throughput_mbs"`
	AllocsPerOp   int64     `json:"allocs_per_op"`
	BytesPerOp    int64     `json:"bytes_per_op"`
	ErrorRate     float64   `json:"error_rate"`
}

// RegressionReport describes whether a current result regressed against a
// baseline.
type RegressionReport struct {
	Regressed bool    `json:"regressed"`
	Delta     float64 `json:"delta"`  // percentage change of the worst metric
	Metric    string  `json:"metric"` // which metric regressed
	Message   string  `json:"message"`
}

// regressionThreshold is the percentage degradation that counts as a regression.
const regressionThreshold = 5.0

// BenchmarkSuite runs and compares benchmarks for a named subject.
type BenchmarkSuite struct {
	name string
}

// NewBenchmarkSuite returns a suite tagged with name.
func NewBenchmarkSuite(name string) *BenchmarkSuite {
	return &BenchmarkSuite{name: name}
}

// Compare evaluates current against baseline. It reports a regression when a
// throughput-style metric drops, or a latency/error metric rises, by more than
// regressionThreshold percent. The worst offending metric is reported.
func (s *BenchmarkSuite) Compare(baseline, current BenchmarkResult) RegressionReport {
	worst := RegressionReport{Message: "no regression"}

	// Higher-is-better metrics: a drop is a regression.
	checkDrop := func(metric string, base, cur float64) {
		if base <= 0 {
			return
		}
		delta := (cur - base) / base * 100 // negative when worse
		if -delta > regressionThreshold && -delta > absDelta(worst) {
			worst = RegressionReport{
				Regressed: true, Delta: delta, Metric: metric,
				Message: fmt.Sprintf("%s dropped %.1f%% (%.2f → %.2f)", metric, -delta, base, cur),
			}
		}
	}
	// Lower-is-better metrics: a rise is a regression.
	checkRise := func(metric string, base, cur float64) {
		if base <= 0 {
			return
		}
		delta := (cur - base) / base * 100 // positive when worse
		if delta > regressionThreshold && delta > absDelta(worst) {
			worst = RegressionReport{
				Regressed: true, Delta: delta, Metric: metric,
				Message: fmt.Sprintf("%s rose %.1f%% (%.2f → %.2f)", metric, delta, base, cur),
			}
		}
	}

	checkDrop("req_per_sec", baseline.ReqPerSec, current.ReqPerSec)
	checkDrop("throughput_mbs", baseline.ThroughputMBs, current.ThroughputMBs)
	checkRise("p99_ms", baseline.P99Ms, current.P99Ms)
	checkRise("p95_ms", baseline.P95Ms, current.P95Ms)
	checkRise("error_rate", baseline.ErrorRate, current.ErrorRate)
	return worst
}

// SaveBaseline writes result to path as JSON.
func (s *BenchmarkSuite) SaveBaseline(result BenchmarkResult, path string) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("perf: encoding baseline: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("perf: writing baseline %s: %w", path, err)
	}
	return nil
}

// LoadBaseline reads a baseline result from path.
func (s *BenchmarkSuite) LoadBaseline(path string) (BenchmarkResult, error) {
	var r BenchmarkResult
	data, err := os.ReadFile(path)
	if err != nil {
		return r, fmt.Errorf("perf: reading baseline %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("perf: decoding baseline: %w", err)
	}
	return r, nil
}

// absDelta returns the magnitude of a report's delta (0 when none).
func absDelta(r RegressionReport) float64 {
	if r.Delta < 0 {
		return -r.Delta
	}
	return r.Delta
}
