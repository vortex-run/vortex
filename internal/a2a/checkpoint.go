package a2a

import (
	"fmt"
	"sync"
	"time"
)

// Checkpoint statuses.
const (
	CheckpointPending  = "pending"
	CheckpointApproved = "approved"
	CheckpointEdited   = "edited"
	CheckpointRejected = "rejected"
)

// FilePreview is a file produced by an agent, shown to the user at a checkpoint.
type FilePreview struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Language string `json:"language"`
	Lines    int    `json:"lines"`
	IsNew    bool   `json:"is_new"`
}

// FileEdit is a user edit applied to a checkpoint file.
type FileEdit struct {
	Path       string    `json:"path"`
	NewContent string    `json:"new_content"`
	EditedBy   string    `json:"edited_by"`
	EditedAt   time.Time `json:"edited_at"`
}

// Checkpoint pauses the pipeline between agent steps for human review. The
// producing agent's Create call blocks until the user approves, rejects, or
// edits (or until ExpiresAt when auto-approve is configured).
type Checkpoint struct {
	ID          string        `json:"id"`
	SessionID   string        `json:"session_id"`
	FromAgent   string        `json:"from_agent"`
	ToAgent     string        `json:"to_agent"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Files       []FilePreview `json:"files"`
	TaskResult  *TaskResult   `json:"task_result,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	ExpiresAt   time.Time     `json:"expires_at"`
	Status      string        `json:"status"`
	EditedFiles []FileEdit    `json:"edited_files,omitempty"`

	// resolve is closed when the checkpoint is decided, unblocking Create.
	resolve chan struct{}
	// reason carries a rejection reason back to Create.
	reason string
}

// CheckpointOutcome is what Create returns once a checkpoint is decided.
type CheckpointOutcome struct {
	Status      string
	Reason      string     // set on rejection
	EditedFiles []FileEdit // set on edit
}

// CheckpointManager holds pending checkpoints and resolves them. It publishes
// checkpoint lifecycle events to the bus so the UI can render the review flow.
type CheckpointManager struct {
	mu               sync.RWMutex
	pending          map[string]*Checkpoint
	bus              *MessageBus
	autoApproveAfter time.Duration
}

// NewCheckpointManager constructs a manager. autoApproveAfter == 0 means a
// checkpoint never auto-approves (always waits for a human).
func NewCheckpointManager(bus *MessageBus, autoApproveAfter time.Duration) *CheckpointManager {
	return &CheckpointManager{
		pending:          map[string]*Checkpoint{},
		bus:              bus,
		autoApproveAfter: autoApproveAfter,
	}
}

// Create registers a checkpoint, publishes it to the bus, and BLOCKS until it
// is approved, rejected, or edited (or auto-approved on timeout). It returns
// the outcome; a rejection is signalled via the outcome's status + reason.
func (m *CheckpointManager) Create(sessionID, fromAgent, toAgent string, result TaskResult, files []FilePreview) (*CheckpointOutcome, error) {
	cp := &Checkpoint{
		ID:          "cp-" + randomID(),
		SessionID:   sessionID,
		FromAgent:   fromAgent,
		ToAgent:     toAgent,
		Title:       agentDisplayName(fromAgent) + " finished",
		Description: describeCheckpoint(fromAgent, toAgent, files),
		Files:       files,
		TaskResult:  &result,
		CreatedAt:   time.Now(),
		Status:      CheckpointPending,
		resolve:     make(chan struct{}),
	}
	if m.autoApproveAfter > 0 {
		cp.ExpiresAt = cp.CreatedAt.Add(m.autoApproveAfter)
	}

	m.mu.Lock()
	m.pending[cp.ID] = cp
	m.mu.Unlock()

	if m.bus != nil {
		m.bus.Publish(BusMessage{
			From: fromAgent, To: "user", Type: MsgCheckpoint, Content: cp.ID,
			SessionID: sessionID, Metadata: map[string]any{
				"title": cp.Title, "description": cp.Description, "files": len(files),
			},
		})
	}

	// Block until resolved or (optionally) auto-approved.
	if m.autoApproveAfter > 0 {
		timer := time.NewTimer(m.autoApproveAfter)
		defer timer.Stop()
		select {
		case <-cp.resolve:
		case <-timer.C:
			_ = m.Approve(cp.ID) // auto-approve on timeout
		}
	} else {
		<-cp.resolve
	}

	m.mu.RLock()
	outcome := &CheckpointOutcome{Status: cp.Status, Reason: cp.reason, EditedFiles: cp.EditedFiles}
	m.mu.RUnlock()

	m.mu.Lock()
	delete(m.pending, cp.ID)
	m.mu.Unlock()

	if outcome.Status == CheckpointRejected {
		return outcome, fmt.Errorf("checkpoint rejected: %s", outcome.Reason)
	}
	return outcome, nil
}

