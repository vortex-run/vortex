package tenancy

import (
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// bandwidthWindow is the rolling window over which per-namespace bandwidth is
// measured before the counter resets.
const bandwidthWindow = time.Minute

// QuotaStats is a snapshot of a namespace's live resource usage.
type QuotaStats struct {
	ActiveConns   int64
	BandwidthUsed int64 // bytes in the current window
	RouteCount    int
}

// nsCounters holds the live counters for one namespace.
type nsCounters struct {
	activeConns atomic.Int64
	bandwidth   atomic.Int64
	routeCount  atomic.Int64
}

// Enforcer enforces namespace quotas at the HTTP and TCP layers. It is safe for
// concurrent use.
type Enforcer struct {
	registry *Registry

	mu       sync.Mutex
	counters map[string]*nsCounters
}

// NewEnforcer builds an Enforcer backed by registry. It starts a background
// goroutine that resets bandwidth counters once per window.
func NewEnforcer(registry *Registry) *Enforcer {
	e := &Enforcer{registry: registry, counters: make(map[string]*nsCounters)}
	go e.resetLoop()
	return e
}

// countersFor returns (creating if needed) the counters for a namespace.
func (e *Enforcer) countersFor(nsID string) *nsCounters {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.counters[nsID]
	if !ok {
		c = &nsCounters{}
		e.counters[nsID] = c
	}
	return c
}

// HTTPMiddleware returns a middleware that enforces the connection quota for
// namespaceID: it increments the active-connection counter for the duration of
// each request and rejects with 429 when the limit is reached.
func (e *Enforcer) HTTPMiddleware(namespaceID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ns, err := e.registry.Get(namespaceID)
			if err != nil {
				// Unknown namespace: fail open (no quota to enforce).
				next.ServeHTTP(w, r)
				return
			}
			c := e.countersFor(namespaceID)

			current := c.activeConns.Load()
			if qerr := ns.CheckQuota("connections", current); qerr != nil {
				writeQuotaError(w, "connections", ns.Quotas().MaxConnections, namespaceID)
				return
			}

			c.activeConns.Add(1)
			defer c.activeConns.Add(-1)
			next.ServeHTTP(w, r)
		})
	}
}

// TCPMiddleware returns a connection wrapper enforcing MaxConnections for a
// namespace. It returns an error (closing the connection) when the limit is
// reached; otherwise it returns a wrapped conn that decrements the counter on
// Close.
func (e *Enforcer) TCPMiddleware(namespaceID string) func(net.Conn) (net.Conn, error) {
	return func(conn net.Conn) (net.Conn, error) {
		ns, err := e.registry.Get(namespaceID)
		if err != nil {
			return conn, nil // unknown namespace: no enforcement
		}
		c := e.countersFor(namespaceID)
		if qerr := ns.CheckQuota("connections", c.activeConns.Load()); qerr != nil {
			return nil, qerr
		}
		c.activeConns.Add(1)
		return &countedConn{Conn: conn, counter: &c.activeConns}, nil
	}
}

// RecordBandwidth adds bytes to a namespace's current-window bandwidth counter.
func (e *Enforcer) RecordBandwidth(namespaceID string, bytes int64) {
	if bytes <= 0 {
		return
	}
	e.countersFor(namespaceID).bandwidth.Add(bytes)
}

// SetRouteCount records the number of routes assigned to a namespace.
func (e *Enforcer) SetRouteCount(namespaceID string, n int) {
	e.countersFor(namespaceID).routeCount.Store(int64(n))
}

// Stats returns a snapshot of a namespace's usage.
func (e *Enforcer) Stats(namespaceID string) QuotaStats {
	c := e.countersFor(namespaceID)
	return QuotaStats{
		ActiveConns:   c.activeConns.Load(),
		BandwidthUsed: c.bandwidth.Load(),
		RouteCount:    int(c.routeCount.Load()),
	}
}

// resetLoop zeroes every namespace's bandwidth counter once per window.
func (e *Enforcer) resetLoop() {
	ticker := time.NewTicker(bandwidthWindow)
	defer ticker.Stop()
	for range ticker.C {
		e.mu.Lock()
		for _, c := range e.counters {
			c.bandwidth.Store(0)
		}
		e.mu.Unlock()
	}
}

// countedConn decrements the active-connection counter when closed.
type countedConn struct {
	net.Conn
	counter *atomic.Int64
	once    sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() { c.counter.Add(-1) })
	return c.Conn.Close()
}

// writeQuotaError writes a 429 JSON quota error.
func writeQuotaError(w http.ResponseWriter, resource string, limit int64, nsID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":     "quota exceeded",
		"resource":  resource,
		"limit":     limit,
		"namespace": nsID,
	})
}
