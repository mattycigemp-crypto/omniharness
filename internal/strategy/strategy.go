// Package strategy selects an execution strategy for a task profile. The
// selector is a pure function: given a TaskProfile, budgets and historical
// performance, it returns a Plan. Single-agent execution is a first-class
// strategy — spawning agents is the exception, not the default.
package strategy

import (
	"fmt"

	"omniharness/internal/budget"
	"omniharness/internal/task"
)

// Strategy identifies an execution strategy.
type Strategy string

const (
	// Direct: one agent, one pass, no orchestration.
	Direct Strategy = "direct"
	// Sequential: ordered steps executed one after another.
	Sequential Strategy = "sequential"
	// Parallel: independent sub-tasks executed concurrently, then joined.
	Parallel Strategy = "parallel"
	// ResearchSynthesis: gather research, then synthesize a deliverable.
	ResearchSynthesis Strategy = "research-synthesis"
	// PlanImplementVerify: plan, implement, then verify with an evaluator.
	PlanImplementVerify Strategy = "plan-implement-verify"
	// Debate: two agents produce competing answers, a reviewer picks.
	Debate Strategy = "debate"
	// RepairLoop: direct execution with an evaluation/repair feedback loop.
	RepairLoop Strategy = "repair-loop"
	// MultiAgent: general multi-agent orchestration with dependency graph.
	MultiAgent Strategy = "multi-agent"
	// Swarm: many parallel agents, only for wide independent exploration.
	Swarm Strategy = "swarm"
)

// Step is one unit of work in a plan.
type Step struct {
	ID       string   `json:"id"`
	Role     string   `json:"role,omitempty"`
	Depends  []string `json:"depends,omitempty"`
	Task     string   `json:"task,omitempty"`
	Parallel bool     `json:"parallel,omitempty"`
}

// Plan is the concrete execution plan produced by the strategy engine.
type Plan struct {
	Strategy Strategy `json:"strategy"`
	Reason   string   `json:"reason"`
	Steps    []Step   `json:"steps"`
}

// Input is everything the selector may consider.
type Input struct {
	Profile task.Profile
	Budget  budget.Budget
	// History is aggregated historical performance keyed by strategy name,
	// from performance memory (may be nil).
	History map[string]Performance
}

// Performance is the historical aggregate for one strategy.
type Performance struct {
	Runs        int     `json:"runs"`
	SuccessRate float64 `json:"successRate"`
	AvgRepairs  float64 `json:"avgRepairs"`
	AvgCostUSD  float64 `json:"avgCostUsd"`
}

// Selector picks a strategy.
type Selector struct{}

// Select returns the Plan for the given input. The profile drives the base
// choice; when performance memory holds conclusive evidence that the profile
// choice underperforms and another strategy clearly outperforms it, the
// selection switches with an explainable reason. Cold start (no/minimal
// history) never changes the profile decision.
func (s Selector) Select(in Input) (Plan, error) {
	plan, err := s.selectByProfile(in)
	if err != nil {
		return plan, err
	}
	if alt, reason, ok := RecommendStrategy(string(plan.Strategy), in.History, 3); ok {
		plan.Strategy = Strategy(alt)
		plan.Reason = reason
		plan.Steps = stepsFor(in.Profile, Strategy(alt))
	}
	return plan, nil
}

