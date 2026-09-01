// Package orchestrator executes a chosen strategy: it builds a step graph
// from the plan, schedules steps respecting dependencies and concurrency
// limits, runs policy-gated agents, evaluates results and drives the repair
// loop. Independent steps run concurrently; dependent steps wait.
package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"omniharness/internal/agent"
	"omniharness/internal/budget"
	composer "omniharness/internal/context"
	"omniharness/internal/evaluate"
	"omniharness/internal/event"
	"omniharness/internal/gateway"
	"omniharness/internal/id"
	"omniharness/internal/memory"
	"omniharness/internal/model"
	"omniharness/internal/policy"
	"omniharness/internal/repair"
	"omniharness/internal/session"
	"omniharness/internal/strategy"
	"omniharness/internal/task"
	"omniharness/internal/tools"
)

// Deps wires the orchestrator to every subsystem.
type Deps struct {
	Bus        *event.Bus
	Store      *session.Store
	Gateway    *gateway.Client
	ModelSel   *model.Selector
	Roles      map[agent.Role]agent.RoleConfig
	Evaluators *evaluate.Registry
	Repair     *repair.Engine
	Analyzer   *task.Analyzer
	Strategist strategy.Selector
	Tools      *tools.Registry
	Policy     *policy.Engine
	Composer   *composer.Composer
	Workspace  string
	// Advisor is performance memory; when non-nil its empirical records feed
	// strategy and model selection with explainable reasons.
	Advisor *memory.Advisor
	// MaxConcurrency caps simultaneous agents (default 4).
	MaxConcurrency int
	// MaxTaskRepairs caps task-level repair cycles (default 3).
	MaxTaskRepairs int
}

// Result is the outcome of task execution.
type Result struct {
	Task    *task.Task
	Outputs map[string]string
}

// Orchestrator runs tasks end to end.
type Orchestrator struct {
	deps Deps
	// cancel is set while a task is running to support graceful cancellation.
	cancelMu sync.Mutex
	cancel   context.CancelFunc
}

// New builds an orchestrator.
func New(deps Deps) *Orchestrator {
	if deps.MaxConcurrency <= 0 {
		deps.MaxConcurrency = 4
	}
	if deps.MaxTaskRepairs <= 0 {
		deps.MaxTaskRepairs = 3
	}
	return &Orchestrator{deps: deps}
}

// SetModelSelector swaps the model selection engine (used by the benchmark
// runner to pin the model under test).
func (o *Orchestrator) SetModelSelector(sel *model.Selector) {
	o.deps.ModelSel = sel
}

// Cancel requests graceful cancellation of the running task.
func (o *Orchestrator) Cancel() {
	o.cancelMu.Lock()
	if o.cancel != nil {
		o.cancel()
	}
	o.cancelMu.Unlock()
}

func (o *Orchestrator) taskEvent(t *task.Task, p event.Payload) {
	e := event.New(p)
	e.SessionID = t.SessionID
	e.TaskID = t.ID
	o.deps.Bus.Publish(e)
}

// agentEvent publishes an event the orchestrator raises on an agent's behalf,
// carrying that agent's id. Agent.publish stamps the id on the agent's own
// events, but the lifecycle events are raised out here — so without this they
// arrived with an empty AgentID and rendered as "agent[] failed", which in a
// run with several agents does not say which one.
func (o *Orchestrator) agentEvent(t *task.Task, agentID string, p event.Payload) {
	e := event.New(p)
	e.SessionID = t.SessionID
	e.TaskID = t.ID
	e.AgentID = agentID
	o.deps.Bus.Publish(e)
}

