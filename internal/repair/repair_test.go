package repair

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"omniharness/internal/gateway"
)

func TestClassifyGatewayErrors(t *testing.T) {
	f := Classify(StageModel, &gateway.Error{Kind: gateway.KindRateLimit, Status: 429, Message: "slow down"})
	if f.Kind != "rate_limit" || f.Stage != StageModel {
		t.Fatalf("%+v", f)
	}
	f = Classify(StageModel, &gateway.Error{Kind: gateway.KindAuth, Status: 401, Message: "nope"})
	if f.Kind != "auth" {
		t.Fatalf("%+v", f)
	}
}

func TestClassifyWrappedErrors(t *testing.T) {
	inner := &gateway.Error{Kind: gateway.KindServer, Status: 502, Message: "boom"}
	wrapped := errors.New("prefix: " + inner.Error()) // plain string, not %w
	if f := Classify(StageModel, wrapped); f.Kind == "server" {
		t.Fatalf("plain string errors must not unwrap: %+v", f)
	}
	wrapped2 := fmt.Errorf("prefix: %w", inner)
	if f := Classify(StageModel, wrapped2); f.Kind != "server" {
		t.Fatalf("wrapped gateway error not classified: %+v", f)
	}
}

func TestClassifyTextErrors(t *testing.T) {
	f := Classify(StageTool, errors.New("tool call failed: something"))
	if f.Kind != "tool" {
		t.Fatalf("%+v", f)
	}
	f = Classify(StageEvaluate, errors.New("build failed: compile error"))
	if f.Kind != "build" {
		t.Fatalf("%+v", f)
	}
	f = Classify(StageEvaluate, errors.New("tests failed: 2 failures"))
	if f.Kind != "test" {
		t.Fatalf("%+v", f)
	}
}

