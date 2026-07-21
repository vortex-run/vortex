// This file implements RunStore: SQLite-backed durability for orchestration
// runs (production audit H3). The planned task DAG, every task state
// transition, and the shared memory are persisted so an interrupted run can be
// resumed after a crash at task granularity — completed tasks keep their
// results and are never re-executed (exactly-once); tasks that were mid-flight
// re-run (at-least-once). It follows the SQLite conventions established by
// internal/agents/workflow.go (same driver, DSN pragmas, single connection,
// idempotent schema).
package orchestration

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Run statuses. A run is resumable while "running"; "done" and "failed" are
// terminal.
const (
	RunRunning = "running"
	RunDone    = "done"
	RunFailed  = "failed"
)

// RunInfo summarises a stored run for listings and resume.
type RunInfo struct {
	ID        string
	Goal      string
	SessionID string
}

// RunStore persists orchestration runs in SQLite. Safe for concurrent use —
// the database/sql pool serialises writes (one connection) and SQLite runs in
// WAL mode for concurrent reads.
type RunStore struct {
	db   *sql.DB
	path string
}

// runSchema is applied on open; CREATE ... IF NOT EXISTS makes it idempotent.
// orch_tasks.idx preserves plan order — Claim fairness depends on queue
// insertion order, so LoadRun must return tasks in the order they were planned.
const runSchema = `
CREATE TABLE IF NOT EXISTS orch_runs (
  id           TEXT PRIMARY KEY,
  goal         TEXT NOT NULL,
  session_id   TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL,
  result       TEXT NOT NULL DEFAULT '',
  resume_count INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orch_runs_status ON orch_runs(status);

CREATE TABLE IF NOT EXISTS orch_tasks (
  run_id      TEXT NOT NULL,
  id          TEXT NOT NULL,
  idx         INTEGER NOT NULL,
  name        TEXT NOT NULL DEFAULT '',
  agent_type  TEXT NOT NULL DEFAULT '',
  input       TEXT NOT NULL DEFAULT '',
  depends_on  TEXT NOT NULL DEFAULT '[]',
  state       TEXT NOT NULL,
  result      TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT '',
  meta        TEXT NOT NULL DEFAULT '{}',
  attempts    INTEGER NOT NULL DEFAULT 0,
  started_at  INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, id),
  FOREIGN KEY (run_id) REFERENCES orch_runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS orch_memory (
  run_id     TEXT NOT NULL,
  key        TEXT NOT NULL,
  value      TEXT NOT NULL DEFAULT 'null',
  author     TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (run_id, key),
  FOREIGN KEY (run_id) REFERENCES orch_runs(id) ON DELETE CASCADE
);
`

// NewRunStore opens (creating if needed) a SQLite-backed run store at dbPath,
// applying the schema and enabling WAL + foreign keys.
func NewRunStore(dbPath string) (*RunStore, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("orchestration: creating run store dir: %w", err)
		}
	}
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("orchestration: opening run db: %w", err)
	}
	// SQLite is a single file; one writer at a time avoids "database is locked".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(runSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("orchestration: applying run schema: %w", err)
	}
	return &RunStore{db: db, path: dbPath}, nil
}

// Close closes the underlying database.
func (s *RunStore) Close() error { return s.db.Close() }

