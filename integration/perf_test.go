//go:build integration

package integration

import (
	"os/exec"
	"strings"
	"testing"
)

// TestPerf_TuneShowWorks runs `vortex tune show` against the real binary and
// checks it prints the recommendation header and a GOMAXPROCS line.
func TestPerf_TuneShowWorks(t *testing.T) {
	bin := getNetBinary(t)
	out, err := exec.Command(bin, "tune", "show").CombinedOutput()
	if err != nil {
		t.Fatalf("tune show: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "Recommended OS tuning") {
		t.Errorf("tune show missing header:\n%s", s)
	}
	if !strings.Contains(s, "GOMAXPROCS") {
		t.Errorf("tune show missing GOMAXPROCS:\n%s", s)
	}
}

// TestPerf_BenchRuns runs `vortex tune bench` and asserts it exits 0 and
// reports throughput in MB/s for at least one benchmark.
func TestPerf_BenchRuns(t *testing.T) {
	bin := getNetBinary(t)
	out, err := exec.Command(bin, "tune", "bench").CombinedOutput()
	if err != nil {
		t.Fatalf("tune bench: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "Benchmark results") {
		t.Errorf("tune bench missing header:\n%s", s)
	}
	if !strings.Contains(s, "MB/s") {
		t.Errorf("tune bench should report MB/s:\n%s", s)
	}
}
