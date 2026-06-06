package perf

import (
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkTCPTunnel exercises the suite's TCP tunnel benchmark. Benchmarks
// report rather than assert; the non-zero-throughput guarantee is asserted in
// TestQuickBench_NonZero, which runs a fixed (measurable) iteration count.
func BenchmarkTCPTunnel(b *testing.B) {
	NewBenchmarkSuite("test").RunTCPTunnel(b)
}

// BenchmarkHTTPProxy exercises the suite's HTTP proxy benchmark.
func BenchmarkHTTPProxy(b *testing.B) {
	NewBenchmarkSuite("test").RunHTTPProxy(b)
}

func TestQuickBench_NonZero(t *testing.T) {
	s := NewBenchmarkSuite("test")
	results := s.QuickBench()
	if len(results) != 3 {
		t.Fatalf("QuickBench = %d results, want 3", len(results))
	}
	// TCP throughput and HTTP req/s must be positive on any machine.
	var tcp, http bool
	for _, r := range results {
		if r.Name == "test/tcp-tunnel" && r.ThroughputMBs > 0 {
			tcp = true
		}
		if r.Name == "test/http-proxy" && r.ReqPerSec > 0 {
			http = true
		}
	}
	if !tcp {
		t.Error("tcp benchmark should report non-zero MB/s")
	}
	if !http {
		t.Error("http benchmark should report non-zero req/s")
	}
}

func TestCompare_DetectsRegression(t *testing.T) {
	s := NewBenchmarkSuite("test")
	baseline := BenchmarkResult{ReqPerSec: 1000, ThroughputMBs: 100, P99Ms: 10}
	// 20% throughput drop → regression.
	current := BenchmarkResult{ReqPerSec: 1000, ThroughputMBs: 80, P99Ms: 10}
	rep := s.Compare(baseline, current)
	if !rep.Regressed {
		t.Fatalf("expected regression, got %+v", rep)
	}
	if rep.Metric != "throughput_mbs" {
		t.Errorf("regressed metric = %q, want throughput_mbs", rep.Metric)
	}
}

func TestCompare_DetectsLatencyRegression(t *testing.T) {
	s := NewBenchmarkSuite("test")
	baseline := BenchmarkResult{P99Ms: 10}
	current := BenchmarkResult{P99Ms: 15} // 50% worse latency
	rep := s.Compare(baseline, current)
	if !rep.Regressed || rep.Metric != "p99_ms" {
		t.Errorf("expected p99 regression, got %+v", rep)
	}
}

func TestCompare_WithinTolerance(t *testing.T) {
	s := NewBenchmarkSuite("test")
	baseline := BenchmarkResult{ReqPerSec: 1000, ThroughputMBs: 100, P99Ms: 10}
	// 3% changes are within the 5% tolerance.
	current := BenchmarkResult{ReqPerSec: 970, ThroughputMBs: 103, P99Ms: 10.2}
	rep := s.Compare(baseline, current)
	if rep.Regressed {
		t.Errorf("3%% changes should not regress: %+v", rep)
	}
}

func TestCompare_NoRegressionAgainstSelf(t *testing.T) {
	s := NewBenchmarkSuite("test")
	r := BenchmarkResult{ReqPerSec: 1000, ThroughputMBs: 100, P99Ms: 10, P95Ms: 8}
	if rep := s.Compare(r, r); rep.Regressed {
		t.Errorf("comparing a result to itself should never regress: %+v", rep)
	}
}

func TestBaseline_SaveLoadRoundTrip(t *testing.T) {
	s := NewBenchmarkSuite("test")
	r := BenchmarkResult{
		Name: "test/tcp", Timestamp: time.Now().UTC().Truncate(time.Second),
		ReqPerSec: 1234, ThroughputMBs: 567.8, P99Ms: 9.9,
	}
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := s.SaveBaseline(r, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ReqPerSec != r.ReqPerSec || loaded.ThroughputMBs != r.ThroughputMBs || loaded.Name != r.Name {
		t.Errorf("round-trip mismatch: %+v vs %+v", loaded, r)
	}
}
