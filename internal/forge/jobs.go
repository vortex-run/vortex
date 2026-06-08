package forge

import (
	"context"
	"crypto/rand"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// JobState is the lifecycle state of a forge build job.
type JobState string

// Job states.
const (
	JobQueued   JobState = "queued"
	JobRunning  JobState = "running"
	JobComplete JobState = "complete"
	JobFailed   JobState = "failed"
	JobClarify  JobState = "needs_clarification"
)

// Job is one asynchronous build request.
type Job struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	SessionID string    `json:"session_id"`
	ChatID    int64     `json:"chat_id"`
	State     JobState  `json:"state"`
	Progress  string    `json:"progress"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// JobManager runs forge builds asynchronously and tracks their status.
type JobManager struct {
	forge *Forge

	mu   sync.Mutex
	jobs map[string]*Job
}

// NewJobManager constructs a manager over a Forge.
func NewJobManager(f *Forge) *JobManager {
	return &JobManager{forge: f, jobs: make(map[string]*Job)}
}

// Submit enqueues a build and starts it asynchronously, returning the job ID.
func (m *JobManager) Submit(ctx context.Context, message, sessionID string, chatID int64) string {
	id := newJobID()
	job := &Job{
		ID: id, Message: message, SessionID: sessionID, ChatID: chatID,
		State: JobQueued, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	m.mu.Lock()
	m.jobs[id] = job
	m.mu.Unlock()

	go m.run(ctx, job)
	return id
}

// run executes the build, updating job state as it progresses.
func (m *JobManager) run(ctx context.Context, job *Job) {
	m.update(job.ID, func(j *Job) { j.State = JobRunning })

	progress := func(msg string) {
		m.update(job.ID, func(j *Job) {
			j.Progress = msg
			if strings.HasPrefix(msg, "❓") { // a clarifying-question line
				j.State = JobClarify
			}
		})
	}

	err := m.forge.Build(ctx, job.Message, job.ChatID, progress)
	m.update(job.ID, func(j *Job) {
		if err != nil {
			j.State = JobFailed
			j.Error = err.Error()
			return
		}
		// A clarification short-circuit leaves no delivery; preserve that state.
		if j.State != JobClarify {
			j.State = JobComplete
		}
	})
}

// update applies fn to the job under lock, refreshing UpdatedAt.
func (m *JobManager) update(id string, fn func(*Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.jobs[id]; ok {
		fn(j)
		j.UpdatedAt = time.Now()
	}
}

// Get returns a copy of the job by ID.
func (m *JobManager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// List returns all jobs, newest first.
func (m *JobManager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// newJobID returns a random job identifier.
func newJobID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("job-%x", b)
}