// Run executes a task from its spec. If taskID is non-empty the existing task
// record is reused (resume path); otherwise a new task is created.
func (o *Orchestrator) Run(ctx context.Context, sessionID string, spec task.Spec, taskID string) (*task.Task, error) {
	tsk, err := o.loadOrCreateTask(sessionID, spec, taskID)
	if err != nil {
		return nil, err
	}
	o.taskEvent(tsk, &event.TaskStateData{Status: task.StatusRunning, Message: "task started"})

	runCtx, cancel := context.WithCancel(ctx)
	o.cancelMu.Lock()
	o.cancel = cancel
	o.cancelMu.Unlock()
	defer func() {
		cancel()
		o.cancelMu.Lock()
		o.cancel = nil
		o.cancelMu.Unlock()
	}()

	if err := o.analyze(runCtx, tsk); err != nil {
		o.fail(tsk, err)
		return tsk, err
	}

	plan, err := o.selectStrategy(tsk)
	if err != nil {
		o.fail(tsk, err)
		return tsk, err
	}
	tsk.Strategy = string(plan.Strategy)
	_ = o.deps.Store.UpdateTask(tsk)
	o.taskEvent(tsk, &event.StrategySelectedData{
		Strategy: string(plan.Strategy), Reason: plan.Reason, Steps: stepNames(plan.Steps),
	})

	artifacts := &sync.Map{}
	// One tracker per task, shared by every agent working on it: the limits are
	// task-wide, not a per-agent allowance.
	budgets := budget.NewTracker(tsk.Spec.Budget)
	outputs, planErr := o.executePlan(runCtx, tsk, plan, artifacts, budgets)
	if runCtx.Err() != nil {
		tsk.Status = task.StatusCancelled
		tsk.Error = "cancelled"
		_ = o.deps.Store.UpdateTask(tsk)
		o.taskEvent(tsk, &event.TaskCancelledData{Status: task.StatusCancelled, Message: "task cancelled"})
		return tsk, context.Canceled
	}
	if planErr != nil {
		o.fail(tsk, planErr)
		return tsk, planErr
	}

	// Task-level verification + repair loop.
	if err := o.verifyAndRepair(runCtx, tsk, plan, outputs, artifacts, budgets); err != nil {
		if runCtx.Err() != nil {
			tsk.Status = task.StatusCancelled
			_ = o.deps.Store.UpdateTask(tsk)
			o.taskEvent(tsk, &event.TaskCancelledData{Status: task.StatusCancelled})
			return tsk, context.Canceled
		}
		return tsk, err
	}

	if tsk.Status == task.StatusCompleted && tsk.Result != nil {
		o.taskEvent(tsk, &event.TaskCompletedData{
			Summary: summarizeOutput(tsk.Result.Summary), Output: tsk.Result.Output,
			Artifacts: tsk.Result.Artifacts,
		})
	}
	return tsk, nil
}

func (o *Orchestrator) loadOrCreateTask(sessionID string, spec task.Spec, taskID string) (*task.Task, error) {
	if taskID != "" {
		tsk, err := o.deps.Store.GetTask(taskID)
		if err != nil {
			return nil, fmt.Errorf("load task %s: %w", taskID, err)
		}
		return tsk, nil
	}
	tsk := &task.Task{
		ID:        id.New(),
		SessionID: sessionID,
		Spec:      spec,
		Status:    task.StatusPending,
	}
	if err := o.deps.Store.CreateTask(tsk); err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	o.taskEvent(tsk, &event.TaskCreatedData{Prompt: spec.Prompt})
	return tsk, nil
}

func (o *Orchestrator) analyze(ctx context.Context, t *task.Task) error {
	if t.Profile.Complexity != "" {
		return nil // already analyzed (resume)
	}
	t.Profile = o.deps.Analyzer.Analyze(t.Spec)
	if err := o.deps.Store.UpdateTask(t); err != nil {
		return err
	}
	o.taskEvent(t, &event.TaskAnalyzedData{Profile: t.Profile})
	return nil
}

func (o *Orchestrator) selectStrategy(t *task.Task) (strategy.Plan, error) {
	in := strategy.Input{
		Profile: t.Profile,
		Budget:  t.Spec.Budget,
	}
	if o.deps.Advisor != nil {
		if h, err := o.deps.Advisor.StrategyPerformance(); err == nil {
			in.History = h
		}
	}
	return o.deps.Strategist.Select(in)
}

