package proxyhttp

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vortex-run/vortex/internal/proxy/tcp"
)

// BackendAddr is the HTTP-layer backend descriptor (host:port + weight).
type BackendAddr struct {
	Addr   string
	Weight int
}

// ErrNoBackends is returned when a balancer is built with no backends.
var ErrNoBackends = errors.New("proxyhttp: no backends configured")

// Balancer selects a backend for each request and records request outcomes so
// adaptive strategies can react.
type Balancer interface {
	// Next picks a backend for req. req may be used by request-affinity
	// strategies; round-robin/least-conn ignore it.
	Next(req *http.Request) (BackendAddr, error)
	// RecordResult reports the outcome of a request to a backend.
	RecordResult(addr string, success bool, latency time.Duration)
}

// NewBalancer constructs a Balancer of the given kind over backends.
// kind is "round-robin" or "least-conn".
func NewBalancer(kind string, backends []BackendAddr) (Balancer, error) {
	switch kind {
	case "round-robin", "":
		return NewRoundRobinBalancer(backends)
	case "least-conn":
		return NewLeastConnBalancer(backends)
	default:
		return nil, errors.New("proxyhttp: unknown balancer kind: " + kind)
	}
}

// toTCPBackends converts HTTP backends to the tcp package's BackendAddr.
func toTCPBackends(backends []BackendAddr) []tcp.BackendAddr {
	out := make([]tcp.BackendAddr, len(backends))
	for i, b := range backends {
		out[i] = tcp.BackendAddr{Addr: b.Addr, Weight: b.Weight}
	}
	return out
}

// RoundRobinBalancer wraps tcp.WeightedRR (smooth weighted round-robin).
type RoundRobinBalancer struct {
	rr *tcp.WeightedRR
}

// NewRoundRobinBalancer builds a round-robin balancer over backends.
func NewRoundRobinBalancer(backends []BackendAddr) (*RoundRobinBalancer, error) {
	if len(backends) == 0 {
		return nil, ErrNoBackends
	}
	rr, err := tcp.NewWeightedRR(toTCPBackends(backends))
	if err != nil {
		return nil, err
	}
	return &RoundRobinBalancer{rr: rr}, nil
}

// Next returns the next backend; req is ignored.
func (b *RoundRobinBalancer) Next(_ *http.Request) (BackendAddr, error) {
	be, err := b.rr.Next()
	if err != nil {
		return BackendAddr{}, err
	}
	return BackendAddr{Addr: be.Addr, Weight: be.Weight}, nil
}

// RecordResult is a no-op for round-robin.
func (b *RoundRobinBalancer) RecordResult(string, bool, time.Duration) {}

// LeastConnBalancer routes each request to the backend with the fewest
// in-flight requests, breaking ties by configured order (first wins). Active
// counts are tracked with atomic counters keyed by backend address.
type LeastConnBalancer struct {
	backends []BackendAddr
	active   sync.Map // addr -> *atomic.Int64
}

// NewLeastConnBalancer builds a least-connections balancer over backends.
func NewLeastConnBalancer(backends []BackendAddr) (*LeastConnBalancer, error) {
	if len(backends) == 0 {
		return nil, ErrNoBackends
	}
	lc := &LeastConnBalancer{backends: append([]BackendAddr(nil), backends...)}
	for _, b := range backends {
		lc.active.Store(b.Addr, new(atomic.Int64))
	}
	return lc, nil
}

func (b *LeastConnBalancer) counter(addr string) *atomic.Int64 {
	v, _ := b.active.LoadOrStore(addr, new(atomic.Int64))
	return v.(*atomic.Int64)
}

// Next picks the backend with the lowest active count and pre-increments it
// (the matching decrement happens in RecordResult). Ties favour the earlier
// backend in configured order.
func (b *LeastConnBalancer) Next(_ *http.Request) (BackendAddr, error) {
	if len(b.backends) == 0 {
		return BackendAddr{}, ErrNoBackends
	}
	best := -1
	var bestN int64
	for i, be := range b.backends {
		n := b.counter(be.Addr).Load()
		if best == -1 || n < bestN {
			best, bestN = i, n
		}
	}
	chosen := b.backends[best]
	b.counter(chosen.Addr).Add(1)
	return chosen, nil
}

// RecordResult decrements the active count for addr (the request finished).
func (b *LeastConnBalancer) RecordResult(addr string, _ bool, _ time.Duration) {
	c := b.counter(addr)
	if c.Load() > 0 {
		c.Add(-1)
	}
}