// resolveCheckpoint sets a terminal status and unblocks Create exactly once.
func (m *CheckpointManager) resolveCheckpoint(id, status, reason string, edits []FileEdit) error {
	m.mu.Lock()
	cp, ok := m.pending[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("checkpoint %s not found or already resolved", id)
	}
	if cp.Status != CheckpointPending {
		m.mu.Unlock()
		return fmt.Errorf("checkpoint %s already %s", id, cp.Status)
	}
	cp.Status = status
	cp.reason = reason
	cp.EditedFiles = edits
	resolve := cp.resolve
	m.mu.Unlock()

	close(resolve)
	if m.bus != nil {
		m.bus.Publish(BusMessage{
			From: "user", To: cp.FromAgent, Type: MsgCheckpoint,
			Content: id, SessionID: cp.SessionID,
			Metadata: map[string]any{"status": status},
		})
	}
	return nil
}

// Approve marks a checkpoint approved and unblocks its Create.
func (m *CheckpointManager) Approve(id string) error {
	return m.resolveCheckpoint(id, CheckpointApproved, "", nil)
}

// Reject marks a checkpoint rejected with a reason; Create returns an error.
func (m *CheckpointManager) Reject(id, reason string) error {
	return m.resolveCheckpoint(id, CheckpointRejected, reason, nil)
}

// Edit applies user edits and marks the checkpoint edited; Create unblocks with
// the edited files.
func (m *CheckpointManager) Edit(id string, edits []FileEdit) error {
	for i := range edits {
		if edits[i].EditedBy == "" {
			edits[i].EditedBy = "user"
		}
		if edits[i].EditedAt.IsZero() {
			edits[i].EditedAt = time.Now()
		}
	}
	return m.resolveCheckpoint(id, CheckpointEdited, "", edits)
}

// Get returns a pending checkpoint by id.
func (m *CheckpointManager) Get(id string) (*Checkpoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp, ok := m.pending[id]
	if !ok {
		return nil, fmt.Errorf("checkpoint %s not found", id)
	}
	return cp, nil
}

// Pending returns the pending checkpoints, filtered by sessionID when set.
func (m *CheckpointManager) Pending(sessionID string) []*Checkpoint {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Checkpoint
	for _, cp := range m.pending {
		if sessionID != "" && cp.SessionID != sessionID {
			continue
		}
		out = append(out, cp)
	}
	return out
}

// --- helpers ----------------------------------------------------------------

// agentDisplayName renders an agent id as a display name.
func agentDisplayName(id string) string {
	switch id {
	case "code-agent":
		return "Code Agent"
	case "test-agent":
		return "Test Agent"
	case "review-agent":
		return "Review Agent"
	case "coordinator":
		return "Coordinator"
	default:
		return id
	}
}

// describeCheckpoint summarises what was produced and who is next.
func describeCheckpoint(from, to string, files []FilePreview) string {
	s := agentDisplayName(from) + " finished."
	if to != "" && to != "user" {
		s += " " + agentDisplayName(to) + " is next."
	}
	if len(files) > 0 {
		s += fmt.Sprintf(" %d file(s) produced.", len(files))
	}
	return s
}

// FileEditsAsContext renders user edits as a context block for the next agent.
func FileEditsAsContext(edits []FileEdit) string {
	if len(edits) == 0 {
		return ""
	}
	s := "User edited these files:\n"
	for _, e := range edits {
		s += "--- " + e.Path + " (edited by " + e.EditedBy + ") ---\n" + e.NewContent + "\n"
	}
	return s
}
