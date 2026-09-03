// Package telemetry computes runtime metrics from recorded data. Every metric
// here is derived from real session rows — nothing is fabricated.
package telemetry

import (
	"time"

	"omniharness/internal/session"
)

// SessionMetrics summarizes one session.
type SessionMetrics struct {
	SessionID   string    `json:"sessionId"`
	ModelCalls  int       `json:"modelCalls"`
	ToolCalls   int       `json:"toolCalls"`
	TokensIn    int64     `json:"tokensIn"`
	TokensOut   int64     `json:"tokensOut"`
	CostUSD     float64   `json:"costUsd"`
	LatencyMS   int64     `json:"latencyMs"`
	FailedCalls int       `json:"failedCalls"`
	Evaluations int       `json:"evaluations"`
	StartedAt   time.Time `json:"startedAt"`
}

// ForSession computes metrics for one session.
func ForSession(store *session.Store, sessionID string) (SessionMetrics, error) {
	var m SessionMetrics
	m.SessionID = sessionID
	err := store.DB().QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(tokens_in), 0),
			COALESCE(SUM(tokens_out), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(latency_ms), 0),
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0)
		FROM model_calls WHERE session_id = ?`, sessionID).
		Scan(&m.ModelCalls, &m.TokensIn, &m.TokensOut, &m.CostUSD, &m.LatencyMS, &m.FailedCalls)
	if err != nil {
		return m, err
	}
	err = store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE session_id = ?`, sessionID).Scan(&m.ToolCalls)
	if err != nil {
		return m, err
	}
	err = store.DB().QueryRow(`SELECT COUNT(*) FROM evaluations WHERE session_id = ?`, sessionID).Scan(&m.Evaluations)
	if err != nil {
		return m, err
	}
	var started string
	err = store.DB().QueryRow(`SELECT created_at FROM sessions WHERE id = ?`, sessionID).Scan(&started)
	if err == nil {
		m.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	}
	return m, nil
}

// GlobalMetrics is a whole-store summary.
type GlobalMetrics struct {
	Sessions     int     `json:"sessions"`
	Tasks        int     `json:"tasks"`
	Completed    int     `json:"completed"`
	Failed       int     `json:"failed"`
	ModelCalls   int     `json:"modelCalls"`
	ToolCalls    int     `json:"toolCalls"`
	Tokens       int64   `json:"tokens"`
	CostUSD      float64 `json:"costUsd"`
	TotalLatency int64   `json:"totalLatencyMs"`
	Repairs      int64   `json:"repairs"`
}

// Global computes whole-store aggregates.
func Global(store *session.Store) (GlobalMetrics, error) {
	var g GlobalMetrics
	db := store.DB()
	scan := func(q string, dest ...any) error { return db.QueryRow(q).Scan(dest...) }
	if err := scan(`SELECT COUNT(*) FROM sessions`, &g.Sessions); err != nil {
		return g, err
	}
	if err := scan(`SELECT COUNT(*) FROM tasks`, &g.Tasks); err != nil {
		return g, err
	}
	if err := scan(`SELECT COALESCE(SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END),0) FROM tasks`, &g.Completed); err != nil {
		return g, err
	}
	if err := scan(`SELECT COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0) FROM tasks`, &g.Failed); err != nil {
		return g, err
	}
	if err := scan(`SELECT COUNT(*) FROM model_calls`, &g.ModelCalls); err != nil {
		return g, err
	}
	if err := scan(`SELECT COUNT(*) FROM tool_calls`, &g.ToolCalls); err != nil {
		return g, err
	}
	if err := scan(`SELECT COALESCE(SUM(tokens_in+tokens_out),0) FROM model_calls`, &g.Tokens); err != nil {
		return g, err
	}
	if err := scan(`SELECT COALESCE(SUM(cost_usd),0) FROM model_calls`, &g.CostUSD); err != nil {
		return g, err
	}
	if err := scan(`SELECT COALESCE(SUM(latency_ms),0) FROM model_calls`, &g.TotalLatency); err != nil {
		return g, err
	}
	if err := scan(`SELECT COALESCE(SUM(repairs),0) FROM tasks`, &g.Repairs); err != nil {
		return g, err
	}
	return g, nil
}

