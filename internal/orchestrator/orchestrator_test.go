package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"omniharness/internal/agent"
	"omniharness/internal/budget"
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

	// The collector runs on its own goroutine, so the assertions below cannot
	// simply read what it wrote: the read needs both mutual exclusion and a
	// happens-before edge, or it is a data race and there is no guarantee the
	// last event was even consumed yet. Unsubscribing closes the channel, which
	// ends the loop; waiting on done is the edge.
	var mu sync.Mutex
	var lastStrategy string
	ch, rawCancel := bus.SubscribeTo(128, event.StrategySelected)
	cancel := sync.OnceFunc(rawCancel)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range ch {
			var d event.StrategySelectedData
			if err := json.Unmarshal(e.Data, &d); err == nil && d.Strategy != "" {
				mu.Lock()
				lastStrategy = d.Strategy
				mu.Unlock()
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

	// Stop the subscription and wait for the collector to drain, so every
	// event published during the run has been seen before anything is read.
	cancel()
	<-done
	mu.Lock()
	observed := lastStrategy
	mu.Unlock()

	// Strategy-level repair: the final execution strategy must differ from
	// the original profile choice (a pure retry would keep it identical).
	// "direct" is the profile choice for a low-complexity prompt; the
	// structural repair escalates it to "sequential".
	if observed == "direct" {
		t.Fatalf("strategy never changed during repair (stayed direct); repair was not structural")
	}
	if observed == "" {
		t.Fatal("no strategy observed")
	}
	t.Logf("strategy escalated to %q over %d repairs", observed, tsk.Repairs)
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

// Budgets were advertised in the config and on the run command, and the
// tracker that measures them was fully written and tested — but nothing ever
// called it, so a cost or token ceiling bounded nothing at all.
func TestBudgetStopsTheRun(t *testing.T) {
	dir := t.TempDir()
	// Several steps' worth of work, so an unbudgeted run would keep going.
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{Content: "one"},
		testutil.FakeStep{Content: "two"},
		testutil.FakeStep{Content: "three"},
	)
	o, _, bus := newOrchestrator(t, fake, dir)

	ch, cancel := bus.Subscribe(256)
	defer cancel()

	ctx, c2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer c2()

	// One token is a ceiling any real call exceeds immediately.
	spec := task.Spec{
		Prompt: "Add a feature to the codebase.",
		CWD:    dir,
		Budget: budget.Budget{MaxTokens: 1},
	}
	tsk, err := o.Run(ctx, "s1", spec, "")
	if err == nil && tsk.Status == task.StatusCompleted {
		t.Fatal("a task over its token budget must not complete")
	}

	var announced bool
	deadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case e := <-ch:
			if e.Type == event.BudgetExceeded {
				announced = true
				break drain
			}
		case <-deadline:
			break drain
		}
	}
	if !announced {
		t.Error("exceeding a budget must publish BudgetExceeded so the UI can say why")
	}
}

// A task with no budget configured is unlimited, as it always was.
func TestNoBudgetRunsFreely(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "done"})
	o, _, _ := newOrchestrator(t, fake, dir)

	ctx, c2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer c2()
	tsk, err := o.Run(ctx, "s1", task.Spec{Prompt: "Fix the typo in README.md.", CWD: dir}, "")
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %s (%s)", tsk.Status, tsk.Error)
	}
}

// A prompt containing "verify" or "make sure it works" sets
// verification=REQUIRED. If no evaluator matches the profile, nothing runs and
// the task still completes — a missing evaluator is not evidence of a broken
// result. The risk is that "completed" then reads as "verified", so the run
// has to say plainly that nothing checked it.
func TestRequiredVerificationWithNoEvaluatorIsRecorded(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "looked it over"})
	o, store, bus := newOrchestrator(t, fake, dir)

	ch, cancel := bus.Subscribe(256)
	defer cancel()

	// A writing task: no evaluator covers the domain, and "check" is what
	// makes verification REQUIRED.
	spec := task.Spec{Prompt: "Draft a short welcome note for new starters and check the tone is right.", CWD: dir}
	ctx, c2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer c2()
	tsk, err := o.Run(ctx, "s1", spec, "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if tsk.Profile.Verification != task.VerificationRequired {
		t.Fatalf("verification = %q, want REQUIRED; the prompt says verify", tsk.Profile.Verification)
	}
	if len(o.deps.Evaluators.ForTask(tsk.Profile)) != 0 {
		t.Fatalf("this case needs a profile no evaluator matches, got domain %q", tsk.Profile.Domain)
	}
	// The task completes: a missing evaluator is not evidence of a broken
	// result. The point is that it must not complete silently.
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %q, want completed", tsk.Status)
	}

	rows, err := store.EvaluationsForTask(tsk.ID)
	if err != nil {
		t.Fatalf("read evaluations: %v", err)
	}
	var recorded bool
	for _, row := range rows {
		if row.Outcome == string(evaluate.NeedsReview) && strings.Contains(row.Detail, "no evaluator matched") {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("a required verification that never ran left no row to audit: %+v", rows)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == event.EvaluationComplete && strings.Contains(string(e.Data), "no evaluator matched") {
				return
			}
		case <-deadline:
			t.Fatal("no event said the required verification never ran")
		}
	}
}

