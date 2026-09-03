package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"omniharness/internal/budget"
	"omniharness/internal/config"
	"omniharness/internal/event"
	"omniharness/internal/gateway"
	"omniharness/internal/task"
	"omniharness/internal/testutil"
)

func testRuntime(t *testing.T, fake *testutil.FakeOmniRoute, dir string) *Runtime {
	t.Helper()
	testutil.InitFakeWorkspace(t, dir)
	cfg := config.Default()
	cfg.Persistence.Dir = dir
	cfg.Policy.WorkspaceRoot = dir
	rt, err := New(cfg, Options{
		Gateway:                fake.Client(),
		DisablePersistenceSink: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return rt
}

func TestRuntimeRunsTaskAndPersists(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "deliverable"})
	rt := testRuntime(t, fake, t.TempDir())

	ss, err := rt.NewSession("", "test task")
	if err != nil {
		t.Fatal(err)
	}
	tsk, err := rt.RunTask(context.Background(), ss.ID, "Fix the typo in README.md.", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %s: %s", tsk.Status, tsk.Error)
	}

	// Durable: reload from the store.
	got, err := rt.Store.GetTask(tsk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusCompleted || got.Strategy == "" {
		t.Fatalf("reloaded task %+v", got)
	}

	// Durability invariant: by the time RunTask returns, the terminal event
	// must already be durable — no polling, no sleep. The bounded flush in
	// RunTask guarantees task.completed is on disk before the caller sees the
	// result.
	evs, err := rt.SessionEvents(ss.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	types := map[event.Type]bool{}
	for _, e := range evs {
		types[e.Type] = true
	}
	for _, want := range []event.Type{event.TaskCreated, event.TaskAnalyzed, event.StrategySelected, event.AgentCreated, event.TaskCompleted} {
		if !types[want] {
			t.Fatalf("missing persisted event %s (have %v)", want, types)
		}
	}

	// Model calls recorded.
	calls, err := rt.Store.ModelCalls(ss.ID)
	if err != nil || len(calls) == 0 {
		t.Fatalf("model calls %v err %v", calls, err)
	}
}

func TestRuntimeSessionsListed(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "x"})
	rt := testRuntime(t, fake, t.TempDir())
	if _, err := rt.NewSession("", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.NewSession("", "beta"); err != nil {
		t.Fatal(err)
	}
	sessions, err := rt.ListSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d", len(sessions))
	}
}

func TestRuntimePing(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	rt := testRuntime(t, fake, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rt.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestRuntimeCancellation(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{testutil.ToolCall("c1", "read_file", `{"path":"x"}`)}, Delay: 300 * time.Millisecond},
	)
	rt := testRuntime(t, fake, t.TempDir())
	ss, _ := rt.NewSession("", "cancel me")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = rt.RunTask(ctx, ss.ID, "Add a feature to the codebase.", RunOptions{})
		close(done)
	}()
	time.Sleep(400 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not stop on cancel")
	}
}

// TestConcurrentTasksAllDurable is the regression guard for the async
// persistence flush race: RunTask must never return a completed task whose
// terminal event is still in flight. The old idle-flag protocol had a window
// between the sink receiving an event and marking itself busy; the seq-based
// flush closes it. Run with many concurrent tasks so scheduling pressure
// widens the window.
func TestConcurrentTasksAllDurable(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "deliverable"})
	rt := testRuntime(t, fake, t.TempDir())

	const n = 12
	type result struct {
		sessionID string
		taskID    string
		err       error
	}
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			ss, err := rt.NewSession("", "concurrent")
			if err != nil {
				results <- result{err: err}
				return
			}
			tsk, err := rt.RunTask(context.Background(), ss.ID, "Fix the typo in README.md.", RunOptions{})
			results <- result{sessionID: ss.ID, taskID: tsk.ID, err: err}
		}(i)
	}

	for i := 0; i < n; i++ {
		res := <-results
		if res.err != nil {
			t.Fatalf("task %d: %v", i, res.err)
		}
		// Durability invariant: the terminal event must already be on disk the
		// moment RunTask returns — no polling, no retry.
		evs, err := rt.SessionEvents(res.sessionID, 100)
		if err != nil {
			t.Fatal(err)
		}
		types := map[event.Type]bool{}
		for _, e := range evs {
			types[e.Type] = true
		}
		if !types[event.TaskCompleted] {
			t.Fatalf("task %s: task.completed not durable on return (have %v)", res.taskID, types)
		}
	}
}