// executePlan runs all steps respecting dependencies, up to MaxConcurrency at
// a time. Artifacts produced by any agent are collected into the shared map.
func (o *Orchestrator) executePlan(ctx context.Context, t *task.Task, plan strategy.Plan, artifacts *sync.Map, budgets *budget.Tracker) (map[string]string, error) {
	outputs := map[string]string{}
	if len(plan.Steps) == 0 {
		return outputs, nil
	}

	var mu sync.Mutex
	remaining := map[string]int{}
	dependents := map[string][]string{}
	for _, s := range plan.Steps {
		remaining[s.ID] = len(s.Depends)
		for _, d := range s.Depends {
			dependents[d] = append(dependents[d], s.ID)
		}
	}
	var pending atomic.Int64
	pending.Store(int64(len(plan.Steps)))

	var firstErr error
	firstErrSet := false
	ready := make(chan strategy.Step)
	var wg sync.WaitGroup

	// Scheduler goroutine: emits ready steps until every step is done, an
	// error occurred, or the task is cancelled. Closing ready lets workers
	// drain and the WaitGroup finish.
	go func() {
		defer close(ready)
		for {
			if ctx.Err() != nil {
				return
			}
			mu.Lock()
			if pending.Load() == 0 || firstErrSet {
				mu.Unlock()
				return
			}
			var emit *strategy.Step
			for i := range plan.Steps {
				if remaining[plan.Steps[i].ID] == 0 {
					step := plan.Steps[i]
					emit = &step
					break
				}
			}
			if emit == nil {
				// Nothing ready yet: wait for a completion or cancel.
				mu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
				continue
			}
			remaining[emit.ID] = -1 // claimed
			mu.Unlock()
			select {
			case ready <- *emit:
			case <-ctx.Done():
				return
			}
		}
	}()

	for i := 0; i < o.deps.MaxConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for step := range ready {
				res := o.executeStep(ctx, t, step, artifacts, budgets)

				mu.Lock()
				pending.Add(-1)
				if res.Err != nil && !firstErrSet {
					firstErr = res.Err
					firstErrSet = true
				} else if res.Err == nil {
					outputs[res.ID] = res.Output
				}
				for _, dep := range dependents[res.ID] {
					if remaining[dep] > 0 {
						remaining[dep]--
					}
				}
				mu.Unlock()
			}
		}()
	}

	waitDone := make(chan struct{})
	go func() { wg.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-ctx.Done():
	}
	return outputs, firstErr
}

// stepResult wraps executeStep output.
type stepResult struct {
	ID     string
	Output string
	Err    error
}

// executeStep runs one plan step with a single agent and a per-step repair
// loop. Repairs change variables (role, model capability, instructions) — the
// exact failed execution is never blindly repeated.
func (o *Orchestrator) executeStep(ctx context.Context, t *task.Task, step strategy.Step, artifacts *sync.Map, budgets *budget.Tracker) stepResult {
	role := agent.Role(step.Role)
	if role == "" {
		role = agent.RoleImplementer
	}
	modelRef := ""
	extra := ""
	maxAttempts := o.deps.Repair.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return stepResult{ID: step.ID, Err: ctx.Err()}
		}
		ag, err := o.runAgent(ctx, t, role, modelRef, extra, step.Task, artifacts, budgets)
		if err == nil {
			return stepResult{ID: step.ID, Output: ag.LastOutput()}
		}
		failure := repair.Classify(repair.StageModel, err)
		failure.Attempt = attempt + 1
		plan, perr := o.deps.Repair.Plan(failure, attempt+1, maxAttempts)
		if perr != nil {
			return stepResult{ID: step.ID, Err: err}
		}
		if plan.SkipRepair {
			return stepResult{ID: step.ID, Err: err}
		}
		if plan.Role != "" {
			role = agent.Role(plan.Role)
		}
		if plan.ModelCapability != "" {
			if m, err := o.deps.ModelSel.Resolve(model.Intent{Capabilities: []string{plan.ModelCapability}}); err == nil {
				modelRef = m
			}
		}
		extra = strings.TrimSpace(extra + " " + plan.ExtraInstructions)
		t.Repairs++
		_ = o.deps.Store.UpdateTask(t)
		o.taskEvent(t, &event.RepairData{
			Attempt: attempt + 1, Strategy: plan.Strategy, Reason: failure.Error, Changed: plan.Changed,
		})
	}
	return stepResult{ID: step.ID, Err: fmt.Errorf("step %s exceeded repair limit", step.ID)}
}

