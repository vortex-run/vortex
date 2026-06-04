package proxyudp

import (
	"context"
	"errors"
	"sync"
	"time"
)

// bucket is a token bucket for a single source IP.
type bucket struct {
	mu        sync.Mutex
	tokens    float64
	lastRefil time.Time
	lastSeen  time.Time // for stale-bucket cleanup
}

// RateLimiter applies a per-source-IP token bucket: each IP may send `rate`
// packets per second sustained, bursting up to `burst` packets. Packets over
// the limit are dropped (Allow returns false).
type RateLimiter struct {
	buckets sync.Map // string(ip) -> *bucket
	rate    float64
	burst   int
}

// NewRateLimiter builds a limiter allowing `rate` packets/sec per IP with a
// burst capacity of `burst`. Both must be positive.
func NewRateLimiter(rate, burst int) (*RateLimiter, error) {
	if rate <= 0 {
		return nil, errors.New("proxyudp: rate must be > 0")
	}
	if burst <= 0 {
		return nil, errors.New("proxyudp: burst must be > 0")
	}
	return &RateLimiter{rate: float64(rate), burst: burst}, nil
}

// Allow reports whether a packet from ip may be forwarded, consuming a token if
// so. It refills the bucket based on elapsed time, caps it at burst, and returns
// false (drop) when fewer than one token is available.
func (r *RateLimiter) Allow(ip string) bool {
	now := time.Now()
	v, _ := r.buckets.LoadOrStore(ip, &bucket{
		tokens:    float64(r.burst),
		lastRefil: now,
		lastSeen:  now,
	})
	b := v.(*bucket)

	b.mu.Lock()
	defer b.mu.Unlock()

	elapsed := now.Sub(b.lastRefil).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * r.rate
		if b.tokens > float64(r.burst) {
			b.tokens = float64(r.burst)
		}
		b.lastRefil = now
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// StartCleanup runs a goroutine that removes buckets unused for 10 minutes,
// sweeping every 5 minutes, until ctx is cancelled. This bounds memory under
// traffic from many distinct source IPs.
func (r *RateLimiter) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.sweep(10 * time.Minute)
			}
		}
	}()
}

// sweep removes buckets idle longer than maxIdle.
func (r *RateLimiter) sweep(maxIdle time.Duration) {
	now := time.Now()
	r.buckets.Range(func(key, value any) bool {
		b := value.(*bucket)
		b.mu.Lock()
		idle := now.Sub(b.lastSeen)
		b.mu.Unlock()
		if idle > maxIdle {
			r.buckets.Delete(key)
		}
		return true
	})
}