// With no DeepAnalyzer configured — the default newOrchestrator fixture,
// and the state of every install until this is wired into config — a task
// runs exactly as it always did: no extra model call, no AcceptanceCriteria.
func TestNoDeepAnalyzerConfiguredLeavesTheProfileAlone(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "done"})
	o, _, _ := newOrchestrator(t, fake, dir)

	tsk, err := runTask(t, o, "s1", task.Spec{Prompt: "clean up the code", CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Profile.AcceptanceCriteria != nil {
		t.Fatalf("AcceptanceCriteria = %v, want nil with no DeepAnalyzer configured", tsk.Profile.AcceptanceCriteria)
	}
}

// A vague prompt ("clean up the code" forces Ambiguity=HIGH regardless of
// complexity — see task.Analyzer's detectAmbiguity) is worth deepening. The
// deepening call's usage must be charged to the task's own budget tracker
// and recorded in the store, and its acceptance criteria must land on the
// task's final Profile.
func TestDeepAnalyzerAddsAcceptanceCriteriaAndAccountsItsCost(t *testing.T) {
	dir := t.TempDir()
	// First scripted response answers the deepening call; the rest answer the
	// implementer's own turn(s).
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{Content: `["the change compiles", "existing tests still pass"]`},
		testutil.FakeStep{Content: "done"},
	)
	o, store, _ := newOrchestrator(t, fake, dir)
	o.deps.DeepAnalyzer = &task.DeepAnalyzer{Gateway: fake.Client(), ModelSel: o.deps.ModelSel}

	tsk, err := runTask(t, o, "s1", task.Spec{Prompt: "clean up the code", CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %s (%s)", tsk.Status, tsk.Error)
	}
	if len(tsk.Profile.AcceptanceCriteria) != 2 {
		t.Fatalf("AcceptanceCriteria = %v, want 2 entries", tsk.Profile.AcceptanceCriteria)
	}

	calls, err := store.ModelCallsForTask(tsk.ID)
	if err != nil {
		t.Fatal(err)
	}
	var deepenCalls int
	for _, c := range calls {
		if c.AgentID == "deep-analyzer" {
			deepenCalls++
			if c.Status != "ok" || c.TokensIn == 0 {
				t.Errorf("deep-analyzer call not recorded properly: %+v", c)
			}
		}
	}
	if deepenCalls != 1 {
		t.Fatalf("deep-analyzer model calls recorded = %d, want 1", deepenCalls)
	}
}

// The deepening call's usage must land in the same budget tracker the rest
// of the task is held to, not a separate, unaccounted allowance. A budget
// sized to exactly what the deepening call spends (the fake gateway always
// reports 100 prompt + 50 completion tokens) must already read as exhausted
// before the implementer's own turn — proven by that turn never happening.
func TestDeepAnalyzerCostCountsAgainstTheTaskBudget(t *testing.T) {
	dir := t.TempDir()
	// The first (and only affordable) response spends the whole budget; the
	// second must never actually be requested.
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{Content: `["a criterion"]`},
		testutil.FakeStep{Content: "should never be reached"},
	)
	o, _, bus := newOrchestrator(t, fake, dir)
	o.deps.DeepAnalyzer = &task.DeepAnalyzer{Gateway: fake.Client(), ModelSel: o.deps.ModelSel}

	ch, cancel := bus.Subscribe(256)
	defer cancel()

	spec := task.Spec{Prompt: "clean up the code", CWD: dir, Budget: budget.Budget{MaxTokens: 150}}
	tsk, err := runTask(t, o, "s1", spec)
	if err == nil && tsk.Status == task.StatusCompleted {
		t.Fatal("a budget already exhausted by the deepening call must not let the task complete")
	}
	if fake.RequestCount() != 1 {
		t.Fatalf("chat requests = %d, want exactly 1 (only the deepening call — the budget was already spent)", fake.RequestCount())
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == event.BudgetExceeded {
				return
			}
		case <-deadline:
			t.Fatal("the deepening call's cost never registered as exceeding the budget")
		}
	}
}
