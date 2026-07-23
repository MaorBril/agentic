// Package store persists sessions and usage events in SQLite.
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the agentic database. modernc.org/sqlite
// uses _pragma=name(value) DSN syntax; WAL lets CLI readers run while the
// router leader holds the single write connection.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenReadOnly opens the database for CLI readers (cost, statusline).
func OpenReadOnly(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  profile    TEXT,
  work_dir   TEXT,
  started_at INTEGER,
  ended_at   INTEGER
);
CREATE TABLE IF NOT EXISTS usage_events (
  id                INTEGER PRIMARY KEY,
  ts                INTEGER NOT NULL,
  session_id        TEXT,
  profile           TEXT,
  provider          TEXT,
  model             TEXT,
  model_alias       TEXT,
  input_tokens      INTEGER,
  output_tokens     INTEGER,
  cache_read_tokens INTEGER,
  cache_write_tokens INTEGER,
  cost_usd          REAL,
  priced            INTEGER,
  request_id        TEXT,
  status            INTEGER,
  err_type          TEXT
);
CREATE TABLE IF NOT EXISTS route_decisions (
  session_id TEXT PRIMARY KEY,
  alias      TEXT,
  tier       TEXT,
  model      TEXT,
  at         INTEGER
);
CREATE TABLE IF NOT EXISTS goal_decisions (
  session_id TEXT PRIMARY KEY,
  goal       INTEGER,
  reason     TEXT,
  at         INTEGER
);
CREATE INDEX IF NOT EXISTS idx_usage_ts ON usage_events(ts);
CREATE INDEX IF NOT EXISTS idx_usage_session ON usage_events(session_id);
`)
	if err != nil {
		return err
	}
	// Additive columns for existing databases. ctx_budget is the routed
	// model's context budget at request time; reported_input is the
	// (possibly scaled) input-side total sent to the client — together they
	// make context-scaling behavior queryable per session.
	for _, col := range []string{
		"ALTER TABLE usage_events ADD COLUMN ctx_budget INTEGER DEFAULT 0",
		"ALTER TABLE usage_events ADD COLUMN reported_input INTEGER DEFAULT 0",
		"ALTER TABLE route_decisions ADD COLUMN reason TEXT DEFAULT ''",
	} {
		if _, err := s.db.Exec(col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
}

type UsageEvent struct {
	TS               time.Time
	SessionID        string
	Profile          string
	Provider         string
	Model            string
	Alias            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
	Priced           bool
	RequestID        string
	Status           int
	ErrType          string
	CtxBudget        int   // model's context budget (0 = unknown/unscaled)
	ReportedInput    int64 // input-side tokens as reported to the client
}

func (s *Store) RecordUsage(e UsageEvent) error {
	_, err := s.db.Exec(`INSERT INTO usage_events
(ts, session_id, profile, provider, model, model_alias,
 input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
 cost_usd, priced, request_id, status, err_type, ctx_budget, reported_input)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.TS.Unix(), e.SessionID, e.Profile, e.Provider, e.Model, e.Alias,
		e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens,
		e.CostUSD, boolToInt(e.Priced), e.RequestID, e.Status, e.ErrType,
		e.CtxBudget, e.ReportedInput)
	return err
}

func (s *Store) StartSession(id, profile, workDir string, at time.Time) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO sessions (id, profile, work_dir, started_at) VALUES (?,?,?,?)`,
		id, profile, workDir, at.Unix())
	return err
}

func (s *Store) EndSession(id string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE sessions SET ended_at = ? WHERE id = ?`, at.Unix(), id)
	return err
}

// ActiveSession is a launched session that has not recorded an end. A
// session that died without cleanup (kill -9, crash) lingers here — LastSeen
// (the newest usage event) lets callers flag those instead of trusting the
// row blindly.
type ActiveSession struct {
	ID        string
	Profile   string
	WorkDir   string
	StartedAt time.Time
	LastSeen  time.Time // zero when no usage was ever attributed
}

// ActiveSessions returns open sessions, most recently started first.
func (s *Store) ActiveSessions() ([]ActiveSession, error) {
	rows, err := s.db.Query(`SELECT id, COALESCE(profile,''), COALESCE(work_dir,''), started_at,
  COALESCE((SELECT MAX(ts) FROM usage_events u WHERE u.session_id = sessions.id), 0)
FROM sessions WHERE ended_at IS NULL ORDER BY started_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveSession
	for rows.Next() {
		var a ActiveSession
		var started, seen int64
		if err := rows.Scan(&a.ID, &a.Profile, &a.WorkDir, &started, &seen); err != nil {
			return nil, err
		}
		a.StartedAt = time.Unix(started, 0)
		if seen > 0 {
			a.LastSeen = time.Unix(seen, 0)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RecordRouteDecision persists the auto-router's tier/model choice for a
// session's current turn, overwriting any prior decision for that session
// (a session re-classifies as new user turns arrive). reason is a free-text
// note — e.g. "size:light→standard" when size-aware routing remapped a tier
// — empty for a plain classifier decision.
func (s *Store) RecordRouteDecision(sessionID, alias, tier, model, reason string, at time.Time) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO route_decisions
(session_id, alias, tier, model, reason, at) VALUES (?,?,?,?,?,?)`,
		sessionID, alias, tier, model, reason, at.Unix())
	return err
}

