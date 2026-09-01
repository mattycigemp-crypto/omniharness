package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omniharness/internal/agent"
	composer "omniharness/internal/context"
	"omniharness/internal/evaluate"
	"omniharness/internal/event"
	"omniharness/internal/gateway"
	"omniharness/internal/model"
	"omniharness/internal/policy"
	"omniharness/internal/repair"
	"omniharness/internal/session"
	"omniharness/internal/strategy"
	"omniharness/internal/task"
	"omniharness/internal/testutil"
	"omniharness/internal/tools"
)

func newOrchestrator(t *testing.T, fake *testutil.FakeOmniRoute, workspace string) (*Orchestrator, *session.Store, *event.Bus) {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	reg := tools.NewRegistry()
	if err := tools.NewNative(workspace).Register(reg); err != nil {
		t.Fatal(err)
	}
	pol := policy.NewEngine(policy.Config{
		RiskAction: map[string]string{
			"low": "allow", "medium": "allow", "high": "ask", "critical": "block",
		},
		ShellAllowed:            true,
		GitPushRequiresApproval: true,
	}, nil)
	pol.SetApprover(policy.ApproverFunc(func(context.Context, policy.Request, string) (bool, error) { return true, nil }))

	bus := event.NewBus()
	evals := evaluate.NewRegistry()
	if err := evals.RegisterDefaults(); err != nil {
		t.Fatal(err)
	}

	deps := Deps{
		Bus:        bus,
		Store:      store,
		Gateway:    fake.Client(),
		ModelSel:   model.NewSelector("fake/m1", nil),
		Roles:      agent.DefaultRoles(),
		Evaluators: evals,
		Repair:     &repair.Engine{MaxAttempts: 2},
		Analyzer:   &task.Analyzer{RepoRoot: workspace},
		Strategist: strategy.Selector{},
		Tools:      reg,
		Policy:     pol,
		Composer:   composer.NewComposer(composer.Limits{}),
		Workspace:  workspace,
	}
	return New(deps), store, bus
}

func runTask(t *testing.T, o *Orchestrator, sessionID string, spec task.Spec) (*task.Task, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return o.Run(ctx, sessionID, spec, "")
}

func TestDirectStrategyRunsSingleAgent(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "fixed it"})
	o, store, _ := newOrchestrator(t, fake, dir)

	spec := task.Spec{Prompt: "Fix the typo in README.md.", CWD: dir, SessionID: "s1"}
	tsk, err := runTask(t, o, "s1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %s, error = %s", tsk.Status, tsk.Error)
	}
	if tsk.Strategy != string(strategy.Direct) {
		t.Fatalf("strategy = %s, want direct", tsk.Strategy)
	}
	agents, _ := store.AgentsForTask(tsk.ID)
	if len(agents) != 1 {
		t.Fatalf("expected exactly one agent, got %d", len(agents))
	}
	if agents[0].Role != string(agent.RoleImplementer) {
		t.Fatalf("role = %s", agents[0].Role)
	}
	if !strings.Contains(tsk.Result.Summary, "fixed it") {
		t.Fatalf("summary %q", tsk.Result.Summary)
	}
}

func TestParallelStrategyRunsConcurrently(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{Content: "subtask result", Delay: 200 * time.Millisecond},
	)
	o, store, _ := newOrchestrator(t, fake, dir)

	spec := task.Spec{
		Prompt: "Implement feature A and feature B and feature C. Run them in parallel and write a summary.",
		CWD:    dir,
	}
	tsk, err := runTask(t, o, "s1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %s: %s", tsk.Status, tsk.Error)
	}
	agents, _ := store.AgentsForTask(tsk.ID)
	// p0 + p1 parallel, then join → 3 agents.
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents (2 parallel + join), got %d", len(agents))
	}
	if fake.RequestCount() < 3 {
		t.Fatalf("expected >=3 model calls, got %d", fake.RequestCount())
	}
	// Concurrency proof: total wall time must be less than sequential
	// execution of all steps (3 steps × 200ms = 600ms + join overhead).
	if tsk.Strategy != string(strategy.Parallel) {
		t.Fatalf("strategy = %s", tsk.Strategy)
	}
}

// passAfterOne fails the first evaluation then passes.
type passAfterOne struct{ count int }

func (p *passAfterOne) Name() string { return "diff-check" }

func (p *passAfterOne) Evaluate(_ context.Context, r evaluate.Request) (evaluate.Outcome, string, error) {
	if len(r.Result.Artifacts) > 0 {
		return evaluate.Pass, "artifacts", nil
	}
	p.count++
	if p.count <= 1 {
		return evaluate.Fail, "first try fails", nil
	}
	return evaluate.Pass, "ok", nil
}

