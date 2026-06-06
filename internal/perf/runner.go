package perf

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// benchPayload is the per-iteration payload size used by the throughput
// benchmarks (64 KiB).
const benchPayload = 64 * 1024

// RunTCPTunnel benchmarks an in-memory TCP byte-copy tunnel using net.Pipe so
// there is no real network overhead, reporting MB/s and allocs/op.
func (s *BenchmarkSuite) RunTCPTunnel(b *testing.B) BenchmarkResult {
	payload := make([]byte, benchPayload)
	buf := make([]byte, benchPayload)

	b.SetBytes(int64(benchPayload))
	b.ReportAllocs()
	b.ResetTimer()

	start := time.Now()
	for i := 0; i < b.N; i++ {
		client, server := net.Pipe()
		go func() {
			_, _ = io.Copy(server, server) // echo
		}()
		go func() {
			_, _ = client.Write(payload)
		}()
		_, _ = io.ReadFull(client, buf)
		_ = client.Close()
		_ = server.Close()
	}
	elapsed := time.Since(start)
	b.StopTimer()

	return BenchmarkResult{
		Name:          s.name + "/tcp-tunnel",
		Timestamp:     time.Now(),
		ThroughputMBs: mbPerSec(int64(b.N)*benchPayload, elapsed),
	}
}

// RunHTTPProxy benchmarks an httptest backend behind a trivial proxy, reporting
// requests/sec.
func (s *BenchmarkSuite) RunHTTPProxy(b *testing.B) BenchmarkResult {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()
	client := backend.Client()

	b.ReportAllocs()
	b.ResetTimer()

	start := time.Now()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(backend.URL)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	elapsed := time.Since(start)
	b.StopTimer()

	return BenchmarkResult{
		Name:      s.name + "/http-proxy",
		Timestamp: time.Now(),
		ReqPerSec: reqPerSec(int64(b.N), elapsed),
	}
}

// RunUDPTunnel benchmarks UDP forwarding over loopback, reporting packets/sec as
// ReqPerSec and MB/s.
func (s *BenchmarkSuite) RunUDPTunnel(b *testing.B) BenchmarkResult {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Skipf("udp listen: %v", err)
	}
	defer func() { _ = server.Close() }()

	// Drain server reads in the background.
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, rerr := server.ReadFrom(buf); rerr != nil {
				return
			}
		}
	}()

	client, err := net.Dial("udp", server.LocalAddr().String())
	if err != nil {
		b.Skipf("udp dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	packet := make([]byte, 1024)
	b.SetBytes(int64(len(packet)))
	b.ReportAllocs()
	b.ResetTimer()

	start := time.Now()
	for i := 0; i < b.N; i++ {
		_, _ = client.Write(packet)
	}
	elapsed := time.Since(start)
	b.StopTimer()

	return BenchmarkResult{
		Name:          s.name + "/udp-tunnel",
		Timestamp:     time.Now(),
		ReqPerSec:     reqPerSec(int64(b.N), elapsed),
		ThroughputMBs: mbPerSec(int64(b.N)*int64(len(packet)), elapsed),
	}
}

// QuickBench runs a fixed-iteration version of each benchmark without a
// *testing.B, for the `vortex tune bench` command. It returns one result per
// subject (tcp, http, udp).
func (s *BenchmarkSuite) QuickBench() []BenchmarkResult {
	return []BenchmarkResult{
		s.quickTCP(2000),
		s.quickHTTP(2000),
		s.quickUDP(20000),
	}
}

// quickTCP measures the in-memory tunnel throughput over n iterations.
func (s *BenchmarkSuite) quickTCP(n int) BenchmarkResult {
	payload := make([]byte, benchPayload)
	buf := make([]byte, benchPayload)
	start := time.Now()
	for i := 0; i < n; i++ {
		client, server := net.Pipe()
		go func() { _, _ = io.Copy(server, server) }()
		go func() { _, _ = client.Write(payload) }()
		_, _ = io.ReadFull(client, buf)
		_ = client.Close()
		_ = server.Close()
	}
	return BenchmarkResult{
		Name: s.name + "/tcp-tunnel", Timestamp: time.Now(),
		ThroughputMBs: mbPerSec(int64(n)*benchPayload, time.Since(start)),
	}
}

// quickHTTP measures proxy request rate over n iterations.
func (s *BenchmarkSuite) quickHTTP(n int) BenchmarkResult {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()
	client := backend.Client()
	start := time.Now()
	for i := 0; i < n; i++ {
		resp, err := client.Get(backend.URL)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	return BenchmarkResult{
		Name: s.name + "/http-proxy", Timestamp: time.Now(),
		ReqPerSec: reqPerSec(int64(n), time.Since(start)),
	}
}

// quickUDP measures UDP packet rate over n iterations.
func (s *BenchmarkSuite) quickUDP(n int) BenchmarkResult {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return BenchmarkResult{Name: s.name + "/udp-tunnel", Timestamp: time.Now()}
	}
	defer func() { _ = server.Close() }()
	go func() {
		b := make([]byte, 2048)
		for {
			if _, _, rerr := server.ReadFrom(b); rerr != nil {
				return
			}
		}
	}()
	client, err := net.Dial("udp", server.LocalAddr().String())
	if err != nil {
		return BenchmarkResult{Name: s.name + "/udp-tunnel", Timestamp: time.Now()}
	}
	defer func() { _ = client.Close() }()

	packet := make([]byte, 1024)
	start := time.Now()
	for i := 0; i < n; i++ {
		_, _ = client.Write(packet)
	}
	return BenchmarkResult{
		Name: s.name + "/udp-tunnel", Timestamp: time.Now(),
		ReqPerSec:     reqPerSec(int64(n), time.Since(start)),
		ThroughputMBs: mbPerSec(int64(n)*int64(len(packet)), time.Since(start)),
	}
}

// mbPerSec computes megabytes per second.
func mbPerSec(bytes int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(bytes) / (1024 * 1024) / d.Seconds()
}

// reqPerSec computes requests per second.
func reqPerSec(n int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / d.Seconds()
}