// selectByProfile is the deterministic profile-driven base selection.
func (Selector) selectByProfile(in Input) (Plan, error) {
	p := in.Profile

	if p.Verification == task.VerificationRequired {
		return plan(in, PlanImplementVerify, "verification is REQUIRED; plan-implement-verify keeps the verify step explicit")
	}

	// High ambiguity means the agent's own reading of the request could be
	// wrong, and complexity says nothing about that risk — "clean up the
	// code" is two words and, before this rule, went straight to Direct with
	// no one checking what "clean up" was taken to mean. This has to run
	// ahead of the low-complexity shortcut below, or the short, vague
	// requests that need it most never reach it. A stated plan turns the
	// agent's interpretation into something a human or an evaluator can
	// catch before the work happens, rather than discovering the
	// misunderstanding after it is already in the diff.
	if p.Ambiguity == task.LevelHigh {
		return plan(in, PlanImplementVerify, "ambiguity is HIGH; a stated plan surfaces the agent's interpretation before it acts on it")
	}

	// Research without a software deliverable.
	if p.Domain == task.DomainResearch && p.Complexity != task.ComplexityLow {
		return plan(in, ResearchSynthesis, "research domain with meaningful depth; gather evidence then synthesize")
	}

	// Low complexity: one agent, direct. No ceremony.
	if p.Complexity == task.ComplexityLow {
		return plan(in, Direct, "low complexity; a single agent is the optimal strategy")
	}

	// Cheap tasks stay direct too.
	if in.Budget.MaxCostUSD > 0 && p.EstimatedCostUSD > in.Budget.MaxCostUSD*0.5 {
		// Task is expensive relative to budget: escalate carefully.
		if p.Complexity == task.ComplexityMedium {
			return plan(in, Direct, "cost-sensitive; single agent, no overhead")
		}
	}

	// High risk and high ambiguity benefit from a second opinion.
	if p.Risk == task.LevelHigh && p.Complexity == task.ComplexityHigh {
		return plan(in, Debate, "high risk + high complexity; independent review reduces catastrophic failure")
	}

	// Parallelizable medium/high complexity: parallel when safe.
	if p.Parallelizable && p.Complexity != task.ComplexityLow {
		steps := parallelSteps(p)
		if len(steps) >= 2 {
			if in.Budget.MaxAgents > 0 && in.Budget.MaxAgents < 2 {
				return plan(in, Sequential, "profile is parallelizable but budget allows only one agent")
			}
			return plan(in, Parallel, fmt.Sprintf("profile shows %d independent sub-tasks; execute concurrently", len(steps)))
		}
	}

	// Medium complexity default: sequential steps with a repair loop.
	if p.Complexity == task.ComplexityMedium {
		return plan(in, Sequential, "medium complexity; ordered steps with verification after each")
	}

	// High complexity with tools and verification: multi-agent.
	if p.Complexity == task.ComplexityHigh {
		if len(p.Tools) >= 3 && p.Context == task.LevelLarge {
			if in.Budget.MaxAgents > 0 && in.Budget.MaxAgents < 3 {
				return plan(in, Sequential, "multi-agent would need 3+ agents but budget allows fewer")
			}
			return plan(in, MultiAgent, "high complexity + large context + broad tooling; specialized agents reduce context thrash")
		}
		if p.Verification == task.VerificationRecommended {
			return plan(in, PlanImplementVerify, "high complexity software task; plan, implement, verify")
		}
		return plan(in, Sequential, "high complexity; sequential with repair loop")
	}

	return plan(in, Direct, "no strong signal; fall back to a single agent")
}

// ChooseSwarm reports whether a swarm is justified. Swarms are expensive and
// reserved for wide, independent exploration with an adequate budget.
func ChooseSwarm(in Input) (bool, string) {
	p := in.Profile
	if p.Complexity != task.ComplexityHigh || !p.Parallelizable {
		return false, "swarm needs high complexity and parallelizable sub-tasks"
	}
	if p.Context == task.LevelSmall {
		return false, "swarm unjustified for small context"
	}
	if in.Budget.MaxAgents > 0 && in.Budget.MaxAgents < 4 {
		return false, "budget forbids the minimum swarm size"
	}
	if in.Budget.MaxCostUSD > 0 && in.Budget.MaxCostUSD < p.EstimatedCostUSD*3 {
		return false, "budget cannot cover swarm overhead"
	}
	return true, "wide independent exploration with adequate budget"
}

// RecommendStrategy is the performance-memory override rule, shared by the
// selector and the memory advisor. It prefers an empirically stronger
// strategy over the profile choice only when the data is conclusive: the
// profile choice must have at least minRuns recorded runs with success below
// 60%, and an alternative must beat it by at least 15 percentage points with
// at least minRuns runs. Returns the alternative and an explainable reason;
// ok=false keeps the profile choice (deterministic cold start).
func RecommendStrategy(profileChoice string, history map[string]Performance, minRuns int) (string, string, bool) {
	if minRuns <= 0 {
		minRuns = 3
	}
	cur, ok := history[profileChoice]
	if !ok || cur.Runs < minRuns || cur.SuccessRate >= 0.6 {
		return "", "", false
	}
	var best string
	var bestRate float64
	for name, p := range history {
		if name == profileChoice || name == string(Swarm) || p.Runs < minRuns {
			continue
		}
		if p.SuccessRate > bestRate {
			best, bestRate = name, p.SuccessRate
		}
	}
	if best == "" || bestRate < cur.SuccessRate+0.15 {
		return "", "", false
	}
	reason := fmt.Sprintf("performance memory: %q has only %.0f%% success over %d runs, while %q achieved %.0f%% over %d runs",
		profileChoice, cur.SuccessRate*100, cur.Runs, best, bestRate*100, history[best].Runs)
	return best, reason, true
}