// ModelStats breaks down usage by model.
type ModelStats struct {
	Model     string  `json:"model"`
	Calls     int     `json:"calls"`
	Failed    int     `json:"failed"`
	TokensIn  int64   `json:"tokensIn"`
	TokensOut int64   `json:"tokensOut"`
	CostUSD   float64 `json:"costUsd"`
	AvgMS     int64   `json:"avgMs"`
}

// ByModel aggregates model usage across all sessions.
func ByModel(store *session.Store) ([]ModelStats, error) {
	rows, err := store.DB().Query(`
		SELECT model, COUNT(*), COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0),
		       COALESCE(SUM(cost_usd),0),
		       -- AVG yields a float, and scanning that into AvgMS fails outright
		       -- unless it happens to be whole. Truncate in SQL instead.
		       CAST(COALESCE(AVG(latency_ms),0) AS INTEGER)
		FROM model_calls GROUP BY model ORDER BY 6 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelStats
	for rows.Next() {
		var ms ModelStats
		if err := rows.Scan(&ms.Model, &ms.Calls, &ms.Failed, &ms.TokensIn, &ms.TokensOut, &ms.CostUSD, &ms.AvgMS); err != nil {
			return nil, err
		}
		out = append(out, ms)
	}
	return out, rows.Err()
}

// ToolStats breaks down tool usage.
type ToolStats struct {
	Tool   string `json:"tool"`
	Calls  int    `json:"calls"`
	Failed int    `json:"failed"`
	Denied int    `json:"denied"`
}

// ByTool aggregates tool usage.
func ByTool(store *session.Store) ([]ToolStats, error) {
	rows, err := store.DB().Query(`
		SELECT tool, COUNT(*),
		       COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN status='denied' THEN 1 ELSE 0 END),0)
		FROM tool_calls GROUP BY tool ORDER BY 2 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolStats
	for rows.Next() {
		var ts ToolStats
		if err := rows.Scan(&ts.Tool, &ts.Calls, &ts.Failed, &ts.Denied); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// StrategyStats breaks down usage by execution strategy.
type StrategyStats struct {
	Strategy  string  `json:"strategy"`
	Runs      int     `json:"runs"`
	Completed int     `json:"completed"`
	Failed    int     `json:"failed"`
	Repairs   int     `json:"repairs"`
	CostUSD   float64 `json:"costUsd"`
}

// ByStrategy aggregates outcomes by the execution strategy each task ran
// under — the same signal memory.Advisor uses internally to decide whether
// to override a strategy choice, surfaced here so that choice is auditable
// rather than only implicit.
//
// Costed and repair-counted from a derived table that groups by task id
// first (a CTE), deliberately, rather than joining tasks straight to
// model_calls and grouping by strategy: a task that calls a model several
// times (multiple agent turns, a multi-step strategy, a repair cycle) would
// otherwise be counted once per call instead of once per task, letting a
// strategy whose tasks average several calls each look artificially busier
// or more failure-prone than one whose tasks average few.
func ByStrategy(store *session.Store) ([]StrategyStats, error) {
	rows, err := store.DB().Query(`
		WITH task_costs AS (
			SELECT t.id AS task_id, t.strategy, t.status, t.repairs,
			       COALESCE(SUM(mc.cost_usd), 0) AS cost
			FROM tasks t
			LEFT JOIN model_calls mc ON mc.task_id = t.id
			WHERE t.strategy != '' AND t.status IN ('completed', 'failed')
			GROUP BY t.id
		)
		SELECT strategy, COUNT(*),
		       COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(repairs), 0),
		       COALESCE(SUM(cost), 0)
		FROM task_costs
		GROUP BY strategy ORDER BY 2 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrategyStats
	for rows.Next() {
		var ss StrategyStats
		if err := rows.Scan(&ss.Strategy, &ss.Runs, &ss.Completed, &ss.Failed, &ss.Repairs, &ss.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}