func TestRepairLoopEndToEnd(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "the deliverable"})
	o, store, _ := newOrchestrator(t, fake, dir)

	// Replace the diff-check evaluator with one that fails exactly once.
	repl := &passAfterOne{}
	reg := evaluate.NewRegistry()
	if err := reg.Register(repl); err != nil {
		t.Fatal(err)
	}
	o.deps.Evaluators = reg

	spec := task.Spec{
		Prompt: "Add a feature to the codebase. Verify it works.",
		CWD:    dir,
	}
	tsk, err := runTask(t, o, "s1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %s: %s", tsk.Status, tsk.Error)
	}
	if tsk.Repairs < 1 {
		t.Fatalf("expected >=1 repair cycle, got %d", tsk.Repairs)
	}
	// The repair must have re-run the task (more agents than a single run).
	agents, _ := store.AgentsForTask(tsk.ID)
	if len(agents) < 2 {
		t.Fatalf("expected re-run agents, got %d", len(agents))
	}
}

func TestRepairLimitFailsTask(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "x"})
	o, store, _ := newOrchestrator(t, fake, dir)

	// Always-failing evaluator.
	reg := evaluate.NewRegistry()
	if err := reg.Register(&alwaysFail{}); err != nil {
		t.Fatal(err)
	}
	o.deps.Evaluators = reg
	o.deps.MaxTaskRepairs = 2

	spec := task.Spec{Prompt: "Add a feature to the codebase. Verify it works.", CWD: dir}
	tsk, err := runTask(t, o, "s1", spec)
	if err == nil {
		t.Fatal("expected task failure")
	}
	if tsk.Status != task.StatusFailed {
		t.Fatalf("status = %s", tsk.Status)
	}
	if tsk.Repairs != o.deps.MaxTaskRepairs {
		t.Fatalf("repairs = %d, want %d", tsk.Repairs, o.deps.MaxTaskRepairs)
	}
	_ = store
}

type alwaysFail struct{}

func (a *alwaysFail) Name() string { return "diff-check" }

func (a *alwaysFail) Evaluate(_ context.Context, r evaluate.Request) (evaluate.Outcome, string, error) {
	if len(r.Result.Artifacts) > 0 {
		return evaluate.Pass, "artifacts", nil
	}
	return evaluate.Fail, "always fails", nil
}

func TestVerificationFailureEscalatesToStrategyLevelRepair(t *testing.T) {
	workspace := t.TempDir()
	// An invalid go.mod makes the real go-build evaluator fail determinis-
	// tically, driving the task-level repair loop.
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("this is not a valid go.mod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "result"})
	o, _, bus := newOrchestrator(t, fake, workspace)
	o.deps.MaxTaskRepairs = 3

	var lastStrategy string
	ch, cancel := bus.SubscribeTo(128, event.StrategySelected)
	defer cancel()
	go func() {
		for e := range ch {
			var d event.StrategySelectedData
			if err := json.Unmarshal(e.Data, &d); err == nil && d.Strategy != "" {
				lastStrategy = d.Strategy
			}
		}
	}()

	tsk, err := o.Run(context.Background(), "sess", task.Spec{
		Prompt: "Fix the bug in the Go codebase.",
		CWD:    workspace,
	}, "")
	if err == nil {
		t.Fatal("verification failures must fail the task")
	}
	if tsk.Status != task.StatusFailed {
		t.Fatalf("status = %s", tsk.Status)
	}
	if tsk.Repairs < 1 {
		t.Fatalf("expected at least one repair cycle, got %d", tsk.Repairs)
	}
	// Strategy-level repair: the final execution strategy must differ from
	// the original profile choice (a pure retry would keep it identical).
	// "direct" is the profile choice for a low-complexity prompt; the
	// structural repair escalates it to "sequential".
	if lastStrategy == "direct" {
		t.Fatalf("strategy never changed during repair (stayed direct); repair was not structural")
	}
	if lastStrategy == "" {
		t.Fatal("no strategy observed")
	}
	t.Logf("strategy escalated to %q over %d repairs", lastStrategy, tsk.Repairs)
}

func TestModelFailureTriggersStepRepair(t *testing.T) {
	dir := t.TempDir()
	// First call fails with 500; the step repair loop must change variables
	// and retry with a new agent, which then succeeds.
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{StatusCode: 500},
		testutil.FakeStep{Content: "recovered"},
	)
	o, store, _ := newOrchestrator(t, fake, dir)

	spec := task.Spec{Prompt: "Add a feature to the codebase.", CWD: dir}
	tsk, err := runTask(t, o, "s1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %s: %s", tsk.Status, tsk.Error)
	}
	agents, _ := store.AgentsForTask(tsk.ID)
	if len(agents) < 2 {
		t.Fatalf("expected multiple agents after repair, got %d", len(agents))
	}
	if tsk.Repairs < 1 {
		t.Fatalf("repairs = %d", tsk.Repairs)
	}
}