// Names returns all supported strategies (for docs and CLI).
func Names() []Strategy {
	return []Strategy{Direct, Sequential, Parallel, ResearchSynthesis, PlanImplementVerify, Debate, RepairLoop, MultiAgent, Swarm}
}

func plan(in Input, s Strategy, reason string) (Plan, error) {
	steps := stepsFor(in.Profile, s)
	return Plan{Strategy: s, Reason: reason, Steps: steps}, nil
}

// StepsFor builds the step list for an arbitrary strategy. Exported so the
// repair engine can restructure execution (strategy-level repair) without
// reaching into plan internals.
func StepsFor(p task.Profile, s Strategy) []Step { return stepsFor(p, s) }

// stepsFor builds the step list for a plan. Steps are advisory: the
// orchestrator schedules them.
func stepsFor(p task.Profile, s Strategy) []Step {
	switch s {
	case Direct:
		return []Step{{ID: "work", Task: "execute the task"}}
	case Sequential:
		return []Step{
			{ID: "s1", Task: "step 1"},
			{ID: "s2", Depends: []string{"s1"}, Task: "step 2"},
			{ID: "s3", Depends: []string{"s2"}, Task: "step 3"},
		}
	case Parallel:
		return parallelSteps(p)
	case ResearchSynthesis:
		return []Step{
			{ID: "r1", Role: "researcher", Task: "gather evidence", Parallel: true},
			{ID: "r2", Role: "researcher", Task: "gather evidence", Parallel: true},
			{ID: "synth", Role: "synthesizer", Depends: []string{"r1", "r2"}, Task: "synthesize findings"},
		}
	case PlanImplementVerify:
		return []Step{
			{ID: "plan", Role: "architect", Task: "produce an implementation plan"},
			{ID: "impl", Role: "implementer", Depends: []string{"plan"}, Task: "implement"},
			{ID: "verify", Role: "reviewer", Depends: []string{"impl"}, Task: "verify with evaluators"},
		}
	case Debate:
		return []Step{
			{ID: "d1", Role: "implementer", Task: "produce answer", Parallel: true},
			{ID: "d2", Role: "implementer", Task: "produce answer", Parallel: true},
			{ID: "judge", Role: "reviewer", Depends: []string{"d1", "d2"}, Task: "select best answer"},
		}
	case RepairLoop:
		return []Step{{ID: "work", Task: "execute with evaluation/repair loop"}}
	case MultiAgent:
		return []Step{
			{ID: "m1", Role: "architect", Task: "decompose and plan"},
			{ID: "m2", Role: "implementer", Depends: []string{"m1"}, Task: "implement", Parallel: true},
			{ID: "m3", Role: "implementer", Depends: []string{"m1"}, Task: "implement", Parallel: true},
			{ID: "m4", Role: "reviewer", Depends: []string{"m2", "m3"}, Task: "review and integrate"},
		}
	case Swarm:
		var steps []Step
		for i := 0; i < 6; i++ {
			steps = append(steps, Step{ID: fmt.Sprintf("w%d", i), Task: "explore in parallel", Parallel: true})
		}
		steps = append(steps, Step{ID: "join", Depends: stepIDs(steps), Task: "synthesize"})
		return steps
	}
	return []Step{{ID: "work", Task: "execute the task"}}
}

func parallelSteps(p task.Profile) []Step {
	n := 2
	if p.Context == task.LevelLarge {
		n = 3
	}
	var steps []Step
	for i := 0; i < n; i++ {
		steps = append(steps, Step{ID: fmt.Sprintf("p%d", i), Task: "sub-task", Parallel: true})
	}
	steps = append(steps, Step{ID: "join", Depends: stepIDs(steps), Task: "integrate results"})
	return steps
}

func stepIDs(steps []Step) []string {
	ids := make([]string, 0, len(steps))
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	return ids
}