// runAgent creates and runs one agent for the task.
func (o *Orchestrator) runAgent(ctx context.Context, t *task.Task, role agent.Role, modelRef, extra, stepTask string, artifacts *sync.Map, budgets *budget.Tracker) (*agent.Agent, error) {
	spec := t.Spec
	if stepTask != "" && stepTask != "execute the task" && !strings.HasPrefix(stepTask, "step ") {
		spec.Prompt = spec.Prompt + "\n\n[This step] " + stepTask
	}
	if extra != "" {
		spec.Prompt = spec.Prompt + "\n\n[Repair instructions] " + extra
	}
	deps := agent.Deps{
		Bus:       o.deps.Bus,
		Store:     o.deps.Store,
		Gateway:   o.deps.Gateway,
		ModelSel:  o.deps.ModelSel,
		Tools:     o.deps.Tools,
		Policy:    o.deps.Policy,
		Composer:  o.deps.Composer,
		Roles:     o.deps.Roles,
		Workspace: o.deps.Workspace,
		Budget:    budgets,
	}
	ag := agent.New(deps, t.SessionID, t.ID, role, modelRef, spec, t.Profile)
	if err := ag.Run(ctx); err != nil {
		o.agentEvent(t, ag.ID, &event.AgentFailedData{Role: string(role), Status: task.StatusFailed, Model: ag.Model, Error: err.Error()})
		return ag, err
	}
	o.agentEvent(t, ag.ID, &event.AgentCompletedData{Role: string(role), Status: task.StatusCompleted, Model: ag.Model,
		Tokens: ag.TokensIn + ag.TokensOut, CostUSD: ag.CostUSD, Latency: ag.Latency})
	for _, p := range ag.ArtifactPaths() {
		artifacts.Store(p, true)
	}
	return ag, nil
}

// verifyAndRepair runs evaluators and drives the task-level repair loop. The
// task may be re-run up to MaxTaskRepairs times with changed variables before
// the failure is terminal.
func (o *Orchestrator) verifyAndRepair(ctx context.Context, t *task.Task, plan strategy.Plan, outputs map[string]string, artifacts *sync.Map, budgets *budget.Tracker) error {
	final := o.finalOutput(plan, outputs)
	t.Result = &task.Result{Summary: final, Output: final, Artifacts: artifactList(artifacts)}

	for cycle := 1; ; cycle++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		outcome, detail := o.evaluate(ctx, t)
		if outcome == evaluate.Pass || outcome == evaluate.PassWithWarnings || outcome == evaluate.NeedsReview {
			t.Status = task.StatusCompleted
			_ = o.deps.Store.UpdateTask(t)
			return nil
		}
		if cycle > o.deps.MaxTaskRepairs {
			t.Status = task.StatusFailed
			t.Error = fmt.Sprintf("verification failed after %d repair cycles: %s", o.deps.MaxTaskRepairs, detail)
			_ = o.deps.Store.UpdateTask(t)
			o.taskEvent(t, &event.TaskFailedData{Status: task.StatusFailed, Error: t.Error})
			return fmt.Errorf("%s", t.Error)
		}
		// Classify the verification detail so repair picks the right strategy:
		// build/test failures get the debugger sequence; generic verification
		// failures escalate.
		kind := "evaluate"
		lower := strings.ToLower(detail)
		switch {
		case strings.Contains(lower, "build failed"):
			kind = "build"
		case strings.Contains(lower, "tests failed"):
			kind = "test"
		}
		failure := repair.Failure{Stage: repair.StageEvaluate, Kind: kind, Error: detail, Attempt: cycle, Strategy: t.Strategy}
		// The loop's own counter decides when to give up (after MaxTaskRepairs
		// re-runs); the planner's limit is set one higher so it never pre-empts.
		rplan, err := o.deps.Repair.Plan(failure, cycle, o.deps.MaxTaskRepairs+1)
		if err != nil {
			return err
		}
		if rplan.SkipRepair {
			t.Status = task.StatusFailed
			t.Error = fmt.Sprintf("verification failed: %s", detail)
			_ = o.deps.Store.UpdateTask(t)
			o.taskEvent(t, &event.TaskFailedData{Status: task.StatusFailed, Error: t.Error})
			return fmt.Errorf("%s", t.Error)
		}
		t.Repairs++
		_ = o.deps.Store.UpdateTask(t)
		o.taskEvent(t, &event.RepairData{
			Attempt: cycle, Strategy: rplan.Strategy, Reason: detail, Changed: rplan.Changed,
		})
		// Re-select the strategy: profile + performance memory, then apply any
		// structural override the repair plan demanded.
		selPlan, err := o.selectStrategy(t)
		if err != nil {
			return err
		}
		if rplan.ExecutionStrategy != "" {
			selPlan.Strategy = strategy.Strategy(rplan.ExecutionStrategy)
			selPlan.Reason = fmt.Sprintf("repair cycle %d restructures execution to %s (%s)", cycle, rplan.ExecutionStrategy, strings.Join(rplan.Changed, "; "))
			selPlan.Steps = strategy.StepsFor(t.Profile, selPlan.Strategy)
		}
		t.Strategy = string(selPlan.Strategy)
		_ = o.deps.Store.UpdateTask(t)
		// Strategy-level repair must be observable: the re-selection is
		// published like the initial selection, with the repair reason.
		o.taskEvent(t, &event.StrategySelectedData{
			Strategy: string(selPlan.Strategy), Reason: selPlan.Reason, Steps: stepNames(selPlan.Steps),
		})
		outputs, err = o.executePlan(ctx, t, selPlan, artifacts, budgets)
		if err != nil {
			continue
		}
		final = o.finalOutput(selPlan, outputs)
		t.Result = &task.Result{Summary: final, Output: final, Artifacts: artifactList(artifacts)}
	}
}

