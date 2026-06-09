// Package healing implements VORTEX's self-healing infrastructure (build plan
// M14): health monitoring of routes and subsystems, automatic recovery actions,
// a process watchdog, and SLO-breach alerting. Everything is stdlib-only.
//
// This file implements the health monitor: it runs a set of checks on their
// own intervals, tracks consecutive failures, and emits events when a check
// crosses its failure threshold or recovers.
package healing

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// Check kinds.
const (
	KindRoute    = "route"    // TCP dial the listen address
	KindEndpoint = "endpoint" // HTTP GET a URL
	KindProcess  = "process"  // pidfile exists + process running
)

// HealthCheck describes one thing to monitor.
type HealthCheck struct {
	Name      string
	Kind      string        // "route" | "process" | "endpoint"
	Target    string        // address, URL, or pidfile path
	Interval  time.Duration // default 30s
	Timeout   time.Duration // default 5s
	Threshold int           // consecutive failures before an alert (default 3)
}

// withDefaults returns a copy with zero fields set to their defaults.
func (c HealthCheck) withDefaults() HealthCheck {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if c.Threshold <= 0 {
		c.Threshold = 3
	}
	return c
}

// CheckResult is the latest outcome of a single check.
type CheckResult struct {
	Name      string        `json:"name"`
	Healthy   bool          `json:"healthy"`
	Latency   time.Duration `json:"-"`
	LatencyMs int64         `json:"latency_ms"`
	Error     string        `json:"error,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
	Attempts  int           `json:"consecutive_failures"`
}

// HealthEvent is emitted when a check crosses its threshold or recovers.
type HealthEvent struct {
	Name        string
	Kind        string
	Healthy     bool
	Recovered   bool
	Error       string
	Consecutive int
	Timestamp   time.Time
}

// Monitor runs health checks concurrently and reports status + events.
type Monitor struct {
	checks []HealthCheck
	probe  prober // pluggable for tests

	mu      sync.RWMutex
	results map[string]CheckResult
	fails   map[string]int  // consecutive failures per check
	alerted map[string]bool // whether we've emitted a failure event (de-dup)

	events chan HealthEvent
}

// prober performs the actual check; swapped out in tests.
type prober interface {
	probe(ctx context.Context, c HealthCheck) (time.Duration, error)
}

// NewMonitor constructs a monitor over the given checks (defaults applied).
func NewMonitor(checks []HealthCheck) *Monitor {
	withDefaults := make([]HealthCheck, len(checks))
	for i, c := range checks {
		withDefaults[i] = c.withDefaults()
	}
	return &Monitor{
		checks:  withDefaults,
		probe:   defaultProber{},
		results: make(map[string]CheckResult),
		fails:   make(map[string]int),
		alerted: make(map[string]bool),
		events:  make(chan HealthEvent, 64),
	}
}

// Events returns the channel of health events.
func (m *Monitor) Events() <-chan HealthEvent { return m.events }

// Start runs every check on its own interval until ctx is cancelled. It returns
// immediately; checks run in background goroutines.
func (m *Monitor) Start(ctx context.Context) {
	for _, c := range m.checks {
		go m.runCheck(ctx, c)
	}
}

// runCheck loops a single check on its interval, running once immediately.
func (m *Monitor) runCheck(ctx context.Context, c HealthCheck) {
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	m.runOnce(ctx, c)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runOnce(ctx, c)
		}
	}
}

// runOnce performs one probe and updates state, emitting events as needed.
func (m *Monitor) runOnce(ctx context.Context, c HealthCheck) {
	latency, err := m.probe.probe(ctx, c)
	now := time.Now()

	m.mu.Lock()
	res := CheckResult{Name: c.Name, Timestamp: now, Latency: latency, LatencyMs: latency.Milliseconds()}
	if err != nil {
		m.fails[c.Name]++
		res.Healthy = false
		res.Error = err.Error()
		res.Attempts = m.fails[c.Name]
		crossed := m.fails[c.Name] >= c.Threshold && !m.alerted[c.Name]
		if crossed {
			m.alerted[c.Name] = true
		}
		m.results[c.Name] = res
		consecutive := m.fails[c.Name]
		m.mu.Unlock()
		if crossed {
			m.emit(HealthEvent{
				Name: c.Name, Kind: c.Kind, Healthy: false, Error: err.Error(),
				Consecutive: consecutive, Timestamp: now,
			})
		}
		return
	}

	// Healthy probe.
	wasAlerted := m.alerted[c.Name]
	m.fails[c.Name] = 0
	m.alerted[c.Name] = false
	res.Healthy = true
	res.Attempts = 0
	m.results[c.Name] = res
	m.mu.Unlock()

	if wasAlerted {
		m.emit(HealthEvent{
			Name: c.Name, Kind: c.Kind, Healthy: true, Recovered: true, Timestamp: now,
		})
	}
}

// emit sends an event without blocking (drops if the buffer is full).
func (m *Monitor) emit(e HealthEvent) {
	select {
	case m.events <- e:
	default:
	}
}

// Status returns a snapshot of every check's latest result.
func (m *Monitor) Status() map[string]CheckResult {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]CheckResult, len(m.results))
	for k, v := range m.results {
		out[k] = v
	}
	return out
}

// Healthy reports whether every check is currently passing. A check with no
// result yet is treated as not-yet-healthy only if others exist; with no
// results at all it returns true (nothing has failed).
func (m *Monitor) Healthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.results {
		if !r.Healthy {
			return false
		}
	}
	return true
}

// defaultProber implements the real network/process probes.
type defaultProber struct{}

func (defaultProber) probe(ctx context.Context, c HealthCheck) (time.Duration, error) {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	switch c.Kind {
	case KindEndpoint:
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, c.Target, nil)
		if err != nil {
			return time.Since(start), err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return time.Since(start), err
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 500 {
			return time.Since(start), &probeError{"endpoint returned " + strconv.Itoa(resp.StatusCode)}
		}
		return time.Since(start), nil

	case KindProcess:
		return time.Since(start), probeProcess(c.Target)

	default: // KindRoute — TCP dial
		var d net.Dialer
		conn, err := d.DialContext(cctx, "tcp", c.Target)
		if err != nil {
			return time.Since(start), err
		}
		_ = conn.Close()
		return time.Since(start), nil
	}
}

// probeProcess checks the pidfile exists and the PID is alive.
func probeProcess(pidfile string) error {
	data, err := os.ReadFile(pidfile)
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(string(bytesTrim(data)))
	if err != nil {
		return &probeError{"invalid pidfile"}
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	// On Unix, Signal(0) checks liveness; on Windows FindProcess already errors
	// for a dead PID. Treat a successful FindProcess as alive on Windows.
	if err := signalZero(proc); err != nil {
		return &probeError{"process not running"}
	}
	return nil
}

// bytesTrim trims ASCII whitespace from a byte slice (avoids strings alloc).
func bytesTrim(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

// probeError is a small error type for probe failures.
type probeError struct{ msg string }

func (e *probeError) Error() string { return e.msg }
