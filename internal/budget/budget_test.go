package budget

import (
	"testing"
	"time"
)

func TestUnlimitedBudgetNeverExceeds(t *testing.T) {
	tr := NewTracker(Budget{})
	for i := 0; i < 1000; i++ {
		tr.AddTokens(1_000_000, 10)
		tr.AddToolCall()
		tr.AddRepairCycle()
	}
	if e := tr.Exceeded(); e != "" {
		t.Fatalf("unlimited budget reported exceeded: %s", e)
	}
}

func TestTokenBudgetExceeded(t *testing.T) {
	tr := NewTracker(Budget{MaxTokens: 100})
	tr.AddTokens(99, 0)
	if e := tr.Exceeded(); e != "" {
		t.Fatalf("unexpected: %s", e)
	}
	tr.AddTokens(1, 0)
	if e := tr.Exceeded(); e == "" {
		t.Fatal("expected token budget exceeded")
	}
}

func TestCostBudgetExceeded(t *testing.T) {
	tr := NewTracker(Budget{MaxCostUSD: 1.0})
	tr.AddTokens(0, 1.0)
	if e := tr.Exceeded(); e == "" {
		t.Fatal("expected cost budget exceeded")
	}
}

func TestToolCallBudget(t *testing.T) {
	tr := NewTracker(Budget{MaxToolCalls: 3})
	for i := 0; i < 3; i++ {
		tr.AddToolCall()
	}
	if e := tr.Exceeded(); e == "" {
		t.Fatal("expected tool-call budget exceeded")
	}
}

func TestRepairBudget(t *testing.T) {
	tr := NewTracker(Budget{MaxRepairCycl: 2})
	tr.AddRepairCycle()
	tr.AddRepairCycle()
	if e := tr.Exceeded(); e == "" {
		t.Fatal("expected repair budget exceeded")
	}
}

func TestDurationBudget(t *testing.T) {
	tr := NewTracker(Budget{MaxDuration: time.Millisecond})
	time.Sleep(5 * time.Millisecond)
	if e := tr.Exceeded(); e == "" {
		t.Fatal("expected duration budget exceeded")
	}
}

func TestUsageSnapshot(t *testing.T) {
	tr := NewTracker(Budget{})
	tr.AddTokens(50, 0.5)
	tr.AddToolCall()
	u := tr.Usage()
	if u.Tokens != 50 || u.CostUSD != 0.5 || u.ToolCalls != 1 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestConcurrentTracking(t *testing.T) {
	tr := NewTracker(Budget{MaxTokens: 10_000})
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				tr.AddTokens(10, 0.001)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if u := tr.Usage(); u.Tokens != 8000 {
		t.Fatalf("tokens = %d, want 8000", u.Tokens)
	}
}