// evaluate runs the evaluators selected for the task and records outcomes.
// The run context propagates so cancellation reaches long-running evaluator
// commands (go build/test) instead of leaving them to finish in the
// background.
func (o *Orchestrator) evaluate(ctx context.Context, t *task.Task) (evaluate.Outcome, string) {
	evs := o.deps.Evaluators.ForTask(t.Profile)
	if len(evs) == 0 {
		if t.Profile.Verification == task.VerificationRequired {
			return evaluate.NeedsReview, "verification required but no evaluator matched"
		}
		return evaluate.Pass, "no evaluator applicable"
	}
	worst := evaluate.Pass
	var details []string
	for _, ev := range evs {
		o.taskEvent(t, &event.EvaluationData{Evaluator: ev.Name(), Outcome: "started"})
		outcome, detail, err := ev.Evaluate(ctx, evaluate.Request{
			Task: *t, Result: *t.Result, CWD: o.deps.Workspace,
		})
		if err != nil {
			detail = "evaluator error: " + err.Error()
		}
		if severity(outcome) > severity(worst) {
			worst = outcome
		}
		details = append(details, fmt.Sprintf("%s: %s", ev.Name(), detail))
		_ = o.deps.Store.RecordEvaluation(&session.Evaluation{
			SessionID: t.SessionID, TaskID: t.ID, Evaluator: ev.Name(), Outcome: string(outcome), Detail: detail,
		})
		o.taskEvent(t, &event.EvaluationCompletedData{Evaluator: ev.Name(), Outcome: string(outcome), Detail: detail})
	}
	return worst, strings.Join(details, "\n")
}

func severity(o evaluate.Outcome) int {
	switch o {
	case evaluate.Pass:
		return 0
	case evaluate.PassWithWarnings:
		return 1
	case evaluate.NeedsReview:
		return 2
	default:
		return 3
	}
}

// finalOutput picks the deliverable: the output of the last step in plan
// order that produced output.
func (o *Orchestrator) finalOutput(plan strategy.Plan, outputs map[string]string) string {
	for i := len(plan.Steps) - 1; i >= 0; i-- {
		if out, ok := outputs[plan.Steps[i].ID]; ok && strings.TrimSpace(out) != "" {
			return out
		}
	}
	// Fall back to any output.
	for _, out := range outputs {
		if strings.TrimSpace(out) != "" {
			return out
		}
	}
	return ""
}

func artifactList(m *sync.Map) []string {
	var out []string
	m.Range(func(k, _ any) bool {
		out = append(out, k.(string))
		return true
	})
	sort.Strings(out)
	return out
}

func (o *Orchestrator) fail(t *task.Task, err error) {
	t.Status = task.StatusFailed
	t.Error = err.Error()
	_ = o.deps.Store.UpdateTask(t)
	o.taskEvent(t, &event.TaskFailedData{Status: task.StatusFailed, Error: err.Error()})
}

func stepNames(steps []strategy.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		name := s.ID
		if s.Role != "" {
			name += "(" + s.Role + ")"
		}
		out = append(out, name)
	}
	return out
}

func summarizeOutput(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 300 {
		return s
	}
	return s[:300] + "…"
}
