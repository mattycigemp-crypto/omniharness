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
//
// A task that calls the same model several times (multiple agent turns, a
// multi-step strategy, a repair cycle) joins model_calls to tasks once per
// call, not once per task. runs/successes/repairs are task-level facts —
// counting or summing them straight over that join let a single task with
// several calls outvote every single-call task, and let SuccessRate exceed
// 100% outright (a 3-call task, once "completed", added 3 to successes
// against a runs count that itself only used DISTINCT task_id). They are
// computed here from a derived table of distinct tasks instead. Latency and
// cost are genuinely call-level questions — "what does one more call to
// this model typically cost" — so those two stay averaged over every call,
// deliberately not deduplicated by task.
func (a *Advisor) modelStats(model string) (runs, successes int, repairs float64, latencyMS int64, costUSD float64, err error) {
	taskRow := a.Store.DB().QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(repairs), 0)
		FROM (
			SELECT DISTINCT t.id, t.status, t.repairs
			FROM tasks t
			JOIN model_calls mc ON mc.task_id = t.id
			WHERE mc.model = ? AND t.status IN ('completed', 'failed')
		)`, model)
	if err := taskRow.Scan(&runs, &successes, &repairs); err != nil {
		return 0, 0, 0, 0, 0, err
	}

	callRow := a.Store.DB().QueryRow(`
		SELECT COALESCE(AVG(mc.latency_ms), 0), COALESCE(AVG(mc.cost_usd), 0)
		FROM model_calls mc
		JOIN tasks t ON t.id = mc.task_id
		WHERE mc.model = ? AND t.status IN ('completed', 'failed')`, model)
	if err := callRow.Scan(&latencyMS, &costUSD); err != nil {
		return 0, 0, 0, 0, 0, err
	}
	return runs, successes, repairs, latencyMS, costUSD, nil
}

// StrategyPerformance aggregates recorded outcomes per strategy, keyed for
// strategy.Input.History. Costed per task (a task's calls summed, then
// averaged across the strategy's tasks) via a CTE that groups by task id
// first — the same double-counting risk modelStats guards against: a
// strategy where tasks average several model calls each must not have its
// runs/successes/repairs inflated by call count, and AvgCostUSD here means
// "typical total cost of a task run under this strategy," which is the
// number worth comparing between strategies — not "cost of one call,"
// which barely varies with strategy at all.
func (a *Advisor) StrategyPerformance() (map[string]strategy.Performance, error) {
	rows, err := a.Store.DB().Query(`
		WITH task_costs AS (
			SELECT t.id AS task_id, t.strategy, t.status, t.repairs,
			       COALESCE(SUM(mc.cost_usd), 0) AS cost
			FROM tasks t
			LEFT JOIN model_calls mc ON mc.task_id = t.id
			WHERE t.strategy != '' AND t.status IN ('completed', 'failed')
			GROUP BY t.id
		)
		SELECT strategy,
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(repairs), 0),
		       COALESCE(AVG(cost), 0)
		FROM task_costs
		GROUP BY strategy`)
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
