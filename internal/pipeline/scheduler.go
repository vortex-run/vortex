package pipeline

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// JobFunc is the work a scheduled job performs. It receives the run's context.
type JobFunc func(ctx context.Context) error

// Job is a scheduled pipeline job.
type Job struct {
	Name     string
	Interval time.Duration // run every Interval (>= time.Minute recommended)
	Run      JobFunc

	mu       sync.Mutex
	lastRun  time.Time
	lastErr  error
	runCount int64
}

// JobStatus is a snapshot of a job's state.
type JobStatus struct {
	Name     string    `json:"name"`
	Interval string    `json:"interval"`
	LastRun  time.Time `json:"last_run"`
	RunCount int64     `json:"run_count"`
	LastErr  string    `json:"last_error,omitempty"`
}

// Scheduler runs registered jobs on their intervals. It is stdlib-only
// (time.Ticker per job) — no cron library.
type Scheduler struct {
	mu       sync.Mutex
	jobs     map[string]*Job
	running  bool
	clock    func() time.Time // injectable for tests
	onResult func(name string, err error)
}

// NewScheduler constructs a scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{jobs: map[string]*Job{}, clock: time.Now}
}

// SetResultHook installs a callback invoked after each job run (used for alerts).
func (s *Scheduler) SetResultHook(fn func(name string, err error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onResult = fn
}

// AddJob registers a job. Re-adding a name replaces it. Interval must be > 0.
func (s *Scheduler) AddJob(name string, interval time.Duration, run JobFunc) error {
	if name == "" {
		return fmt.Errorf("pipeline: job name required")
	}
	if interval <= 0 {
		return fmt.Errorf("pipeline: job interval must be > 0")
	}
	if run == nil {
		return fmt.Errorf("pipeline: job func required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[name] = &Job{Name: name, Interval: interval, Run: run}
	return nil
}

// RemoveJob unregisters a job.
func (s *Scheduler) RemoveJob(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, name)
}

// Jobs returns a status snapshot of all jobs.
func (s *Scheduler) Jobs() []JobStatus {
	s.mu.Lock()
	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()

	out := make([]JobStatus, 0, len(jobs))
	for _, j := range jobs {
		j.mu.Lock()
		st := JobStatus{
			Name: j.Name, Interval: j.Interval.String(),
			LastRun: j.lastRun, RunCount: j.runCount,
		}
		if j.lastErr != nil {
			st.LastErr = j.lastErr.Error()
		}
		j.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// RunNow executes a job immediately (outside its schedule).
func (s *Scheduler) RunNow(ctx context.Context, name string) error {
	s.mu.Lock()
	job, ok := s.jobs[name]
	hook := s.onResult
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("pipeline: no job %q", name)
	}
	return s.execute(ctx, job, hook)
}

// execute runs a job and records its result.
func (s *Scheduler) execute(ctx context.Context, job *Job, hook func(string, error)) error {
	err := job.Run(ctx)
	job.mu.Lock()
	job.lastRun = s.clock()
	job.lastErr = err
	job.runCount++
	job.mu.Unlock()
	if hook != nil {
		hook(job.Name, err)
	}
	return err
}

// Start launches a goroutine per job that runs it on its interval until ctx is
// cancelled. Start is idempotent (a second call is a no-op).
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	hook := s.onResult
	s.mu.Unlock()

	for _, job := range jobs {
		go s.runLoop(ctx, job, hook)
	}
}

// runLoop ticks a single job.
func (s *Scheduler) runLoop(ctx context.Context, job *Job, hook func(string, error)) {
	ticker := time.NewTicker(job.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.execute(ctx, job, hook)
		}
	}
}
