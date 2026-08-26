package memory

import (
	"testing"

	"omniharness/internal/id"
	"omniharness/internal/session"
	"omniharness/internal/task"
)

func openTest(t *testing.T) *session.Store {
	t.Helper()
	s, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seed(t *testing.T, s *session.Store, model, strategy string, completed bool, tokens int64, repairs int) {
	t.Helper()
	tsk := &task.Task{
		ID:        id.New(),
		SessionID: "s1",
		Spec:      task.Spec{Prompt: "task"},
		Strategy:  strategy,
		Repairs:   repairs,
	}
	if completed {
		tsk.Status = task.StatusCompleted
	} else {
		tsk.Status = task.StatusFailed
	}
	if err := s.CreateTask(tsk); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordModelCall(&session.ModelCall{
		SessionID: "s1", TaskID: tsk.ID, Model: model, TokensIn: tokens, TokensOut: 100, Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	if completed {
		if err := s.RecordEvaluation(&session.Evaluation{SessionID: "s1", TaskID: tsk.ID, Evaluator: "tests", Outcome: "PASS"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAggregateSuccessRates(t *testing.T) {
	s := openTest(t)
	// model A: 2 runs, 2 successes (100%)
	seed(t, s, "model-a", "direct", true, 100, 0)
	seed(t, s, "model-a", "direct", true, 120, 1)
	// model B: 3 runs, 1 success (33%)
	seed(t, s, "model-b", "direct", true, 200, 2)
	seed(t, s, "model-b", "direct", false, 300, 1)
	seed(t, s, "model-b", "direct", false, 400, 3)

	stats, err := Aggregate(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("stats = %d, want 2", len(stats))
	}
	byModel := map[string]PerfStat{}
	for _, st := range stats {
		byModel[st.Model] = st
	}
	a := byModel["model-a"]
	if a.Runs != 2 || a.Successes != 2 || a.SuccessRate != 1.0 {
		t.Fatalf("model-a %+v", a)
	}
	if a.AvgRepairs != 0.5 {
		t.Fatalf("model-a avg repairs %v", a.AvgRepairs)
	}
	if a.Outcomes["PASS"] != 2 {
		t.Fatalf("model-a outcomes %v", a.Outcomes)
	}
	b := byModel["model-b"]
	if b.Runs != 3 || b.SuccessRate != 1.0/3.0 {
		t.Fatalf("model-b %+v", b)
	}
	if b.Outcomes["PASS"] != 1 {
		t.Fatalf("model-b outcomes %v", b.Outcomes)
	}
}

func TestBestRespectsMinRuns(t *testing.T) {
	stats := []PerfStat{
		{PerfKey: PerfKey{Model: "m1", Strategy: "direct"}, Runs: 1, Successes: 1, SuccessRate: 1.0},
		{PerfKey: PerfKey{Model: "m2", Strategy: "direct"}, Runs: 10, Successes: 8, SuccessRate: 0.8},
	}
	best, ok := Best(stats, 5)
	if !ok || best.Model != "m2" {
		t.Fatalf("best = %+v ok=%v", best, ok)
	}
	if _, ok := Best(stats, 100); ok {
		t.Fatal("should not find best below min runs")
	}
}

func TestProjectMemory(t *testing.T) {
	s := openTest(t)
	p := Project(s)
	if err := p.Remember("proj-x", "instructions", "build with -tags=prod"); err != nil {
		t.Fatal(err)
	}
	content, found, err := p.Recall("proj-x", "instructions")
	if err != nil || !found || content != "build with -tags=prod" {
		t.Fatalf("content=%q found=%v err=%v", content, found, err)
	}
	if _, found, _ := p.Recall("proj-x", "missing"); found {
		t.Fatal("unexpected found")
	}
	all, err := p.RecallAll("proj-x")
	if err != nil || len(all) != 1 {
		t.Fatalf("all = %d err=%v", len(all), err)
	}
}