func TestRuntimeBudgetsRespected(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "x"})
	rt := testRuntime(t, fake, t.TempDir())
	ss, _ := rt.NewSession("", "budgeted")
	tsk, err := rt.RunTask(context.Background(), ss.ID, "do something",
		RunOptions{Deadline: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status %s: %s", tsk.Status, tsk.Error)
	}
	_ = strings.TrimSpace
}

// The [budgets] config section reached the orchestrator's concurrency and
// repair caps, but max_tokens, max_cost_usd, max_duration and max_tool_calls
// were read from the file and then never consulted. A per-run flag still wins.
func TestConfigBudgetsReachTheTaskSpec(t *testing.T) {
	cfg := config.Default()
	cfg.Budgets.MaxTokens = 1234
	cfg.Budgets.MaxCostUSD = 5.5
	cfg.Budgets.MaxToolCalls = 7
	rt := &Runtime{Cfg: cfg}

	// Nothing set per run: the file's ceilings apply.
	got := rt.budgetFor(budget.Budget{})
	if got.MaxTokens != 1234 || got.MaxCostUSD != 5.5 || got.MaxToolCalls != 7 {
		t.Fatalf("config budgets did not reach the spec: %+v", got)
	}

	// An explicit per-run value wins, and the rest still fall back.
	got = rt.budgetFor(budget.Budget{MaxCostUSD: 0.25})
	if got.MaxCostUSD != 0.25 {
		t.Errorf("per-run cost = %v, want the override 0.25", got.MaxCostUSD)
	}
	if got.MaxTokens != 1234 {
		t.Errorf("unset dimensions must still fall back: %+v", got)
	}
}

// config.Default() leaves deep_analysis off, so a task that would otherwise
// warrant deepening must still come back with no AcceptanceCriteria: the
// runtime must not wire the pass in unless the config says to.
func TestDeepAnalysisOffByDefaultInRuntime(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "done"})
	rt := testRuntime(t, fake, t.TempDir())

	ss, err := rt.NewSession("", "test task")
	if err != nil {
		t.Fatal(err)
	}
	tsk, err := rt.RunTask(context.Background(), ss.ID, "clean up the code", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Profile.AcceptanceCriteria != nil {
		t.Fatalf("AcceptanceCriteria = %v, want nil with deep_analysis left at its default (off)", tsk.Profile.AcceptanceCriteria)
	}
}

// Turning cfg.Task.DeepAnalysis on must actually reach the orchestrator —
// proven end to end, not just by inspecting the config value.
func TestDeepAnalysisWiredFromConfig(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{Content: `["a real criterion"]`}, // the deepening call
		testutil.FakeStep{Content: "done"},
	)
	dir := t.TempDir()
	testutil.InitFakeWorkspace(t, dir)
	cfg := config.Default()
	cfg.Persistence.Dir = dir
	cfg.Policy.WorkspaceRoot = dir
	cfg.Task.DeepAnalysis = true
	rt, err := New(cfg, Options{Gateway: fake.Client()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)

	ss, err := rt.NewSession("", "test task")
	if err != nil {
		t.Fatal(err)
	}
	tsk, err := rt.RunTask(context.Background(), ss.ID, "clean up the code", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tsk.Profile.AcceptanceCriteria) != 1 {
		t.Fatalf("AcceptanceCriteria = %v, want 1 entry — config.Task.DeepAnalysis=true should reach the orchestrator", tsk.Profile.AcceptanceCriteria)
	}
}
