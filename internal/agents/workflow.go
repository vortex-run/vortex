package agents

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Workflow statuses. pending/running/paused are incomplete (resumable after a
// crash); done/failed are terminal.
const (
	WorkflowPending = "pending"
	WorkflowRunning = "running"
	WorkflowPaused  = "paused"
	WorkflowDone    = "done"
	WorkflowFailed  = "failed"
)

// Step statuses. A failed step with attempts remaining returns to pending so
// Resume retries it; it becomes failed only when MaxRetries is exhausted.
const (
	StepPending = "pending"
	StepDone    = "done"
	StepFailed  = "failed"
)

// defaultStepRetries is MaxRetries for steps created without one.
const defaultStepRetries = 3

// WorkflowState is a durable multi-step task (build plan upgrade 4 — workflow
// recovery). Every step result is written to SQLite immediately, so a crash
// mid-task loses at most the step in flight; on restart ListIncomplete finds
// the workflow and it resumes from the first incomplete step.
type WorkflowState struct {
	ID          string         `json:"id"`
	Goal        string         `json:"goal"`
	Steps       []WorkflowStep `json:"steps"`
	CurrentStep int            `json:"current_step"`
	Status      string         `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	SessionID   string         `json:"session_id"`
	Result      string         `json:"result"`
}

// WorkflowStep is one durable step of a workflow.
type WorkflowStep struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	ToolName    string         `json:"tool_name,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
	Status      string         `json:"status"`
	Result      string         `json:"result,omitempty"`
	Error       string         `json:"error,omitempty"`
	Attempts    int            `json:"attempts"`
	MaxRetries  int            `json:"max_retries"`
}

// WorkflowStore persists workflow state in SQLite. Safe for concurrent use
// (single connection, WAL), mirroring the other agent stores.
type WorkflowStore struct {
	db   *sql.DB
	path string
}

// workflowSchema is applied on open; IF NOT EXISTS makes it idempotent.
const workflowSchema = `
CREATE TABLE IF NOT EXISTS workflows (
  id           TEXT PRIMARY KEY,
  goal         TEXT NOT NULL,
  status       TEXT NOT NULL,
  current_step INTEGER NOT NULL DEFAULT 0,
  session_id   TEXT NOT NULL DEFAULT '',
  result       TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS workflow_steps (
  id          TEXT PRIMARY KEY,
  workflow_id TEXT NOT NULL,
  idx         INTEGER NOT NULL,
  description TEXT NOT NULL,
  tool_name   TEXT NOT NULL DEFAULT '',
  params_json TEXT NOT NULL DEFAULT '{}',
  status      TEXT NOT NULL,
  result      TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT '',
  attempts    INTEGER NOT NULL DEFAULT 0,
  max_retries INTEGER NOT NULL DEFAULT 3,
  FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_steps_workflow ON workflow_steps(workflow_id, idx);
CREATE INDEX IF NOT EXISTS idx_workflows_status ON workflows(status);
`

// NewWorkflowStore opens (creating if needed) a SQLite-backed workflow store
// at dbPath. Writes are transactional, so step updates are atomic and durable.
func NewWorkflowStore(dbPath string) (*WorkflowStore, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("agents: creating workflow dir: %w", err)
		}
	}
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("agents: opening workflow db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(workflowSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agents: applying workflow schema: %w", err)
	}
	return &WorkflowStore{db: db, path: dbPath}, nil
}

// Close closes the underlying database.
func (s *WorkflowStore) Close() error { return s.db.Close() }

