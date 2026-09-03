package memory

import (
	"testing"

	"omniharness/internal/session"
	"omniharness/internal/task"
)

// seedTaskWithCalls inserts one task and n model calls against it — the
// shape a real multi-turn task actually has, unlike seedTask's fixed 1:1.
func seedTaskWithCalls(t *testing.T, s *session.Store, sid, taskID, strategy, model, status string, repairs, n int) {
	t.Helper()
	tsk := &task.Task{ID: taskID, SessionID: sid, Strategy: strategy, Status: task.Status(status), Repairs: repairs}
	if err := s.CreateTask(tsk); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := s.RecordModelCall(&session.ModelCall{
			SessionID: sid, TaskID: taskID, Model: model, TokensIn: 100, TokensOut: 50,
			CostUSD: 0.01, Status: "ok",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// seedTask inserts a task + a model call so the advisor has real data.
func seedTask(t *testing.T, s *session.Store, sid, taskID, strategy, model, status string) {
	t.Helper()
	tsk := &task.Task{ID: taskID, SessionID: sid, Strategy: strategy, Status: task.Status(status)}
	if err := s.CreateTask(tsk); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordModelCall(&session.ModelCall{
		SessionID: sid, TaskID: taskID, Model: model, TokensIn: 100, TokensOut: 50,
		CostUSD: 0.01, Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdvisorColdStartDeclines(t *testing.T) {
	s, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := &Advisor{Store: s, MinRuns: 3}
	// Zero data: must not recommend anything.
	advice, ok := a.RecommendModel([]string{"cursor/m1", "openai/m2"})
	if ok {
		t.Fatalf("cold start must decline, got %+v", advice)
	}
	if advice.Reason == "" {
		t.Fatal("decline must still explain why")
	}
}

func TestAdvisorNeedsMinRuns(t *testing.T) {
	s, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedTask(t, s, "s1", "t1", "direct", "cursor/m1", "completed")
	seedTask(t, s, "s2", "t2", "direct", "cursor/m1", "completed")
	// 2 runs < MinRuns 3: still cold.
	a := &Advisor{Store: s, MinRuns: 3}
	if _, ok := a.RecommendModel([]string{"cursor/m1", "openai/m2"}); ok {
		t.Fatal("below MinRuns must decline")
	}
	a2 := &Advisor{Store: s, MinRuns: 2}
	if advice, ok := a2.RecommendModel([]string{"cursor/m1", "openai/m2"}); !ok {
		t.Fatal("at MinRuns should recommend")
	} else if advice.Model != "cursor/m1" {
		t.Fatalf("recommended %q", advice.Model)
	}
}

func TestAdvisorPrefersHigherSuccess(t *testing.T) {
	s, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// m-a: 4/5 completed. m-b: 1/5 completed.
	for i := 0; i < 5; i++ {
		status := "failed"
		if i < 4 {
			status = "completed"
		}
		seedTask(t, s, "s", "ta"+itoa(i), "direct", "cursor/m-a", status)
		status = "failed"
		if i < 1 {
			status = "completed"
		}
		seedTask(t, s, "s", "tb"+itoa(i), "direct", "openai/m-b", status)
	}
	a := &Advisor{Store: s, MinRuns: 3}
	advice, ok := a.RecommendModel([]string{"cursor/m-a", "openai/m-b"})
	if !ok {
		t.Fatal("expected recommendation")
	}
	if advice.Model != "cursor/m-a" {
		t.Fatalf("recommended %q, want cursor/m-a (%s)", advice.Model, advice.Reason)
	}
	if len(advice.Candidates) != 2 {
		t.Fatalf("candidates = %d, want both models listed", len(advice.Candidates))
	}
}

func TestAdvisorConfidenceBeatsOneHitWonder(t *testing.T) {
	s, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// m-a: 40/50 (80%). m-b: 1/1 (100% but tiny sample).
	for i := 0; i < 50; i++ {
		status := "failed"
		if i < 40 {
			status = "completed"
		}
		seedTask(t, s, "s", "ta"+itoa(i), "direct", "cursor/m-a", status)
	}
	seedTask(t, s, "s", "tb0", "direct", "openai/m-b", "completed")
	a := &Advisor{Store: s, MinRuns: 3}
	advice, ok := a.RecommendModel([]string{"cursor/m-a", "openai/m-b"})
	if !ok {
		t.Fatal("expected recommendation")
	}
	if advice.Model != "cursor/m-a" {
		t.Fatalf("confidence-adjusted score must prefer 40/50 over 1/1, got %q (%s)", advice.Model, advice.Reason)
	}
}

func TestAdvisorStrategyPerformanceAndRecommend(t *testing.T) {
	s, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// sequential: poor (1/5). direct: strong (4/5).
	statusFor := func(ok bool) string {
		if ok {
			return "completed"
		}
		return "failed"
	}
	for i := 0; i < 5; i++ {
		seedTask(t, s, "s", "sq"+itoa(i), "sequential", "cursor/m1", statusFor(i == 0))
		seedTask(t, s, "s", "di"+itoa(i), "direct", "cursor/m1", statusFor(i < 4))
	}
	a := &Advisor{Store: s, MinRuns: 3}
	hist, err := a.StrategyPerformance()
	if err != nil {
		t.Fatal(err)
	}
	if hist["sequential"].Runs != 5 {
		t.Fatalf("sequential runs = %d", hist["sequential"].Runs)
	}
	alt, reason, ok := a.RecommendStrategy("sequential", hist)
	if !ok {
		t.Fatal("sequential's poor record should trigger a recommendation")
	}
	if alt != "direct" {
		t.Fatalf("recommended %q, want direct (%s)", alt, reason)
	}
	// The strong strategy must not be overridden.
	if _, _, ok := a.RecommendStrategy("direct", hist); ok {
		t.Fatal("a strong profile choice must not be overridden")
	}
}

// A task with several model calls (multiple agent turns, a multi-step
// strategy) must count as exactly one run, not one run per call — the bug
// this pins joining model_calls to tasks directly let a single 3-call
// completed task push successes to 3 against a runs count of 1, a
// mathematically impossible >100% success rate.
func TestModelStatsDoesNotDoubleCountATaskWithSeveralCalls(t *testing.T) {
	s, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedTaskWithCalls(t, s, "s", "multi", "direct", "cursor/m1", "completed", 0, 3)

	a := &Advisor{Store: s, MinRuns: 1}
	runs, successes, _, _, _, err := a.modelStats("cursor/m1")
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("runs = %d, want 1 (one task, regardless of its 3 model calls)", runs)
	}
	if successes != 1 {
		t.Fatalf("successes = %d, want 1", successes)
	}
}

// Same bug, same fix, on the strategy-level aggregation RecommendStrategy's
// threshold logic depends on.
func TestStrategyPerformanceDoesNotDoubleCountATaskWithSeveralCalls(t *testing.T) {
	s, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedTaskWithCalls(t, s, "s", "multi", "direct", "cursor/m1", "completed", 0, 3)

	a := &Advisor{Store: s, MinRuns: 1}
	hist, err := a.StrategyPerformance()
	if err != nil {
		t.Fatal(err)
	}
	got := hist["direct"]
	if got.Runs != 1 {
		t.Fatalf("Runs = %d, want 1 (one task, regardless of its 3 model calls)", got.Runs)
	}
	if got.SuccessRate != 1.0 {
		t.Fatalf("SuccessRate = %v, want 1.0 — a >100%% rate was the actual bug", got.SuccessRate)
	}
	// $0.01 × 3 calls = $0.03 total for the one task; the strategy average
	// over its one task is that same $0.03, not $0.01 (the per-call figure).
	if got.AvgCostUSD < 0.0299 || got.AvgCostUSD > 0.0301 {
		t.Fatalf("AvgCostUSD = %v, want ~0.03 (the task's total cost, not one call's)", got.AvgCostUSD)
	}
}

// A task's repair count must contribute once per task, not once per model
// call — otherwise a strategy whose tasks happen to make more calls looks
// like it needs more repairs than an equally-repaired strategy that makes
// fewer.
func TestStrategyPerformanceAveragesRepairsPerTaskNotPerCall(t *testing.T) {
	s, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedTaskWithCalls(t, s, "s", "a", "direct", "cursor/m1", "completed", 2, 1)
	seedTaskWithCalls(t, s, "s", "b", "direct", "cursor/m1", "completed", 2, 5)

	a := &Advisor{Store: s, MinRuns: 1}
	hist, err := a.StrategyPerformance()
	if err != nil {
		t.Fatal(err)
	}
	got := hist["direct"]
	if got.Runs != 2 {
		t.Fatalf("Runs = %d, want 2", got.Runs)
	}
	if got.AvgRepairs != 2 {
		t.Fatalf("AvgRepairs = %v, want 2 (both tasks had exactly 2 repairs; the 5-call task must not outweigh the 1-call task)", got.AvgRepairs)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
