package tcp

import (
	"errors"
	"sync"
	"sync/atomic"
)

// BackendAddr is a backend target with a load-balancing weight. It is the
// shared backend descriptor used by the selector and the listener.
type BackendAddr struct {
	// Addr is the dial target, "host:port".
	Addr string
	// Weight biases selection in weighted round-robin; <=0 is treated as 1.
	Weight int
}

// ErrNoBackends is returned when a selector is constructed or queried with no
// backends configured.
var ErrNoBackends = errors.New("tcp: no backends configured")

// WeightedRR selects backends using smooth weighted round-robin — the algorithm
// Nginx uses. Compared to naive weighted RR (which bursts: A,A,A,A,A,B,C for
// weights 5,1,1), the smooth variant interleaves selections (A,A,B,A,A,C,A) so
// load is spread evenly over time rather than clumped.
//
// It is safe for concurrent use. A single-backend selector takes a lock-free
// fast path.
type WeightedRR struct {
	mu       sync.Mutex
	backends []wrrBackend

	// single holds the sole backend for the lock-free fast path; set only when
	// exactly one backend is configured. Read via the atomic pointer.
	single atomic.Pointer[BackendAddr]
}

// wrrBackend pairs a backend with its mutable smooth-WRR running weight.
type wrrBackend struct {
	addr          BackendAddr
	currentWeight int
}

// NewWeightedRR builds a selector over backends. It returns ErrNoBackends if the
// list is empty. Any backend with Weight <= 0 is normalized to Weight 1.
func NewWeightedRR(backends []BackendAddr) (*WeightedRR, error) {
	if len(backends) == 0 {
		return nil, ErrNoBackends
	}
	w := &WeightedRR{}
	w.setBackends(backends)
	return w, nil
}

// setBackends replaces the backend set. Caller must hold w.mu unless during
// construction (no other references yet). It normalizes weights and updates the
// single-backend fast-path pointer.
func (w *WeightedRR) setBackends(backends []BackendAddr) {
	w.backends = make([]wrrBackend, len(backends))
	for i, b := range backends {
		if b.Weight <= 0 {
			b.Weight = 1
		}
		w.backends[i] = wrrBackend{addr: b}
	}
	if len(w.backends) == 1 {
		b := w.backends[0].addr
		w.single.Store(&b)
	} else {
		w.single.Store(nil)
	}
}

// Next returns the next backend to use. It is thread-safe. With a single
// backend it returns immediately without taking the lock.
func (w *WeightedRR) Next() (BackendAddr, error) {
	if b := w.single.Load(); b != nil {
		return *b, nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.backends) == 0 {
		return BackendAddr{}, ErrNoBackends
	}
	// Re-check single under the lock in case Update shrank to one backend
	// between the atomic read and acquiring the lock.
	if len(w.backends) == 1 {
		return w.backends[0].addr, nil
	}

	total := 0
	best := -1
	for i := range w.backends {
		w.backends[i].currentWeight += w.backends[i].addr.Weight
		total += w.backends[i].addr.Weight
		if best == -1 || w.backends[i].currentWeight > w.backends[best].currentWeight {
			best = i
		}
	}
	w.backends[best].currentWeight -= total
	return w.backends[best].addr, nil
}

// Len returns the number of configured backends.
func (w *WeightedRR) Len() int {
	if w.single.Load() != nil {
		return 1
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.backends)
}

// Update atomically replaces the backend list, used for config hot-reload so a
// route's backends can change without dropping traffic. CurrentWeight state is
// preserved for backends that remain (matched by Addr); new backends start at
// CurrentWeight 0. Returns ErrNoBackends if the new list is empty.
func (w *WeightedRR) Update(backends []BackendAddr) error {
	if len(backends) == 0 {
		return ErrNoBackends
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Snapshot existing running weights by address.
	prev := make(map[string]int, len(w.backends))
	for _, b := range w.backends {
		prev[b.addr.Addr] = b.currentWeight
	}

	next := make([]wrrBackend, len(backends))
	for i, b := range backends {
		if b.Weight <= 0 {
			b.Weight = 1
		}
		next[i] = wrrBackend{addr: b, currentWeight: prev[b.Addr]} // 0 if new
	}
	w.backends = next

	if len(next) == 1 {
		bb := next[0].addr
		w.single.Store(&bb)
	} else {
		w.single.Store(nil)
	}
	return nil
}
