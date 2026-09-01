package memory

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"omniharness/internal/session"
	"omniharness/internal/strategy"
)

// Advisor turns recorded performance memory into explainable recommendations.
// Nothing here is fabricated: every number comes from the model_calls, tasks,
// tool_calls and evaluations tables. Cold start is deterministic — below
// MinRuns for every candidate, the advisor declines to influence a decision
// and callers keep their config-driven default.
type Advisor struct {
	Store *session.Store
	// MinRuns is the minimum number of recorded tasks for a candidate before
	// memory may influence a decision. 0 uses a conservative default of 3.
	MinRuns int
}

func (a *Advisor) minRuns() int {
	if a.MinRuns > 0 {
		return a.MinRuns
	}
	return 3
}

// Candidate is one scored option in a recommendation.
type Candidate struct {
	Model       string  `json:"model"`
	Runs        int     `json:"runs"`
	SuccessRate float64 `json:"successRate"`
	AvgRepairs  float64 `json:"avgRepairs"`
	AvgLatency  int64   `json:"avgLatencyMs"`
	AvgCostUSD  float64 `json:"avgCostUsd"`

	score float64 // internal ranking, not serialized
}

// ModelAdvice is the outcome of a model recommendation.
type ModelAdvice struct {
	Model      string      `json:"model"`
	Reason     string      `json:"reason"`
	Candidates []Candidate `json:"candidates"`
}

// recommendScore ranks a candidate. It uses the Wilson lower confidence
// bound (z=1, ~84% one-sided) so a candidate with 1/1 runs does not beat one
// with 40/50, then subtracts a repair penalty: repairs mean the execution
// needed correction loops, which cost tokens, latency and reliability.
func recommendScore(runs, successes int, repairs float64) float64 {
	if runs <= 0 {
		return 0
	}
	p := float64(successes) / float64(runs)
	z := 1.0
	n := float64(runs)
	denom := n + z*z
	center := (p + z*z/(2*n)) / denom
	margin := z * math.Sqrt((p*(1-p)/n+z*z/(4*n*n))/denom)
	return center - margin - 0.05*repairs
}

// RecommendModel evaluates candidate models by recorded success and returns
// the best with an explainable reason. It reports ok=false (and the reason
// states so) when no candidate has enough recorded runs — the caller then
// keeps its config default. Candidates with zero runs are listed but never
// chosen over one with data.
func (a *Advisor) RecommendModel(candidates []string) (ModelAdvice, bool) {
	advice := ModelAdvice{Candidates: []Candidate{}}
	minRuns := a.minRuns()
	var scored []Candidate
	for _, m := range candidates {
		runs, successes, repairs, lat, cost, err := a.modelStats(m)
		if err != nil || runs == 0 {
			continue
		}
		c := Candidate{Model: m, Runs: runs, SuccessRate: float64(successes) / float64(runs),
			AvgRepairs: repairs, AvgLatency: lat, AvgCostUSD: cost}
		advice.Candidates = append(advice.Candidates, c)
		if runs >= minRuns {
			c.score = recommendScore(runs, successes, repairs)
			scored = append(scored, c)
		}
	}
	if len(scored) == 0 {
		advice.Reason = "insufficient performance data (need " + strconv.Itoa(minRuns) + "+ recorded runs per candidate); using configured model"
		return advice, false
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	best := scored[0]
	advice.Model = best.Model
	advice.Reason = a.modelReason(advice.Candidates, best)
	return advice, true
}

func (a *Advisor) modelReason(cands []Candidate, best Candidate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "performance memory: %s leads candidates (%.0f%% success over %d runs, %.0f avg repairs, %.1fs avg, $%.4f avg)",
		best.Model, best.SuccessRate*100, best.Runs, best.AvgRepairs, float64(best.AvgLatency)/1000, best.AvgCostUSD)
	for _, c := range cands {
		if c.Model == best.Model {
			continue
		}
		fmt.Fprintf(&b, "; %s: %d runs, %.0f%% success", c.Model, c.Runs, c.SuccessRate*100)
	}
	return b.String()
}

// modelStats aggregates one model's outcomes across recorded tasks.
func (a *Advisor) modelStats(model string) (runs, successes int, repairs float64, latencyMS int64, costUSD float64, err error) {
	row := a.Store.DB().QueryRow(`
		SELECT COUNT(DISTINCT mc.task_id),
		       COALESCE(SUM(CASE WHEN t.status = 'completed' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(t.repairs), 0),
		       COALESCE(AVG(mc.latency_ms), 0),
		       COALESCE(AVG(mc.cost_usd), 0)
		FROM model_calls mc
		JOIN tasks t ON t.id = mc.task_id
		WHERE mc.model = ? AND t.status IN ('completed','failed')`, model)
	if err := row.Scan(&runs, &successes, &repairs, &latencyMS, &costUSD); err != nil {
		return 0, 0, 0, 0, 0, err
	}
	return runs, successes, repairs, latencyMS, costUSD, nil
}

// StrategyPerformance aggregates recorded outcomes per strategy, keyed for
// strategy.Input.History.
func (a *Advisor) StrategyPerformance() (map[string]strategy.Performance, error) {
	rows, err := a.Store.DB().Query(`
		SELECT t.strategy,
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN t.status = 'completed' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(t.repairs), 0),
		       COALESCE(AVG(mc.cost_usd), 0)
		FROM tasks t
		JOIN model_calls mc ON mc.task_id = t.id
		WHERE t.strategy != '' AND t.status IN ('completed','failed')
		GROUP BY t.strategy`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]strategy.Performance{}
	for rows.Next() {
		var name string
		var runs, successes int
		var repairs, cost float64
		if err := rows.Scan(&name, &runs, &successes, &repairs, &cost); err != nil {
			return nil, err
		}
		out[name] = strategy.Performance{
			Runs:        runs,
			SuccessRate: float64(successes) / float64(runs),
			AvgRepairs:  repairs,
			AvgCostUSD:  cost,
		}
	}
	return out, rows.Err()
}

// RecommendStrategy delegates to the shared rule in the strategy package so
// the selector and the advisor can never disagree.
func (a *Advisor) RecommendStrategy(profileChoice string, history map[string]strategy.Performance) (string, string, bool) {
	return strategy.RecommendStrategy(profileChoice, history, a.minRuns())
}
