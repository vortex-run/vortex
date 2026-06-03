package logger

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// countingHandler is a minimal slog.Handler that counts handled records.
type countingHandler struct {
	n *int64
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(context.Context, slog.Record) error {
	atomic.AddInt64(h.n, 1)
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

func newSampler(t *testing.T, cfg SamplingConfig) (*SamplingHandler, *int64) {
	t.Helper()
	var n int64
	s := NewSamplingHandler(&countingHandler{n: &n}, cfg)
	t.Cleanup(s.Stop)
	return s, &n
}

func emit(s *SamplingHandler, level slog.Level, msg string, times int) {
	for i := 0; i < times; i++ {
		r := slog.NewRecord(time.Now(), level, msg, 0)
		_ = s.Handle(context.Background(), r)
	}
}

func TestSamplingFirstNAllLogged(t *testing.T) {
	s, n := newSampler(t, SamplingConfig{Tick: time.Hour, First: 10, Thereafter: 100})
	emit(s, slog.LevelInfo, "hot", 10)
	if got := atomic.LoadInt64(n); got != 10 {
		t.Errorf("first 10 should all log, got %d", got)
	}
}

func TestSamplingThereafterRate(t *testing.T) {
	s, n := newSampler(t, SamplingConfig{Tick: time.Hour, First: 10, Thereafter: 100})
	// Emit 10 (all logged) + 200 more (2 logged: at 100th and 200th past First).
	emit(s, slog.LevelInfo, "hot", 210)
	got := atomic.LoadInt64(n)
	if got != 12 {
		t.Errorf("expected 10 + 2 sampled = 12 logged, got %d", got)
	}
}

func TestSamplingWarnNeverSampled(t *testing.T) {
	s, n := newSampler(t, SamplingConfig{Tick: time.Hour, First: 1, Thereafter: 1000})
	emit(s, slog.LevelWarn, "warn-msg", 50)
	if got := atomic.LoadInt64(n); got != 50 {
		t.Errorf("warn should never be sampled, got %d of 50", got)
	}
}

func TestSamplingErrorNeverSampled(t *testing.T) {
	s, n := newSampler(t, SamplingConfig{Tick: time.Hour, First: 1, Thereafter: 1000})
	emit(s, slog.LevelError, "err-msg", 50)
	if got := atomic.LoadInt64(n); got != 50 {
		t.Errorf("error should never be sampled, got %d of 50", got)
	}
}

func TestSamplingCounterResetsAfterTick(t *testing.T) {
	s, n := newSampler(t, SamplingConfig{Tick: 50 * time.Millisecond, First: 10, Thereafter: 1000})
	emit(s, slog.LevelInfo, "hot", 10) // 10 logged
	// Wait for the window to reset.
	time.Sleep(120 * time.Millisecond)
	emit(s, slog.LevelInfo, "hot", 10) // first 10 again
	if got := atomic.LoadInt64(n); got != 20 {
		t.Errorf("after reset first 10 should log again (total 20), got %d", got)
	}
}

func TestSamplingWithAttrsReturnsSamplingHandler(t *testing.T) {
	s, _ := newSampler(t, SamplingConfig{})
	h := s.WithAttrs([]slog.Attr{slog.String("k", "v")})
	if _, ok := h.(*SamplingHandler); !ok {
		t.Errorf("WithAttrs should return *SamplingHandler, got %T", h)
	}
}

func TestSamplingWithGroupReturnsSamplingHandler(t *testing.T) {
	s, _ := newSampler(t, SamplingConfig{})
	h := s.WithGroup("grp")
	if _, ok := h.(*SamplingHandler); !ok {
		t.Errorf("WithGroup should return *SamplingHandler, got %T", h)
	}
}