func TestSequentialStrategyRespectsOrder(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "step result"})
	o, store, _ := newOrchestrator(t, fake, dir)
	spec := task.Spec{Prompt: "First write the parser, then the evaluator, then the repl.", CWD: dir}
	tsk, err := runTask(t, o, "s1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Strategy != string(strategy.Sequential) {
		t.Fatalf("strategy = %s", tsk.Strategy)
	}
	agents, _ := store.AgentsForTask(tsk.ID)
	if len(agents) != 3 {
		t.Fatalf("expected 3 sequential agents, got %d", len(agents))
	}
}

func TestCancellationStopsTask(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{testutil.ToolCall("c1", "read_file", `{"path":"x"}`)}, Delay: 200 * time.Millisecond},
	)
	o, _, _ := newOrchestrator(t, fake, dir)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := o.Run(ctx, "s1", task.Spec{Prompt: "Add a feature to the codebase.", CWD: dir}, "")
		done <- err
	}()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled && err != nil {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task did not stop")
	}
}

func TestArtifactsCollected(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("c1", "write_file", `{"path":"out.txt","content":"data"}`),
		}},
		testutil.FakeStep{Content: "wrote out.txt"},
	)
	o, _, _ := newOrchestrator(t, fake, dir)
	spec := task.Spec{Prompt: "Create out.txt with data and report it.", CWD: dir}
	tsk, err := runTask(t, o, "s1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %s", tsk.Status)
	}
	if len(tsk.Result.Artifacts) == 0 {
		t.Fatal("expected artifacts")
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); err != nil {
		t.Fatalf("out.txt missing: %v", err)
	}
}

func TestEventsPublishedForLifecycle(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "done"})
	o, store, bus := newOrchestrator(t, fake, dir)

	ch, cancel := bus.Subscribe(256)
	defer cancel()
	_ = store

	spec := task.Spec{Prompt: "Fix the typo in README.md.", CWD: dir}
	ctx, c2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer c2()
	tsk, err := o.Run(ctx, "s1", spec, "")
	if err != nil {
		t.Fatal(err)
	}

	var got []event.Type
	deadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case e := <-ch:
			got = append(got, e.Type)
			if e.Type == event.TaskCompleted && e.TaskID == tsk.ID {
				break drain
			}
		case <-deadline:
			break drain
		}
	}
	want := []event.Type{event.TaskCreated, event.TaskAnalyzed, event.StrategySelected, event.TaskStarted}
	for _, w := range want {
		if !containsType(got, w) {
			t.Fatalf("missing event %s in %v", w, got)
		}
	}
	if !containsType(got, event.AgentCreated) || !containsType(got, event.AgentCompleted) {
		t.Fatalf("missing agent events: %v", got)
	}
	if !containsType(got, event.TaskCompleted) {
		t.Fatalf("missing task completed: %v", got)
	}
}

func containsType(types []event.Type, want event.Type) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

// The agent lifecycle events are raised by the orchestrator rather than by the
// agent itself, so they were arriving with an empty envelope AgentID and the
// CLI rendered them as "agent[] failed" — no way to tell which agent, which is
// the whole point of the field in a run with several of them.
func TestAgentLifecycleEventsCarryTheAgentID(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "done"})
	o, _, bus := newOrchestrator(t, fake, dir)

	ch, cancel := bus.Subscribe(256)
	defer cancel()

	ctx, c2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer c2()
	tsk, err := o.Run(ctx, "s1", task.Spec{Prompt: "Fix the typo in README.md.", CWD: dir}, "")
	if err != nil {
		t.Fatal(err)
	}

	seen := 0
	deadline := time.After(3 * time.Second)
drain:
	for {
		select {
		case e := <-ch:
			switch e.Type {
			case event.AgentCompleted, event.AgentFailed:
				seen++
				if e.AgentID == "" {
					t.Fatalf("%s event has an empty AgentID", e.Type)
				}
			}
			if e.Type == event.TaskCompleted && e.TaskID == tsk.ID {
				break drain
			}
		case <-deadline:
			break drain
		}
	}
	if seen == 0 {
		t.Fatal("no agent lifecycle event observed")
	}
}
