// Package session provides durable persistence for OmniHarness. It owns the
// SQLite schema and stores everything needed to resume meaningful work:
// sessions, tasks, agents, events, model calls, tool calls, evaluations,
// checkpoints and project memory. The package does not import the agent or
// evaluate packages — records are self-contained JSON.
package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"omniharness/internal/event"
	"omniharness/internal/id"
	"omniharness/internal/task"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  title      TEXT NOT NULL DEFAULT '',
  cwd        TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'active',
  summary    TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
  id           TEXT PRIMARY KEY,
  session_id   TEXT NOT NULL,
  spec_json    TEXT NOT NULL,
  profile_json TEXT NOT NULL DEFAULT '{}',
  strategy     TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL,
  repairs      INTEGER NOT NULL DEFAULT 0,
  result_json  TEXT,
  error        TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_session ON tasks(session_id);
CREATE TABLE IF NOT EXISTS events (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  event_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id, seq);
CREATE TABLE IF NOT EXISTS model_calls (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  task_id    TEXT NOT NULL DEFAULT '',
  agent_id   TEXT NOT NULL DEFAULT '',
  provider   TEXT NOT NULL DEFAULT '',
  model      TEXT NOT NULL,
  tokens_in  INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  cost_usd   REAL NOT NULL DEFAULT 0,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  status     TEXT NOT NULL,
  error      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_calls_session ON model_calls(session_id);
CREATE TABLE IF NOT EXISTS tool_calls (
  id          TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL,
  task_id     TEXT NOT NULL DEFAULT '',
  agent_id    TEXT NOT NULL DEFAULT '',
  tool        TEXT NOT NULL,
  status      TEXT NOT NULL,
  risk        TEXT NOT NULL DEFAULT 'low',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  error       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tool_calls_session ON tool_calls(session_id);
CREATE TABLE IF NOT EXISTS evaluations (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  task_id    TEXT NOT NULL DEFAULT '',
  evaluator  TEXT NOT NULL,
  outcome    TEXT NOT NULL,
  detail     TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_evaluations_session ON evaluations(session_id);
CREATE TABLE IF NOT EXISTS agents (
  id              TEXT PRIMARY KEY,
  session_id      TEXT NOT NULL,
  task_id         TEXT NOT NULL DEFAULT '',
  role            TEXT NOT NULL,
  model           TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL,
  transcript_json TEXT NOT NULL DEFAULT '[]',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agents_session ON agents(session_id);
CREATE TABLE IF NOT EXISTS checkpoints (
  id           TEXT PRIMARY KEY,
  session_id   TEXT NOT NULL,
  task_id      TEXT NOT NULL DEFAULT '',
  agent_id     TEXT NOT NULL DEFAULT '',
  reason       TEXT NOT NULL DEFAULT '',
  payload_json TEXT NOT NULL,
  created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_checkpoints_session ON checkpoints(session_id);
CREATE TABLE IF NOT EXISTS project_memory (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_key TEXT NOT NULL,
  kind        TEXT NOT NULL,
  content     TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_project ON project_memory(project_key, kind);
`

// Session is the durable record of a working session.
type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CWD       string    `json:"cwd"`
	Status    string    `json:"status"` // active | ended
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ModelCall is a recorded model request/response.
type ModelCall struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	TaskID    string    `json:"taskId,omitempty"`
	AgentID   string    `json:"agentId,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model"`
	TokensIn  int64     `json:"tokensIn"`
	TokensOut int64     `json:"tokensOut"`
	CostUSD   float64   `json:"costUsd"`
	LatencyMS int64     `json:"latencyMs"`
	Status    string    `json:"status"` // ok | failed
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// ToolCall is a recorded tool invocation.
type ToolCall struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	TaskID    string    `json:"taskId,omitempty"`
	AgentID   string    `json:"agentId,omitempty"`
	Tool      string    `json:"tool"`
	Status    string    `json:"status"` // completed | failed | denied | cancelled
	Risk      string    `json:"risk"`
	DurationMS int64    `json:"durationMs"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Evaluation is a recorded evaluation outcome.
type Evaluation struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	TaskID    string    `json:"taskId,omitempty"`
	Evaluator string    `json:"evaluator"`
	Outcome   string    `json:"outcome"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// AgentRecord is the durable form of an agent.
type AgentRecord struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"sessionId"`
	TaskID     string    `json:"taskId,omitempty"`
	Role       string    `json:"role"`
	Model      string    `json:"model,omitempty"`
	Status     string    `json:"status"`
	Transcript []byte    `json:"-"` // JSON messages, not stored in the struct field
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// ProjectMemory is a durable project-level memory entry.
type ProjectMemory struct {
	ID        int64     `json:"id"`
	ProjectKey string   `json:"projectKey"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Checkpoint is a resumable snapshot of work.
type Checkpoint struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"sessionId"`
	TaskID     string    `json:"taskId,omitempty"`
	AgentID    string    `json:"agentId,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Payload    []byte    `json:"payload"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Store is a SQLite-backed session store.
type Store struct {
	db *sql.DB
	// wmu serializes writes. SQLite allows one writer; with WAL + several
	// connection pools, concurrent Exec calls can hit SQLITE_BUSY. Serializing
	// writes eliminates that failure class deterministically (reads stay
	// concurrent).
	wmu sync.Mutex
}

// Open opens (creating if needed) the store at dir/omniharness.db.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create persistence dir: %w", err)
	}
	dsn := "file:" + url.PathEscape(filepath.Join(dir, "omniharness.db")) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for advanced queries (memory, telemetry).
func (s *Store) DB() *sql.DB { return s.db }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// exec runs a write statement under the write mutex.
func (s *Store) exec(q string, args ...any) (sql.Result, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.db.Exec(q, args...)
}

// ---- Sessions ----

// CreateSession inserts a session.
func (s *Store) CreateSession(ss *Session) error {
	if ss.ID == "" {
		ss.ID = id.New()
	}
	t := now()
	ss.CreatedAt, ss.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	_, err := s.exec(`INSERT INTO sessions (id, title, cwd, status, summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ss.ID, ss.Title, ss.CWD, ss.Status, ss.Summary, t, t)
	return err
}

// GetSession loads a session by id.
func (s *Store) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(`SELECT id, title, cwd, status, summary, created_at, updated_at FROM sessions WHERE id = ?`, id)
	var ss Session
	var created, updated string
	if err := row.Scan(&ss.ID, &ss.Title, &ss.CWD, &ss.Status, &ss.Summary, &created, &updated); err != nil {
		return nil, err
	}
	ss.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	ss.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &ss, nil
}

// ListSessions returns the most recent sessions, newest first.
func (s *Store) ListSessions(limit int) ([]*Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, title, cwd, status, summary, created_at, updated_at
		FROM sessions ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		var ss Session
		var created, updated string
		if err := rows.Scan(&ss.ID, &ss.Title, &ss.CWD, &ss.Status, &ss.Summary, &created, &updated); err != nil {
			return nil, err
		}
		ss.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		ss.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, &ss)
	}
	return out, rows.Err()
}

// EndSession marks a session ended.
func (s *Store) EndSession(id, summary string) error {
	_, err := s.exec(`UPDATE sessions SET status = 'ended', summary = ?, updated_at = ? WHERE id = ?`,
		summary, now(), id)
	return err
}

// ---- Tasks ----

// CreateTask inserts a task.
func (s *Store) CreateTask(t *task.Task) error {
	t.CreatedAt, t.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	spec, _ := json.Marshal(t.Spec)
	profile, _ := json.Marshal(t.Profile)
	var result string
	if t.Result != nil {
		b, _ := json.Marshal(t.Result)
		result = string(b)
	}
	_, err := s.exec(`INSERT INTO tasks (id, session_id, spec_json, profile_json, strategy, status, repairs, result_json, error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)`,
		t.ID, t.SessionID, string(spec), string(profile), t.Strategy, string(t.Status), t.Repairs, result, t.Error, now(), now())
	return err
}

// UpdateTask persists task changes.
func (s *Store) UpdateTask(t *task.Task) error {
	t.UpdatedAt = time.Now().UTC()
	profile, _ := json.Marshal(t.Profile)
	var result string
	if t.Result != nil {
		b, _ := json.Marshal(t.Result)
		result = string(b)
	}
	_, err := s.exec(`UPDATE tasks SET profile_json = ?, strategy = ?, status = ?, repairs = ?, result_json = NULLIF(?, ''), error = ?, updated_at = ?
		WHERE id = ?`,
		string(profile), t.Strategy, string(t.Status), t.Repairs, result, t.Error, now(), t.ID)
	return err
}

// GetTask loads a task.
func (s *Store) GetTask(id string) (*task.Task, error) {
	row := s.db.QueryRow(`SELECT id, session_id, spec_json, profile_json, strategy, status, repairs, result_json, error, created_at, updated_at
		FROM tasks WHERE id = ?`, id)
	var t task.Task
	var spec, profile, result, created, updated string
	var resultNull sql.NullString
	if err := row.Scan(&t.ID, &t.SessionID, &spec, &profile, &t.Strategy, &t.Status, &t.Repairs, &resultNull, &t.Error, &created, &updated); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(spec), &t.Spec)
	_ = json.Unmarshal([]byte(profile), &t.Profile)
	if resultNull.Valid {
		var r task.Result
		_ = json.Unmarshal([]byte(resultNull.String), &r)
		t.Result = &r
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	_ = result
	return &t, nil
}

// TasksBySession lists tasks for a session, newest first.
func (s *Store) TasksBySession(sessionID string) ([]*task.Task, error) {
	rows, err := s.db.Query(`SELECT id, session_id, spec_json, profile_json, strategy, status, repairs, result_json, error, created_at, updated_at
		FROM tasks WHERE session_id = ? ORDER BY created_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*task.Task
	for rows.Next() {
		var t task.Task
		var spec, profile, created, updated string
		var resultNull sql.NullString
		if err := rows.Scan(&t.ID, &t.SessionID, &spec, &profile, &t.Strategy, &t.Status, &t.Repairs, &resultNull, &t.Error, &created, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(spec), &t.Spec)
		_ = json.Unmarshal([]byte(profile), &t.Profile)
		if resultNull.Valid {
			var r task.Result
			_ = json.Unmarshal([]byte(resultNull.String), &r)
			t.Result = &r
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, &t)
	}
	return out, rows.Err()
}

// ---- Events ----

// AppendEvent persists an event to the session log.
func (s *Store) AppendEvent(sessionID string, e event.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.exec(`INSERT INTO events (session_id, event_json, created_at) VALUES (?, ?, ?)`,
		sessionID, string(b), now())
	return err
}

// Events returns persisted events for a session in chronological order.
func (s *Store) Events(sessionID string, limit int) ([]event.Event, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.Query(`SELECT event_json FROM events WHERE session_id = ? ORDER BY seq ASC LIMIT ?`,
		sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []event.Event
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e event.Event
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			continue // skip unreadable events rather than corrupting the log
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- Model calls ----

// RecordModelCall inserts a model call record.
func (s *Store) RecordModelCall(mc *ModelCall) error {
	if mc.ID == "" {
		mc.ID = id.New()
	}
	mc.CreatedAt = time.Now().UTC()
	_, err := s.exec(`INSERT INTO model_calls
		(id, session_id, task_id, agent_id, provider, model, tokens_in, tokens_out, cost_usd, latency_ms, status, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		mc.ID, mc.SessionID, mc.TaskID, mc.AgentID, mc.Provider, mc.Model, mc.TokensIn, mc.TokensOut,
		mc.CostUSD, mc.LatencyMS, mc.Status, mc.Error, now())
	return err
}

// ModelCalls lists model calls for a session, newest first.
func (s *Store) ModelCalls(sessionID string) ([]ModelCall, error) {
	rows, err := s.db.Query(`SELECT id, session_id, task_id, agent_id, provider, model, tokens_in, tokens_out,
		cost_usd, latency_ms, status, error, created_at FROM model_calls WHERE session_id = ? ORDER BY created_at DESC LIMIT 1000`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanModelCalls(rows)
}

// ModelCallsForTask lists model calls for one task.
func (s *Store) ModelCallsForTask(taskID string) ([]ModelCall, error) {
	rows, err := s.db.Query(`SELECT id, session_id, task_id, agent_id, provider, model, tokens_in, tokens_out,
		cost_usd, latency_ms, status, error, created_at FROM model_calls WHERE task_id = ? ORDER BY created_at ASC`,
		taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanModelCalls(rows)
}

func scanModelCalls(rows *sql.Rows) ([]ModelCall, error) {
	var out []ModelCall
	for rows.Next() {
		var mc ModelCall
		var created string
		if err := rows.Scan(&mc.ID, &mc.SessionID, &mc.TaskID, &mc.AgentID, &mc.Provider, &mc.Model,
			&mc.TokensIn, &mc.TokensOut, &mc.CostUSD, &mc.LatencyMS, &mc.Status, &mc.Error, &created); err != nil {
			return nil, err
		}
		mc.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, mc)
	}
	return out, rows.Err()
}

// ---- Tool calls ----

// RecordToolCall inserts a tool call record.
func (s *Store) RecordToolCall(tc *ToolCall) error {
	if tc.ID == "" {
		tc.ID = id.New()
	}
	tc.CreatedAt = time.Now().UTC()
	_, err := s.exec(`INSERT INTO tool_calls
		(id, session_id, task_id, agent_id, tool, status, risk, duration_ms, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tc.ID, tc.SessionID, tc.TaskID, tc.AgentID, tc.Tool, tc.Status, tc.Risk, tc.DurationMS, tc.Error, now())
	return err
}

// ToolCalls lists tool calls for a session.
func (s *Store) ToolCalls(sessionID string) ([]ToolCall, error) {
	rows, err := s.db.Query(`SELECT id, session_id, task_id, agent_id, tool, status, risk, duration_ms, error, created_at
		FROM tool_calls WHERE session_id = ? ORDER BY created_at DESC LIMIT 1000`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolCall
	for rows.Next() {
		var tc ToolCall
		var created string
		if err := rows.Scan(&tc.ID, &tc.SessionID, &tc.TaskID, &tc.AgentID, &tc.Tool, &tc.Status,
			&tc.Risk, &tc.DurationMS, &tc.Error, &created); err != nil {
			return nil, err
		}
		tc.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, tc)
	}
	return out, rows.Err()
}

// ---- Evaluations ----

// RecordEvaluation inserts an evaluation record.
func (s *Store) RecordEvaluation(ev *Evaluation) error {
	if ev.ID == "" {
		ev.ID = id.New()
	}
	ev.CreatedAt = time.Now().UTC()
	_, err := s.exec(`INSERT INTO evaluations (id, session_id, task_id, evaluator, outcome, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.SessionID, ev.TaskID, ev.Evaluator, ev.Outcome, ev.Detail, now())
	return err
}

// EvaluationsForTask lists evaluations for a task.
func (s *Store) EvaluationsForTask(taskID string) ([]Evaluation, error) {
	rows, err := s.db.Query(`SELECT id, session_id, task_id, evaluator, outcome, detail, created_at
		FROM evaluations WHERE task_id = ? ORDER BY created_at ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Evaluation
	for rows.Next() {
		var ev Evaluation
		var created string
		if err := rows.Scan(&ev.ID, &ev.SessionID, &ev.TaskID, &ev.Evaluator, &ev.Outcome, &ev.Detail, &created); err != nil {
			return nil, err
		}
		ev.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ---- Agents ----

// UpsertAgent inserts or updates an agent record.
func (s *Store) UpsertAgent(a *AgentRecord) error {
	a.UpdatedAt = time.Now().UTC()
	_, err := s.exec(`INSERT INTO agents (id, session_id, task_id, role, model, status, transcript_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			model = excluded.model,
			transcript_json = excluded.transcript_json,
			updated_at = excluded.updated_at`,
		a.ID, a.SessionID, a.TaskID, a.Role, a.Model, a.Status, string(a.Transcript), now(), now())
	return err
}

// Agent loads an agent record with its transcript.
func (s *Store) Agent(id string) (*AgentRecord, error) {
	row := s.db.QueryRow(`SELECT id, session_id, task_id, role, model, status, transcript_json, created_at, updated_at
		FROM agents WHERE id = ?`, id)
	var a AgentRecord
	var transcript, created, updated string
	if err := row.Scan(&a.ID, &a.SessionID, &a.TaskID, &a.Role, &a.Model, &a.Status, &transcript, &created, &updated); err != nil {
		return nil, err
	}
	a.Transcript = []byte(transcript)
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &a, nil
}

// AgentsForTask lists agents for a task.
func (s *Store) AgentsForTask(taskID string) ([]AgentRecord, error) {
	rows, err := s.db.Query(`SELECT id, session_id, task_id, role, model, status, transcript_json, created_at, updated_at
		FROM agents WHERE task_id = ? ORDER BY created_at ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentRecord
	for rows.Next() {
		var a AgentRecord
		var transcript, created, updated string
		if err := rows.Scan(&a.ID, &a.SessionID, &a.TaskID, &a.Role, &a.Model, &a.Status, &transcript, &created, &updated); err != nil {
			return nil, err
		}
		a.Transcript = []byte(transcript)
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- Checkpoints ----

// SaveCheckpoint stores a checkpoint.
func (s *Store) SaveCheckpoint(c *Checkpoint) error {
	if c.ID == "" {
		c.ID = id.New()
	}
	c.CreatedAt = time.Now().UTC()
	_, err := s.exec(`INSERT INTO checkpoints (id, session_id, task_id, agent_id, reason, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.SessionID, c.TaskID, c.AgentID, c.Reason, string(c.Payload), now())
	return err
}

// LatestCheckpoint returns the most recent checkpoint for a session.
func (s *Store) LatestCheckpoint(sessionID string) (*Checkpoint, error) {
	row := s.db.QueryRow(`SELECT id, session_id, task_id, agent_id, reason, payload_json, created_at
		FROM checkpoints WHERE session_id = ? ORDER BY created_at DESC LIMIT 1`, sessionID)
	var c Checkpoint
	var payload, created string
	if err := row.Scan(&c.ID, &c.SessionID, &c.TaskID, &c.AgentID, &c.Reason, &payload, &created); err != nil {
		return nil, err
	}
	c.Payload = []byte(payload)
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return &c, nil
}

// ---- Project memory ----

// PutMemory upserts a project memory entry by (project_key, kind).
func (s *Store) PutMemory(projectKey, kind, content string) error {
	_, err := s.exec(`INSERT INTO project_memory (project_key, kind, content, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_key, kind) DO UPDATE SET content = excluded.content, updated_at = excluded.updated_at`,
		projectKey, kind, content, now(), now())
	return err
}

// GetMemory loads a project memory entry.
func (s *Store) GetMemory(projectKey, kind string) (*ProjectMemory, error) {
	row := s.db.QueryRow(`SELECT id, project_key, kind, content, created_at, updated_at
		FROM project_memory WHERE project_key = ? AND kind = ?`, projectKey, kind)
	var m ProjectMemory
	var created, updated string
	if err := row.Scan(&m.ID, &m.ProjectKey, &m.Kind, &m.Content, &created, &updated); err != nil {
		return nil, err
	}
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &m, nil
}

// ListMemory lists project memory entries for a project.
func (s *Store) ListMemory(projectKey string) ([]ProjectMemory, error) {
	rows, err := s.db.Query(`SELECT id, project_key, kind, content, created_at, updated_at
		FROM project_memory WHERE project_key = ? ORDER BY updated_at DESC`, projectKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectMemory
	for rows.Next() {
		var m ProjectMemory
		var created, updated string
		if err := rows.Scan(&m.ID, &m.ProjectKey, &m.Kind, &m.Content, &created, &updated); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, m)
	}
	return out, rows.Err()
}
