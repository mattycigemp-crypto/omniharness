package memory

import (
	"testing"

	"omniharness/internal/session"
	"omniharness/internal/task"
)

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
