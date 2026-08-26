// Package memory implements the durable memory layer: performance memory
// (empirical model/strategy outcomes) and project memory (persistent knowledge
// about a repository). Aggregation is honest — this package computes
// statistics from recorded telemetry; it does not pretend to "learn".
package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"omniharness/internal/session"
)

// PerfKey identifies a (model, strategy) combination.
type PerfKey struct {
	Model    string `json:"model"`
	Strategy string `json:"strategy"`
}

// PerfStat is the aggregated performance of one (model, strategy) pair.
type PerfStat struct {
	PerfKey
	Runs          int     `json:"runs"`
	Successes     int     `json:"successes"`
	SuccessRate   float64 `json:"successRate"`
	AvgLatencyMS  int64   `json:"avgLatencyMs"`
	AvgTokensIn   int64   `json:"avgTokensIn"`
	AvgTokensOut  int64   `json:"avgTokensOut"`
	AvgCostUSD    float64 `json:"avgCostUsd"`
	AvgToolCalls  float64 `json:"avgToolCalls"`
	AvgRepairs    float64 `json:"avgRepairs"`
	Outcomes      map[string]int `json:"outcomes"` // evaluation outcome counts
}

// Aggregate computes performance statistics over recorded tasks. Empty
// model/strategy groups are excluded.
func Aggregate(s *session.Store) ([]PerfStat, error) {
	rows, err := s.DB().Query(`
		SELECT t.strategy, mc.model,
		       COUNT(DISTINCT t.id),
		       COALESCE(SUM(CASE WHEN t.status = 'completed' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(mc.latency_ms), 0),
		       COALESCE(AVG(mc.tokens_in), 0),
		       COALESCE(AVG(mc.tokens_out), 0),
		       COALESCE(AVG(mc.cost_usd), 0),
		       COALESCE(AVG(t.repairs), 0),
		       COALESCE(AVG((SELECT COUNT(*) FROM tool_calls tc WHERE tc.task_id = t.id)), 0)
		FROM tasks t
		JOIN model_calls mc ON mc.task_id = t.id
		WHERE t.status IN ('completed', 'failed')
		GROUP BY t.strategy, mc.model
		ORDER BY 3 DESC`)
	if err != nil {
		return nil, fmt.Errorf("aggregate performance: %w", err)
	}
	defer rows.Close()

	var stats []PerfStat
	index := map[PerfKey]int{}
	for rows.Next() {
		var st PerfStat
		var avgRepairs, avgTools float64
		if err := rows.Scan(&st.Strategy, &st.Model, &st.Runs, &st.Successes,
			&st.AvgLatencyMS, &st.AvgTokensIn, &st.AvgTokensOut, &st.AvgCostUSD,
			&avgRepairs, &avgTools); err != nil {
			return nil, err
		}
		if st.Runs == 0 || st.Strategy == "" || st.Model == "" {
			continue
		}
		st.SuccessRate = float64(st.Successes) / float64(st.Runs)
		st.AvgRepairs = avgRepairs
		st.AvgToolCalls = avgTools
		st.Outcomes = map[string]int{}
		index[st.PerfKey] = len(stats)
		stats = append(stats, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Merge evaluation outcomes per group.
	erows, err := s.DB().Query(`
		SELECT t.strategy, mc.model, ev.outcome, COUNT(*)
		FROM evaluations ev
		JOIN tasks t ON t.id = ev.task_id
		JOIN model_calls mc ON mc.task_id = t.id
		GROUP BY t.strategy, mc.model, ev.outcome`)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var strategy, model, outcome string
		var n int
		if err := erows.Scan(&strategy, &model, &outcome, &n); err != nil {
			return nil, err
		}
		key := PerfKey{Model: model, Strategy: strategy}
		if i, ok := index[key]; ok {
			stats[i].Outcomes[outcome] = n
		}
	}
	return stats, erows.Err()
}

// Best returns the (model, strategy) pair with the best empirical success
// rate among pairs with at least minRuns observations. It is the seed of
// empirical model/strategy selection; callers decide how much weight to give
// it versus config.
func Best(stats []PerfStat, minRuns int) (PerfKey, bool) {
	var best PerfKey
	var bestRate float64
	found := false
	for _, st := range stats {
		if st.Runs < minRuns {
			continue
		}
		if st.SuccessRate > bestRate {
			best, bestRate, found = st.PerfKey, st.SuccessRate, true
		}
	}
	return best, found
}

// ProjectMemories groups project memory reads.
type ProjectMemories struct {
	store *session.Store
}

// Project returns a handle for project memory operations.
func Project(store *session.Store) *ProjectMemories {
	return &ProjectMemories{store: store}
}

// Remember stores a durable project memory entry.
func (p *ProjectMemories) Remember(projectKey, kind, content string) error {
	return p.store.PutMemory(projectKey, kind, content)
}

// Recall loads one entry.
func (p *ProjectMemories) Recall(projectKey, kind string) (string, bool, error) {
	m, err := p.store.GetMemory(projectKey, kind)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return m.Content, true, nil
}

// RecallAll lists everything remembered about a project.
func (p *ProjectMemories) RecallAll(projectKey string) ([]session.ProjectMemory, error) {
	return p.store.ListMemory(projectKey)
}

// TimeSince returns the zero time used for "no window" queries (kept for
// API symmetry; aggregation currently covers all history).
func TimeSince(_ time.Duration) time.Time { return time.Time{} }
