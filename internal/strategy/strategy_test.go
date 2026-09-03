package strategy

import (
	"strings"
	"testing"

	"omniharness/internal/budget"
	"omniharness/internal/task"
)

func selectFor(p task.Profile) Plan {
	s := Selector{}
	pl, err := s.Select(Input{Profile: p, Budget: budget.Budget{}})
	if err != nil {
		panic(err)
	}
	return pl
}

func TestPerformanceMemoryOverridesWeakStrategy(t *testing.T) {
	in := Input{
		Profile: task.Profile{Complexity: task.ComplexityMedium, Domain: task.DomainSoftware},
		History: map[string]Performance{
			"sequential": {Runs: 10, SuccessRate: 0.3},
			"direct":     {Runs: 8, SuccessRate: 0.8},
		},
	}
	p, err := (Selector{}).Select(in)
	if err != nil {
		t.Fatal(err)
	}
	// Medium complexity defaults to sequential, but memory shows it underperforms.
	if p.Strategy != Direct {
		t.Fatalf("strategy = %s, want direct via performance memory", p.Strategy)
	}
	if !strings.Contains(p.Reason, "performance memory") {
		t.Fatalf("reason must explain the memory override, got: %s", p.Reason)
	}
}

func TestPerformanceMemoryColdStartKeepsProfileChoice(t *testing.T) {
	in := Input{
		Profile: task.Profile{Complexity: task.ComplexityMedium, Domain: task.DomainSoftware},
		History: map[string]Performance{
			"sequential": {Runs: 2, SuccessRate: 0.0}, // below min-runs threshold
			"direct":     {Runs: 1, SuccessRate: 1.0},
		},
	}
	p, err := (Selector{}).Select(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Strategy != Sequential {
		t.Fatalf("cold start must keep the profile choice, got %s", p.Strategy)
	}
}

func TestPerformanceMemoryIgnoresStrongChoice(t *testing.T) {
	in := Input{
		Profile: task.Profile{Complexity: task.ComplexityMedium, Domain: task.DomainSoftware},
		History: map[string]Performance{
			"sequential": {Runs: 10, SuccessRate: 0.9},
			"direct":     {Runs: 8, SuccessRate: 0.95},
		},
	}
	p, err := (Selector{}).Select(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Strategy != Sequential {
		t.Fatalf("a strong profile choice must not be overridden, got %s", p.Strategy)
	}
}

func TestTrivialTaskUsesDirect(t *testing.T) {
	p := task.Profile{Complexity: task.ComplexityLow, Domain: task.DomainSoftware, Risk: task.LevelLow}
	pl := selectFor(p)
	if pl.Strategy != Direct {
		t.Fatalf("strategy = %s, want direct", pl.Strategy)
	}
}

func TestVerificationRequiredForcesPlanImplementVerify(t *testing.T) {
	p := task.Profile{
		Complexity:   task.ComplexityHigh,
		Domain:       task.DomainSoftware,
		Risk:         task.LevelLow,
		Verification: task.VerificationRequired,
	}
	pl := selectFor(p)
	if pl.Strategy != PlanImplementVerify {
		t.Fatalf("strategy = %s, want plan-implement-verify", pl.Strategy)
	}
}

func TestResearchUsesResearchSynthesis(t *testing.T) {
	p := task.Profile{Complexity: task.ComplexityMedium, Domain: task.DomainResearch, Risk: task.LevelLow}
	pl := selectFor(p)
	if pl.Strategy != ResearchSynthesis {
		t.Fatalf("strategy = %s", pl.Strategy)
	}
}

func TestHighRiskHighComplexityDebates(t *testing.T) {
	p := task.Profile{
		Complexity: task.ComplexityHigh,
		Domain:     task.DomainSoftware,
		Risk:       task.LevelHigh,
	}
	pl := selectFor(p)
	if pl.Strategy != Debate {
		t.Fatalf("strategy = %s, want debate", pl.Strategy)
	}
}

func TestParallelizableUsesParallel(t *testing.T) {
	p := task.Profile{
		Complexity:     task.ComplexityMedium,
		Domain:         task.DomainSoftware,
		Risk:           task.LevelLow,
		Parallelizable: true,
	}
	pl := selectFor(p)
	if pl.Strategy != Parallel {
		t.Fatalf("strategy = %s, want parallel", pl.Strategy)
	}
	if len(pl.Steps) == 0 {
		t.Fatal("parallel plan has no steps")
	}
	// Last step must depend on all parallel steps.
	join := pl.Steps[len(pl.Steps)-1]
	if len(join.Depends) != len(pl.Steps)-1 {
		t.Fatalf("join deps = %v", join.Depends)
	}
}

func TestMultiAgentOnlyWhenJustified(t *testing.T) {
	// Large context + broad tooling + high complexity.
	p := task.Profile{
		Complexity: task.ComplexityHigh,
		Domain:     task.DomainSoftware,
		Risk:       task.LevelLow,
		Context:    task.LevelLarge,
		Tools:      []string{"filesystem", "shell", "git", "search"},
	}
	pl := selectFor(p)
	if pl.Strategy != MultiAgent {
		t.Fatalf("strategy = %s, want multi-agent", pl.Strategy)
	}

	// Same complexity but small context: must NOT force multi-agent.
	p.Context = task.LevelSmall
	p.Tools = []string{"filesystem"}
	pl = selectFor(p)
	if pl.Strategy == MultiAgent {
		t.Fatal("multi-agent must not be forced for small context")
	}
}

func TestSingleAgentIsDefault(t *testing.T) {
	// A medium complexity, sequential task must not spawn many agents.
	p := task.Profile{
		Complexity: task.ComplexityMedium,
		Domain:     task.DomainGeneral,
		Risk:       task.LevelLow,
	}
	pl := selectFor(p)
	if pl.Strategy != Sequential {
		t.Fatalf("strategy = %s, want sequential", pl.Strategy)
	}
}

func TestSwarmOnlyWhenJustified(t *testing.T) {
	in := Input{
		Profile: task.Profile{
			Complexity:       task.ComplexityHigh,
			Parallelizable:   true,
			Context:          task.LevelLarge,
			EstimatedCostUSD: 1.0,
		},
		Budget: budget.Budget{MaxCostUSD: 10},
	}
	ok, _ := ChooseSwarm(in)
	if !ok {
		t.Fatal("swarm should be justified here")
	}
	in.Budget.MaxCostUSD = 0.5
	ok, reason := ChooseSwarm(in)
	if ok {
		t.Fatal("swarm must not be justified with tiny budget")
	}
	if reason == "" {
		t.Fatal("expected a reason")
	}
}

func TestBudgetMaxAgentsDowngradesParallel(t *testing.T) {
	s := Selector{}
	p := task.Profile{
		Complexity:     task.ComplexityMedium,
		Domain:         task.DomainSoftware,
		Risk:           task.LevelLow,
		Parallelizable: true,
	}
	pl, err := s.Select(Input{Profile: p, Budget: budget.Budget{MaxAgents: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Strategy != Sequential {
		t.Fatalf("strategy = %s, want sequential downgrade", pl.Strategy)
	}
}

func TestBudgetMaxAgentsDowngradesMultiAgent(t *testing.T) {
	s := Selector{}
	p := task.Profile{
		Complexity: task.ComplexityHigh,
		Domain:     task.DomainSoftware,
		Risk:       task.LevelLow,
		Context:    task.LevelLarge,
		Tools:      []string{"filesystem", "shell", "git", "search"},
	}
	pl, err := s.Select(Input{Profile: p, Budget: budget.Budget{MaxAgents: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Strategy == MultiAgent {
		t.Fatal("multi-agent must be downgraded when budget caps agents at 2")
	}
}

// The case detectAmbiguity's own vague-phrase list names directly: a short
// request is not the same thing as a well-specified one. Before this rule,
// "clean up the code" — two words, low complexity by the word-count
// heuristic — went straight to Direct with nothing checking what "clean up"
// was taken to mean. This has to fire ahead of the low-complexity shortcut,
// or the requests that need it most never reach it.
func TestHighAmbiguityGetsAPlanEvenAtLowComplexity(t *testing.T) {
	p := task.Profile{Complexity: task.ComplexityLow, Domain: task.DomainSoftware, Ambiguity: task.LevelHigh}
	pl := selectFor(p)
	if pl.Strategy != PlanImplementVerify {
		t.Fatalf("strategy = %s, want plan-implement-verify for a short but ambiguous request", pl.Strategy)
	}
	if !strings.Contains(pl.Reason, "ambiguity") {
		t.Fatalf("reason must explain the ambiguity trigger, got: %s", pl.Reason)
	}
}

func TestHighAmbiguityAtHighComplexityAlsoGetsAPlan(t *testing.T) {
	p := task.Profile{Complexity: task.ComplexityHigh, Domain: task.DomainSoftware, Ambiguity: task.LevelHigh}
	pl := selectFor(p)
	if pl.Strategy != PlanImplementVerify {
		t.Fatalf("strategy = %s, want plan-implement-verify", pl.Strategy)
	}
}

// Explicit REQUIRED verification is a stronger, separate signal (the request
// itself demands checking) and must still win outright — this rule must not
// swallow that case just because both land on the same strategy.
func TestVerificationRequiredStillWinsOverAmbiguity(t *testing.T) {
	p := task.Profile{
		Complexity: task.ComplexityHigh, Domain: task.DomainSoftware,
		Ambiguity: task.LevelHigh, Verification: task.VerificationRequired,
	}
	pl := selectFor(p)
	if pl.Strategy != PlanImplementVerify {
		t.Fatalf("strategy = %s, want plan-implement-verify", pl.Strategy)
	}
	if !strings.Contains(pl.Reason, "REQUIRED") {
		t.Fatalf("the REQUIRED-verification reason must still be the one that fires: %s", pl.Reason)
	}
}

// Low or medium ambiguity must not be swept into the same treatment as high —
// only a genuinely unclear request pays the extra planning step.
func TestModerateAmbiguityDoesNotForceAPlan(t *testing.T) {
	p := task.Profile{Complexity: task.ComplexityLow, Domain: task.DomainSoftware, Ambiguity: task.LevelMedium}
	pl := selectFor(p)
	if pl.Strategy != Direct {
		t.Fatalf("strategy = %s, want direct — only HIGH ambiguity should escalate", pl.Strategy)
	}
}