// Create persists a new running workflow with its planned steps (steps may be
// empty for goals whose steps are discovered during execution — see
// AppendCompletedStep).
func (s *WorkflowStore) Create(goal, sessionID string, steps []WorkflowStep) (*WorkflowState, error) {
	if strings.TrimSpace(goal) == "" {
		return nil, fmt.Errorf("agents: workflow goal required")
	}
	id, err := randomSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	wf := &WorkflowState{
		ID: id, Goal: goal, Status: WorkflowRunning, SessionID: sessionID,
		CreatedAt: now, UpdatedAt: now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT INTO workflows(id, goal, status, current_step, session_id, result, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		wf.ID, wf.Goal, wf.Status, 0, wf.SessionID, "", now.UnixMilli(), now.UnixMilli()); err != nil {
		return nil, fmt.Errorf("agents: creating workflow: %w", err)
	}
	for i, st := range steps {
		if st.ID == "" {
			sid, err := randomSessionID()
			if err != nil {
				return nil, err
			}
			st.ID = sid
		}
		if st.Status == "" {
			st.Status = StepPending
		}
		if st.MaxRetries <= 0 {
			st.MaxRetries = defaultStepRetries
		}
		paramsJSON, _ := json.Marshal(st.Params)
		if st.Params == nil {
			paramsJSON = []byte("{}")
		}
		if _, err := tx.Exec(
			`INSERT INTO workflow_steps(id, workflow_id, idx, description, tool_name, params_json,
			   status, result, error, attempts, max_retries)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			st.ID, wf.ID, i, st.Description, st.ToolName, string(paramsJSON),
			st.Status, st.Result, st.Error, st.Attempts, st.MaxRetries); err != nil {
			return nil, fmt.Errorf("agents: creating workflow step: %w", err)
		}
		wf.Steps = append(wf.Steps, st)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return wf, nil
}

// UpdateStep records one step execution attempt, durably. On success the step
// is marked done and the workflow's current step advances. On failure the
// attempt counter increments: with retries remaining the step returns to
// pending (Resume will retry it, logged); once MaxRetries is exhausted the
// step and the whole workflow are marked failed.
func (s *WorkflowStore) UpdateStep(workflowID, stepID, result, errMsg string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var idx, attempts, maxRetries int
	if err := tx.QueryRow(
		`SELECT idx, attempts, max_retries FROM workflow_steps WHERE id=? AND workflow_id=?`,
		stepID, workflowID).Scan(&idx, &attempts, &maxRetries); err != nil {
		return fmt.Errorf("agents: step %s not found in workflow %s: %w", stepID, workflowID, err)
	}
	now := time.Now().UnixMilli()

	if errMsg == "" {
		if _, err := tx.Exec(
			`UPDATE workflow_steps SET status=?, result=?, error='', attempts=attempts+1 WHERE id=?`,
			StepDone, result, stepID); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`UPDATE workflows SET current_step=?, updated_at=? WHERE id=?`,
			idx+1, now, workflowID); err != nil {
			return err
		}
		return tx.Commit()
	}

	attempts++
	status := StepPending // retryable
	if attempts >= maxRetries {
		status = StepFailed
	}
	if _, err := tx.Exec(
		`UPDATE workflow_steps SET status=?, error=?, attempts=? WHERE id=?`,
		status, errMsg, attempts, stepID); err != nil {
		return err
	}
	if status == StepFailed {
		if _, err := tx.Exec(
			`UPDATE workflows SET status=?, result=?, updated_at=? WHERE id=?`,
			WorkflowFailed, "step failed: "+errMsg, now, workflowID); err != nil {
			return err
		}
	} else {
		slog.Info("retrying workflow step",
			"workflow", workflowID, "step", idx+1, "attempt", attempts+1, "max", maxRetries)
		if _, err := tx.Exec(
			`UPDATE workflows SET updated_at=? WHERE id=?`, now, workflowID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AppendCompletedStep durably records a step that has already executed — used
// by goals whose steps are discovered during execution (e.g. orchestration
// progress lines) rather than planned upfront.
func (s *WorkflowStore) AppendCompletedStep(workflowID, description string) error {
	id, err := randomSessionID()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM workflow_steps WHERE workflow_id=?`, workflowID).Scan(&n); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO workflow_steps(id, workflow_id, idx, description, status, attempts, max_retries)
		 VALUES(?,?,?,?,?,1,?)`,
		id, workflowID, n, description, StepDone, defaultStepRetries); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE workflows SET current_step=?, updated_at=? WHERE id=?`,
		n+1, time.Now().UnixMilli(), workflowID); err != nil {
		return err
	}
	return tx.Commit()
}

// Resume loads a workflow positioned at its first incomplete step and marks it
// running again. The returned state's CurrentStep indexes the step to execute
// next.
func (s *WorkflowStore) Resume(workflowID string) (*WorkflowState, error) {
	wf, err := s.load(workflowID)
	if err != nil {
		return nil, err
	}
	next := len(wf.Steps)
	for i, st := range wf.Steps {
		if st.Status != StepDone {
			next = i
			break
		}
	}
	wf.CurrentStep = next
	wf.Status = WorkflowRunning
	now := time.Now()
	wf.UpdatedAt = now
	if _, err := s.db.Exec(
		`UPDATE workflows SET status=?, current_step=?, updated_at=? WHERE id=?`,
		WorkflowRunning, next, now.UnixMilli(), workflowID); err != nil {
		return nil, err
	}
	return wf, nil
}

// ListIncomplete returns all workflows that have not reached a terminal state
// (pending/running/paused), oldest first — called on startup to resume
// interrupted work.
func (s *WorkflowStore) ListIncomplete() ([]*WorkflowState, error) {
	rows, err := s.db.Query(
		`SELECT id FROM workflows WHERE status NOT IN (?, ?) ORDER BY created_at ASC`,
		WorkflowDone, WorkflowFailed)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []*WorkflowState
	for _, id := range ids {
		wf, err := s.load(id)
		if err != nil {
			return nil, err
		}
		out = append(out, wf)
	}
	return out, nil
}

// Complete marks a workflow done with its final result.
func (s *WorkflowStore) Complete(workflowID, result string) error {
	return s.finish(workflowID, WorkflowDone, result)
}

// Fail marks a workflow failed with the error message.
func (s *WorkflowStore) Fail(workflowID, errMsg string) error {
	return s.finish(workflowID, WorkflowFailed, errMsg)
}

// finish sets a terminal status and result.
func (s *WorkflowStore) finish(workflowID, status, result string) error {
	res, err := s.db.Exec(
		`UPDATE workflows SET status=?, result=?, updated_at=? WHERE id=?`,
		status, result, time.Now().UnixMilli(), workflowID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("agents: workflow %s not found", workflowID)
	}
	return nil
}

// load reads one workflow with its steps in index order.
func (s *WorkflowStore) load(workflowID string) (*WorkflowState, error) {
	wf := &WorkflowState{}
	var created, updated int64
	if err := s.db.QueryRow(
		`SELECT id, goal, status, current_step, session_id, result, created_at, updated_at
		 FROM workflows WHERE id=?`, workflowID).Scan(
		&wf.ID, &wf.Goal, &wf.Status, &wf.CurrentStep, &wf.SessionID, &wf.Result,
		&created, &updated); err != nil {
		return nil, fmt.Errorf("agents: workflow %s not found: %w", workflowID, err)
	}
	wf.CreatedAt = time.UnixMilli(created)
	wf.UpdatedAt = time.UnixMilli(updated)

	rows, err := s.db.Query(
		`SELECT id, description, tool_name, params_json, status, result, error, attempts, max_retries
		 FROM workflow_steps WHERE workflow_id=? ORDER BY idx ASC`, workflowID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var st WorkflowStep
		var params string
		if err := rows.Scan(&st.ID, &st.Description, &st.ToolName, &params,
			&st.Status, &st.Result, &st.Error, &st.Attempts, &st.MaxRetries); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(params), &st.Params)
		wf.Steps = append(wf.Steps, st)
	}
	return wf, rows.Err()
}