func TestRateLimitBacksOffAndSwitchesModel(t *testing.T) {
	e := Engine{MaxAttempts: 3}
	p, err := e.Plan(Failure{Kind: "rate_limit", Error: "x"}, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if p.SkipRepair {
		t.Fatal("rate limit must be repairable")
	}
	if p.ModelCapability != "fast" {
		t.Fatalf("capability %q", p.ModelCapability)
	}
}

func TestAuthIsTerminal(t *testing.T) {
	e := Engine{MaxAttempts: 3}
	p, _ := e.Plan(Failure{Kind: "auth"}, 1, 3)
	if !p.SkipRepair {
		t.Fatal("auth failures must not be auto-repaired")
	}
}

func TestBuildFailureEscalatesRoles(t *testing.T) {
	e := Engine{MaxAttempts: 4}
	p, _ := e.Plan(Failure{Kind: "build", Error: "build failed"}, 1, 4)
	if p.Role != "debugger" {
		t.Fatalf("role %q", p.Role)
	}
	p2, _ := e.Plan(Failure{Kind: "build", Error: "build failed"}, 2, 4)
	if p2.Role != "debugger" || p2.ModelCapability != "reasoning" {
		t.Fatalf("attempt 2: %+v", p2)
	}
	// Attempt 3 is a structural repair: it must change the execution strategy
	// rather than repeat the same structure with the same role.
	p3, _ := e.Plan(Failure{Kind: "build", Error: "build failed", Strategy: "direct"}, 3, 4)
	if p3.ExecutionStrategy == "" {
		t.Fatalf("attempt 3 must restructure execution: %+v", p3)
	}
	if p3.ExecutionStrategy != "sequential" {
		t.Fatalf("direct failures should escalate to sequential, got %q", p3.ExecutionStrategy)
	}
	p3b, _ := e.Plan(Failure{Kind: "build", Error: "build failed", Strategy: "sequential"}, 3, 4)
	if p3b.ExecutionStrategy != "plan-implement-verify" {
		t.Fatalf("sequential failures should escalate to plan-implement-verify, got %q", p3b.ExecutionStrategy)
	}
}

func TestRepairLimitGivesUp(t *testing.T) {
	e := Engine{MaxAttempts: 2}
	p, err := e.Plan(Failure{Kind: "unknown", Error: "x"}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !p.SkipRepair {
		t.Fatalf("expected give-up: %+v", p)
	}
}

func TestUnknownFailureEscalates(t *testing.T) {
	e := Engine{MaxAttempts: 3}
	p, _ := e.Plan(Failure{Kind: "unknown", Error: "weird"}, 1, 3)
	if p.ModelCapability != "reasoning" {
		t.Fatalf("%+v", p)
	}
	p2, _ := e.Plan(Failure{Kind: "unknown", Error: "weird"}, 2, 3)
	if p2.Role != "reviewer" {
		t.Fatalf("%+v", p2)
	}
}

func TestBudgetIsTerminal(t *testing.T) {
	e := Engine{}
	p, _ := e.Plan(Failure{Kind: "budget"}, 1, 3)
	if !p.SkipRepair {
		t.Fatal("budget exhaustion must be terminal")
	}
}

func TestEveryPlanChangesSomething(t *testing.T) {
	e := Engine{MaxAttempts: 3}
	kinds := []string{"rate_limit", "server", "network", "timeout", "build", "test", "tool", "unknown"}
	for _, kind := range kinds {
		p, err := e.Plan(Failure{Kind: kind, Error: kind}, 1, 3)
		if err != nil {
			t.Fatal(err)
		}
		if p.SkipRepair {
			continue
		}
		if len(p.Changed) == 0 {
			t.Fatalf("plan for %q changed nothing", kind)
		}
	}
}

// Classification decides whether the next attempt backs off, switches model,
// or gives up, so a gateway error hidden inside a wrapper must still be found.
// The previous hand-rolled unwrapper followed Unwrap() error only; errors.Join
// produces Unwrap() []error, and a joined gateway error was classified as
// "unknown" — the kind that escalates instead of backing off.
func TestClassifyFindsAGatewayErrorInsideAJoin(t *testing.T) {
	gwErr := &gateway.Error{Kind: gateway.KindRateLimit, Status: 429, Message: "slow down"}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare", gwErr},
		{"wrapped once", fmt.Errorf("model call: %w", gwErr)},
		{"wrapped twice", fmt.Errorf("step: %w", fmt.Errorf("model call: %w", gwErr))},
		{"joined", errors.Join(errors.New("context cancelled"), gwErr)},
		{"joined then wrapped", fmt.Errorf("step: %w", errors.Join(errors.New("other"), gwErr))},
	} {
		if got := Classify(StageModel, tc.err).Kind; got != string(gateway.KindRateLimit) {
			t.Errorf("%s: kind = %q, want %q", tc.name, got, gateway.KindRateLimit)
		}
	}
}

// Budget exhaustion is terminal, so it must win over substrings that would
// otherwise route it somewhere retryable.
func TestBudgetClassificationWinsOverOtherSignals(t *testing.T) {
	f := Classify(StageModel, errors.New("budget exceeded while running the tool that timed out"))
	if f.Kind != "budget" {
		t.Errorf("kind = %q, want budget; a terminal failure must not be retried", f.Kind)
	}
}

// A "replan" failure is not a defect — nothing to reproduce — so it must
// skip straight to a structural change on the very first attempt, unlike
// build/test/evaluate which spend attempts 1-2 on a debugger role first.
func TestReplanSkipsStraightToRestructuring(t *testing.T) {
	e := Engine{MaxAttempts: 4}
	p, err := e.Plan(Failure{Kind: "replan", Error: "found two unrelated bugs", Strategy: "direct"}, 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if p.SkipRepair {
		t.Fatal("SkipRepair = true, want a real restructuring plan")
	}
	if p.ExecutionStrategy != "sequential" {
		t.Fatalf("direct → replan should escalate to sequential, got %q", p.ExecutionStrategy)
	}
	if !strings.Contains(p.ExtraInstructions, "found two unrelated bugs") {
		t.Errorf("ExtraInstructions = %q, want it to carry the reason", p.ExtraInstructions)
	}

	p2, _ := e.Plan(Failure{Kind: "replan", Error: "x", Strategy: "sequential"}, 1, 4)
	if p2.ExecutionStrategy != "plan-implement-verify" {
		t.Fatalf("sequential → replan should escalate to plan-implement-verify, got %q", p2.ExecutionStrategy)
	}

	p3, _ := e.Plan(Failure{Kind: "replan", Error: "x", Strategy: ""}, 1, 4)
	if p3.ExecutionStrategy != "sequential" {
		t.Fatalf("no prior strategy → replan should escalate to sequential, got %q", p3.ExecutionStrategy)
	}
}

// A replan request is still bounded by the same repair-limit machinery as
// every other failure kind — it must not be able to loop forever.
func TestReplanRespectsTheRepairLimit(t *testing.T) {
	e := Engine{MaxAttempts: 2}
	p, err := e.Plan(Failure{Kind: "replan", Error: "x"}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !p.SkipRepair {
		t.Fatalf("attempt at the limit must give up: %+v", p)
	}
}