// newRunID returns a 32-hex-char run identifier (16 random bytes) — the same
// convention as the agents package's session/workflow IDs.
func newRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("orchestration: generating run id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// CreateRun persists a new running run with its full planned DAG in one
// transaction and returns the run ID.
func (s *RunStore) CreateRun(goal, sessionID string, tasks []*Task) (string, error) {
	id, err := newRunID()
	if err != nil {
		return "", err
	}
	now := time.Now().UnixMilli()

	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT INTO orch_runs(id, goal, session_id, status, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		id, goal, sessionID, RunRunning, now, now); err != nil {
		return "", fmt.Errorf("orchestration: inserting run: %w", err)
	}
	for i, t := range tasks {
		if err := upsertTask(tx, id, i, t); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// SaveTask upserts one task's current state. A transition to running also
// increments the attempt counter (in SQL, so the Task struct needs no field).
func (s *RunStore) SaveTask(runID string, t *Task) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// idx -1 sentinel: the ON CONFLICT update below never touches idx, and
	// SaveTask is only ever called for tasks CreateRun already inserted.
	if err := upsertTask(tx, runID, -1, t); err != nil {
		return err
	}
	if t.State == StateRunning {
		if _, err := tx.Exec(
			`UPDATE orch_tasks SET attempts = attempts + 1 WHERE run_id=? AND id=?`,
			runID, t.ID); err != nil {
			return fmt.Errorf("orchestration: bumping task attempts: %w", err)
		}
	}
	return tx.Commit()
}

// SyncTasks upserts every task's state in one transaction — the end-of-run
// reconcile that captures transitions made inside the queue (dep-failure marks
// in Claim/drainBlocked never pass through runTask) and self-corrects any
// dropped best-effort writes.
func (s *RunStore) SyncTasks(runID string, tasks []*Task) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, t := range tasks {
		if err := upsertTask(tx, runID, -1, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// upsertTask inserts or updates one task row. idx is only written on insert
// (CreateRun); updates never move a task's plan position. An empty state is
// stored as pending — planner tasks arrive unset and the queue's Add applies
// the same default.
func upsertTask(tx *sql.Tx, runID string, idx int, t *Task) error {
	state := t.State
	if state == "" {
		state = StatePending
	}
	deps, err := json.Marshal(t.DependsOn)
	if err != nil {
		return fmt.Errorf("orchestration: marshaling task deps: %w", err)
	}
	if t.DependsOn == nil {
		deps = []byte("[]")
	}
	meta, err := json.Marshal(t.Meta)
	if err != nil {
		return fmt.Errorf("orchestration: marshaling task meta: %w", err)
	}
	if t.Meta == nil {
		meta = []byte("{}")
	}
	_, err = tx.Exec(
		`INSERT INTO orch_tasks(run_id, id, idx, name, agent_type, input, depends_on, state, result, error, meta, started_at, finished_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(run_id, id) DO UPDATE SET
		   state=excluded.state, result=excluded.result, error=excluded.error,
		   meta=excluded.meta, started_at=excluded.started_at, finished_at=excluded.finished_at`,
		runID, t.ID, idx, t.Name, t.AgentType, t.Input, string(deps),
		string(state), t.Result, t.Error, string(meta),
		unixMilliOrZero(t.StartedAt), unixMilliOrZero(t.FinishedAt))
	if err != nil {
		return fmt.Errorf("orchestration: upserting task %s: %w", t.ID, err)
	}
	return nil
}

// SyncMemory upserts a shared-memory snapshot for the run in one transaction.
// Values are JSON-encoded (executor results are strings in practice).
func (s *RunStore) SyncMemory(runID string, snap map[string]MemoryEntry) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for key, e := range snap {
		val, merr := json.Marshal(e.Value)
		if merr != nil {
			// Unserializable values (shouldn't occur — results are strings)
			// are stored as null rather than failing the whole snapshot.
			val = []byte("null")
		}
		if _, err := tx.Exec(
			`INSERT INTO orch_memory(run_id, key, value, author, updated_at) VALUES(?,?,?,?,?)
			 ON CONFLICT(run_id, key) DO UPDATE SET
			   value=excluded.value, author=excluded.author, updated_at=excluded.updated_at`,
			runID, key, string(val), e.Author, e.UpdatedAt.UnixMilli()); err != nil {
			return fmt.Errorf("orchestration: upserting memory %s: %w", key, err)
		}
	}
	return tx.Commit()
}

// FinishRun marks a run terminal with its final status (done/failed) and a
// result summary or failure reason.
func (s *RunStore) FinishRun(runID, status, result string) error {
	_, err := s.db.Exec(
		`UPDATE orch_runs SET status=?, result=?, updated_at=? WHERE id=?`,
		status, result, time.Now().UnixMilli(), runID)
	return err
}

// MarkResumed increments and returns the run's resume counter. The caller caps
// it (maxResumeAttempts) so a poison run that crashes the process cannot loop
// through resume forever.
func (s *RunStore) MarkResumed(runID string) (int, error) {
	if _, err := s.db.Exec(
		`UPDATE orch_runs SET resume_count = resume_count + 1, updated_at=? WHERE id=?`,
		time.Now().UnixMilli(), runID); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRow(`SELECT resume_count FROM orch_runs WHERE id=?`, runID).Scan(&n)
	return n, err
}

// ListIncomplete returns resumable runs (status running), oldest first.
func (s *RunStore) ListIncomplete() ([]RunInfo, error) {
	rows, err := s.db.Query(
		`SELECT id, goal, session_id FROM orch_runs WHERE status=? ORDER BY created_at ASC`,
		RunRunning)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RunInfo
	for rows.Next() {
		var r RunInfo
		if err := rows.Scan(&r.ID, &r.Goal, &r.SessionID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadRun returns a run's tasks (in plan order) and shared-memory entries.
func (s *RunStore) LoadRun(runID string) ([]*Task, []MemoryEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, name, agent_type, input, depends_on, state, result, error, meta, started_at, finished_at
		 FROM orch_tasks WHERE run_id=? ORDER BY idx ASC`, runID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	var tasks []*Task
	for rows.Next() {
		var t Task
		var deps, meta, state string
		var started, finished int64
		if err := rows.Scan(&t.ID, &t.Name, &t.AgentType, &t.Input, &deps,
			&state, &t.Result, &t.Error, &meta, &started, &finished); err != nil {
			return nil, nil, err
		}
		t.State = TaskState(state)
		_ = json.Unmarshal([]byte(deps), &t.DependsOn)
		_ = json.Unmarshal([]byte(meta), &t.Meta)
		if started > 0 {
			t.StartedAt = time.UnixMilli(started)
		}
		if finished > 0 {
			t.FinishedAt = time.UnixMilli(finished)
		}
		tasks = append(tasks, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	mrows, err := s.db.Query(
		`SELECT key, value, author, updated_at FROM orch_memory WHERE run_id=?`, runID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = mrows.Close() }()
	var memory []MemoryEntry
	for mrows.Next() {
		var e MemoryEntry
		var val string
		var updated int64
		if err := mrows.Scan(&e.Key, &val, &e.Author, &updated); err != nil {
			return nil, nil, err
		}
		_ = json.Unmarshal([]byte(val), &e.Value)
		e.UpdatedAt = time.UnixMilli(updated)
		memory = append(memory, e)
	}
	return tasks, memory, mrows.Err()
}

// unixMilliOrZero converts a time to unix ms, mapping the zero time to 0 so
// unset timestamps round-trip as zero values.
func unixMilliOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
