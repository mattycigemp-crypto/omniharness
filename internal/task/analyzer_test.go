package task

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestTrivialTaskIsCheap(t *testing.T) {
	a := Analyzer{}
	p := a.Analyze(Spec{Prompt: "Fix the typo in README.md."})
	if p.Complexity != ComplexityLow {
		t.Fatalf("complexity = %v, want LOW", p.Complexity)
	}
	if p.Domain != DomainSoftware {
		t.Fatalf("domain = %v, want SOFTWARE", p.Domain)
	}
	if p.Parallelizable {
		t.Fatal("trivial task must not be parallelizable")
	}
	if p.EstimatedCostUSD > 0.01 {
		t.Fatalf("trivial task cost estimate too high: %v", p.EstimatedCostUSD)
	}
	if len(p.Signals) == 0 {
		t.Fatal("profile must explain its judgments")
	}
}

func TestComplexParallelTask(t *testing.T) {
	a := Analyzer{}
	p := a.Analyze(Spec{Prompt: `Design a distributed task scheduler and implement it in Go.
	It must handle retries, backoff, worker pools, and cancellation.
	Also add observability and a CLI. Write tests for all of it.`})
	if p.Complexity != ComplexityHigh {
		t.Fatalf("complexity = %v, want HIGH", p.Complexity)
	}
	if p.Domain != DomainSoftware {
		t.Fatalf("domain = %v", p.Domain)
	}
	if p.Verification != VerificationRecommended && p.Verification != VerificationRequired {
		t.Fatalf("verification = %v", p.Verification)
	}
	if len(p.Tools) == 0 {
		t.Fatal("expected tool requirements")
	}
}

func TestDangerousTaskRaisesRiskAndApproval(t *testing.T) {
	a := Analyzer{}
	p := a.Analyze(Spec{Prompt: "Delete the production database and force push to master."})
	if p.Risk != LevelHigh {
		t.Fatalf("risk = %v, want HIGH", p.Risk)
	}
	if !p.ApprovalRecommended {
		t.Fatal("approval should be recommended for high-risk tasks")
	}
}

func TestOrderedTaskNotParallel(t *testing.T) {
	a := Analyzer{}
	p := a.Analyze(Spec{Prompt: "First write the parser, then the evaluator, then the repl, and finally the docs."})
	if p.Parallelizable {
		t.Fatal("ordered task must not be flagged parallelizable")
	}
}

func TestResearchDomain(t *testing.T) {
	a := Analyzer{}
	p := a.Analyze(Spec{Prompt: "Research the latest literature on speculative decoding and survey the evidence."})
	if p.Domain != DomainResearch {
		t.Fatalf("domain = %v, want RESEARCH", p.Domain)
	}
}

func TestContextLargeWithRepo(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 400; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := Analyzer{RepoRoot: dir}
	p := a.Analyze(Spec{Prompt: "Add a feature to the codebase."})
	if p.Context != LevelLarge {
		t.Fatalf("context = %v, want LARGE", p.Context)
	}
}

func TestBudgetUnlimitedAndExceeded(t *testing.T) {
	// Budget behavior is covered in package budget; here we only verify the
	// default spec budget is zero (unlimited) so tasks are not blocked.
	s := Spec{Prompt: "x"}
	if !s.Budget.Unlimited() {
		t.Fatal("default spec budget must be unlimited")
	}
}