// LatestRouteDecision returns the most recent auto-router decision recorded
// for a session, if any. ok is false when no decision has been recorded yet
// (e.g. the profile isn't using a routing alias).
func (s *Store) LatestRouteDecision(sessionID string) (alias, tier, model, reason string, ok bool, err error) {
	row := s.db.QueryRow(`SELECT alias, tier, model, reason FROM route_decisions WHERE session_id = ?`, sessionID)
	err = row.Scan(&alias, &tier, &model, &reason)
	if err == sql.ErrNoRows {
		return "", "", "", "", false, nil
	}
	return alias, tier, model, reason, err == nil, err
}

// RecordGoalDecision persists the auto-goal classifier's verdict for a
// session's current turn, overwriting any prior decision for that session
// (a session re-classifies as new user turns arrive).
func (s *Store) RecordGoalDecision(sessionID string, goal bool, reason string, at time.Time) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO goal_decisions
(session_id, goal, reason, at) VALUES (?,?,?,?)`,
		sessionID, boolToInt(goal), reason, at.Unix())
	return err
}

// LatestGoalDecision returns the most recent auto-goal verdict recorded for
// a session, if any. ok is false when no decision has been recorded yet.
func (s *Store) LatestGoalDecision(sessionID string) (goal bool, reason string, ok bool, err error) {
	row := s.db.QueryRow(`SELECT goal, reason FROM goal_decisions WHERE session_id = ?`, sessionID)
	var g int
	err = row.Scan(&g, &reason)
	if err == sql.ErrNoRows {
		return false, "", false, nil
	}
	return g != 0, reason, err == nil, err
}

// ContextEvent is one request's context-fullness datapoint: how full the
// routed model really was vs what the client was told. The research surface
// for tuning context_window/effective_context.
type ContextEvent struct {
	TS            time.Time
	Model         string
	TrueInput     int64 // input + cache read + cache write, as billed
	ReportedInput int64 // what the client's context gauge saw
	CtxBudget     int   // model budget at request time (0 = unscaled)
	Status        int
	ErrType       string
}

// ContextTrajectory returns a session's per-request context datapoints in
// time order. A drop in TrueInput between consecutive rows is a compaction.
func (s *Store) ContextTrajectory(sessionID string) ([]ContextEvent, error) {
	rows, err := s.db.Query(`SELECT ts, model,
  COALESCE(input_tokens,0)+COALESCE(cache_read_tokens,0)+COALESCE(cache_write_tokens,0),
  COALESCE(reported_input,0), COALESCE(ctx_budget,0), status, COALESCE(err_type,'')
FROM usage_events WHERE session_id = ? ORDER BY ts, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContextEvent
	for rows.Next() {
		var e ContextEvent
		var ts int64
		if err := rows.Scan(&ts, &e.Model, &e.TrueInput, &e.ReportedInput, &e.CtxBudget, &e.Status, &e.ErrType); err != nil {
			return nil, err
		}
		e.TS = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestSessionID returns the session with the most recent usage event, or
// "" when nothing attributed has been recorded.
func (s *Store) LatestSessionID() (string, error) {
	row := s.db.QueryRow(`SELECT session_id FROM usage_events
WHERE session_id != '' ORDER BY ts DESC, id DESC LIMIT 1`)
	var id string
	err := row.Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// SpendRow is one line of a cost report.
type SpendRow struct {
	Key          string // model, profile, or session id depending on grouping
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	Unpriced     int64 // count of events with priced=0
}

// SpendSince aggregates usage from `since`, grouped by "model", "profile",
// or "session".
func (s *Store) SpendSince(since time.Time, groupBy string) ([]SpendRow, error) {
	col := map[string]string{"model": "model", "profile": "profile", "session": "session_id"}[groupBy]
	if col == "" {
		return nil, fmt.Errorf("unknown grouping %q", groupBy)
	}
	rows, err := s.db.Query(`SELECT `+col+`,
  COALESCE(SUM(input_tokens+cache_read_tokens+cache_write_tokens),0),
  COALESCE(SUM(output_tokens),0),
  COALESCE(SUM(cost_usd),0),
  COALESCE(SUM(1-priced),0)
FROM usage_events WHERE ts >= ? GROUP BY `+col+` ORDER BY SUM(cost_usd) DESC`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpendRow
	for rows.Next() {
		var r SpendRow
		if err := rows.Scan(&r.Key, &r.InputTokens, &r.OutputTokens, &r.CostUSD, &r.Unpriced); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TotalSince returns total spend from `since`, optionally filtered by
// profile ("" = all) or session ("" = all).
func (s *Store) TotalSince(since time.Time, profile, session string) (float64, error) {
	q := `SELECT COALESCE(SUM(cost_usd),0) FROM usage_events WHERE ts >= ?`
	args := []any{since.Unix()}
	if profile != "" {
		q += ` AND profile = ?`
		args = append(args, profile)
	}
	if session != "" {
		q += ` AND session_id = ?`
		args = append(args, session)
	}
	var total float64
	err := s.db.QueryRow(q, args...).Scan(&total)
	return total, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
