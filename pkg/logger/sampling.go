package logger

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// SamplingConfig tunes the SamplingHandler.
type SamplingConfig struct {
	// Tick is the sampling window; counters reset at this interval. Default 1s.
	Tick time.Duration
	// First is how many records per (level,message) to log unconditionally at
	// the start of each window. Default 10.
	First int
	// Thereafter is the 1-in-N rate applied after First within a window.
	// Default 100.
	Thereafter int
}

// SamplingHandler wraps another slog.Handler and rate-limits high-volume Debug
// and Info records. Within each Tick window, the first First records for a
// given (level, message) pair pass through; after that only every Thereafter-th
// record does. Warn and Error records are never sampled — they always pass.
//
// This bounds log volume on hot paths (e.g. a route doing >10k req/s) without
// losing the signal of the first occurrences or any warning/error.
type SamplingHandler struct {
	inner      slog.Handler
	first      int
	thereafter int

	counts *sync.Map // message key -> *int64 (count within current window)
	stop   chan struct{}
	once   *sync.Once
}

// NewSamplingHandler wraps h with sampling per cfg, applying defaults for zero
// fields. It starts a background goroutine that clears counters every Tick.
func NewSamplingHandler(h slog.Handler, cfg SamplingConfig) *SamplingHandler {
	if cfg.Tick <= 0 {
		cfg.Tick = time.Second
	}
	if cfg.First <= 0 {
		cfg.First = 10
	}
	if cfg.Thereafter <= 0 {
		cfg.Thereafter = 100
	}
	s := &SamplingHandler{
		inner:      h,
		first:      cfg.First,
		thereafter: cfg.Thereafter,
		counts:     &sync.Map{},
		stop:       make(chan struct{}),
		once:       &sync.Once{},
	}
	go s.resetLoop(cfg.Tick)
	return s
}

// resetLoop clears the per-key counters every tick window.
func (s *SamplingHandler) resetLoop(tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.counts.Range(func(k, _ any) bool {
				s.counts.Delete(k)
				return true
			})
		}
	}
}

// Stop ends the background reset goroutine. Safe to call multiple times.
func (s *SamplingHandler) Stop() {
	s.once.Do(func() { close(s.stop) })
}

func (s *SamplingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return s.inner.Enabled(ctx, level)
}

// Handle applies sampling to Debug/Info and passes Warn/Error through.
func (s *SamplingHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		return s.inner.Handle(ctx, r)
	}
	if s.allow(r.Level, r.Message) {
		return s.inner.Handle(ctx, r)
	}
	return nil
}

// allow decides whether a (level, message) record passes the sampler.
func (s *SamplingHandler) allow(level slog.Level, msg string) bool {
	key := level.String() + "|" + msg
	v, _ := s.counts.LoadOrStore(key, new(int64))
	n := atomic.AddInt64(v.(*int64), 1)
	if n <= int64(s.first) {
		return true
	}
	// After the first N, pass every Thereafter-th record.
	return (n-int64(s.first))%int64(s.thereafter) == 0
}

func (s *SamplingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &SamplingHandler{
		inner:      s.inner.WithAttrs(attrs),
		first:      s.first,
		thereafter: s.thereafter,
		counts:     s.counts,
		stop:       s.stop,
		once:       s.once,
	}
}

func (s *SamplingHandler) WithGroup(name string) slog.Handler {
	return &SamplingHandler{
		inner:      s.inner.WithGroup(name),
		first:      s.first,
		thereafter: s.thereafter,
		counts:     s.counts,
		stop:       s.stop,
		once:       s.once,
	}
}
