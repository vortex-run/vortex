package pipeline

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduler_AddJobValidates(t *testing.T) {
	s := NewScheduler()
	if err := s.AddJob("", time.Minute, func(context.Context) error { return nil }); err == nil {
		t.Error("empty name should error")
	}
	if err := s.AddJob("j", 0, func(context.Context) error { return nil }); err == nil {
		t.Error("zero interval should error")
	}
	if err := s.AddJob("j", time.Minute, nil); err == nil {
		t.Error("nil func should error")
	}
	if err := s.AddJob("good", time.Minute, func(context.Context) error { return nil }); err != nil {
		t.Errorf("valid AddJob errored: %v", err)
	}
}

func TestScheduler_RunNowExecutes(t *testing.T) {
	s := NewScheduler()
	var ran atomic.Int32
	_ = s.AddJob("j", time.Hour, func(context.Context) error {
		ran.Add(1)
		return nil
	})
	if err := s.RunNow(context.Background(), "j"); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 1 {
		t.Errorf("RunNow should execute once, got %d", ran.Load())
	}
}

func TestScheduler_RunNowUnknownJob(t *testing.T) {
	if err := NewScheduler().RunNow(context.Background(), "nope"); err == nil {
		t.Error("RunNow on unknown job should error")
	}
}

func TestScheduler_JobsStatusTracksRuns(t *testing.T) {
	s := NewScheduler()
	_ = s.AddJob("j", time.Hour, func(context.Context) error { return nil })
	_ = s.RunNow(context.Background(), "j")
	_ = s.RunNow(context.Background(), "j")

	jobs := s.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("Jobs() = %d, want 1", len(jobs))
	}
	if jobs[0].RunCount != 2 {
		t.Errorf("run count = %d, want 2", jobs[0].RunCount)
	}
	if jobs[0].LastRun.IsZero() {
		t.Error("last run should be set")
	}
}

func TestScheduler_RecordsError(t *testing.T) {
	s := NewScheduler()
	_ = s.AddJob("bad", time.Hour, func(context.Context) error { return fmt.Errorf("boom") })
	_ = s.RunNow(context.Background(), "bad")
	if jobs := s.Jobs(); jobs[0].LastErr != "boom" {
		t.Errorf("last error = %q, want boom", jobs[0].LastErr)
	}
}

func TestScheduler_ResultHookFires(t *testing.T) {
	s := NewScheduler()
	var mu sync.Mutex
	var gotName string
	var gotErr error
	s.SetResultHook(func(name string, err error) {
		mu.Lock()
		gotName, gotErr = name, err
		mu.Unlock()
	})
	_ = s.AddJob("j", time.Hour, func(context.Context) error { return fmt.Errorf("x") })
	_ = s.RunNow(context.Background(), "j")
	mu.Lock()
	defer mu.Unlock()
	if gotName != "j" || gotErr == nil {
		t.Errorf("hook got %q / %v", gotName, gotErr)
	}
}

func TestScheduler_RemoveJob(t *testing.T) {
	s := NewScheduler()
	_ = s.AddJob("j", time.Hour, func(context.Context) error { return nil })
	s.RemoveJob("j")
	if len(s.Jobs()) != 0 {
		t.Error("RemoveJob should remove the job")
	}
}

func TestScheduler_StartRunsOnInterval(t *testing.T) {
	s := NewScheduler()
	var ran atomic.Int32
	_ = s.AddJob("fast", 20*time.Millisecond, func(context.Context) error {
		ran.Add(1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	// Allow a few ticks.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && ran.Load() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if ran.Load() < 2 {
		t.Errorf("job should have run multiple times, got %d", ran.Load())
	}
}

func TestScheduler_StartIdempotent(_ *testing.T) {
	s := NewScheduler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	s.Start(ctx) // second call must be a no-op, not panic
}
