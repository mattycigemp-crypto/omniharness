// Package agent implements the agent runtime: capability-driven roles, a
// lifecycle state machine, and the execution loop that composes context,
// calls models through OmniRoute, executes policy-gated tools, observes
// results and decides whether to continue.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"omniharness/internal/budget"
	composer "omniharness/internal/context"
	"omniharness/internal/event"
	"omniharness/internal/gateway"
	"omniharness/internal/id"
	"omniharness/internal/model"
	"omniharness/internal/policy"
	"omniharness/internal/session"
	"omniharness/internal/task"
	"omniharness/internal/tools"
)

// Role identifies a capability-driven agent role. Roles are capabilities, not
// personalities: the same runtime drives any role.
type Role string

const (
	RoleArchitect       Role = "architect"
	RoleImplementer     Role = "implementer"
	RoleResearcher      Role = "researcher"
	RoleDebugger        Role = "debugger"
	RoleReviewer        Role = "reviewer"
	RoleTester          Role = "tester"
	RoleSecurityAuditor Role = "security-auditor"
	RoleOptimizer       Role = "optimizer"
	RoleSynthesizer     Role = "synthesizer"
)

// AllRoles lists every role.
func AllRoles() []Role {
	return []Role{RoleArchitect, RoleImplementer, RoleResearcher, RoleDebugger, RoleReviewer,
		RoleTester, RoleSecurityAuditor, RoleOptimizer, RoleSynthesizer}
}

// RoleConfig declares a role's system prompt, model intent and tool policy.
type RoleConfig struct {
	Role        Role
	Prompt      string
	ModelIntent model.Intent
	ToolAllow   []string // empty = use registry policy only
}

// DefaultRoles returns the built-in role definitions.
func DefaultRoles() map[Role]RoleConfig {
	return map[Role]RoleConfig{
		RoleArchitect: {
			Role:        RoleArchitect,
			Prompt:      "You are the architect. Produce precise designs, plans and decomposition. Be concrete: name files, functions and interfaces. Prefer reading before writing.",
			ModelIntent: model.Intent{Capabilities: []string{model.CapReasoning, model.CapCoding}},
			ToolAllow:   []string{"read_file", "list_dir", "find_files", "search", "git", "remember", "request_replan"},
		},
		RoleImplementer: {
			Role:        RoleImplementer,
			Prompt:      "You are the implementer. Make minimal, correct changes. Prefer editing existing files. Run the provided tools to inspect before you modify. Keep changes focused on the task.",
			ModelIntent: model.Intent{Capabilities: []string{model.CapCoding, model.CapFast}},
			ToolAllow:   []string{"read_file", "write_file", "edit_file", "list_dir", "find_files", "search", "shell", "git", "remember", "request_replan"},
		},
		RoleResearcher: {
			Role:        RoleResearcher,
			Prompt:      "You are the researcher. Gather evidence and sources. Report findings with citations and note uncertainty explicitly. Do not fabricate sources.",
			ModelIntent: model.Intent{Capabilities: []string{model.CapResearch, model.CapReasoning}},
			ToolAllow:   []string{"read_file", "list_dir", "find_files", "search", "shell", "git", "remember", "request_replan"},
		},
		RoleDebugger: {
			Role:        RoleDebugger,
			Prompt:      "You are the debugger. Reproduce the failure first, then isolate root cause with the smallest possible experiment. Report the root cause and the fix.",
			ModelIntent: model.Intent{Capabilities: []string{model.CapReasoning, model.CapCoding}},
			ToolAllow:   []string{"read_file", "search", "shell", "git", "remember", "request_replan"},
		},
		RoleReviewer: {
			Role:        RoleReviewer,
			Prompt:      "You are the reviewer. Check correctness, safety, and adherence to the task. Identify concrete defects with file/line references. Be skeptical; do not rubber-stamp.",
			ModelIntent: model.Intent{Capabilities: []string{model.CapReview, model.CapReasoning}},
			ToolAllow:   []string{"read_file", "list_dir", "find_files", "search", "shell", "git", "remember", "request_replan"},
		},
		RoleTester: {
			Role:        RoleTester,
			Prompt:      "You are the tester. Write and run tests that prove the behavior described in the task. Report pass/fail per test.",
			ModelIntent: model.Intent{Capabilities: []string{model.CapCoding, model.CapFast}},
			ToolAllow:   []string{"read_file", "write_file", "edit_file", "search", "shell", "git", "remember", "request_replan"},
		},
		RoleSecurityAuditor: {
			Role:        RoleSecurityAuditor,
			Prompt:      "You are the security auditor. Look for injection, secrets, unsafe file/shell operations, and privilege issues. Report severity and concrete fixes.",
			ModelIntent: model.Intent{Capabilities: []string{model.CapReasoning, model.CapReview}},
			ToolAllow:   []string{"read_file", "search", "git", "remember", "request_replan"},
		},
		RoleOptimizer: {
			Role:        RoleOptimizer,
			Prompt:      "You are the optimizer. Improve performance without changing observable behavior. Measure before and after.",
			ModelIntent: model.Intent{Capabilities: []string{model.CapCoding, model.CapReasoning}},
			ToolAllow:   []string{"read_file", "edit_file", "search", "shell", "git", "remember", "request_replan"},
		},
		RoleSynthesizer: {
			Role:        RoleSynthesizer,
			Prompt:      "You are the synthesizer. Combine the collected results into one coherent deliverable. Integrate, reconcile conflicts, and produce the final answer.",
			ModelIntent: model.Intent{Capabilities: []string{model.CapReasoning, model.CapCoding}},
			ToolAllow:   []string{"read_file", "search", "remember", "request_replan"},
		},
	}
}

// Lifecycle is the agent state machine.
type Lifecycle string

const (
	LifecycleCreated   Lifecycle = "created"
	LifecycleReady     Lifecycle = "ready"
	LifecycleThinking  Lifecycle = "thinking"
	LifecycleActing    Lifecycle = "acting"
	LifecycleObserving Lifecycle = "observing"
	LifecyclePaused    Lifecycle = "paused"
	LifecycleCompleted Lifecycle = "completed"
	LifecycleFailed    Lifecycle = "failed"
	LifecycleCancelled Lifecycle = "cancelled"
)

// Deps are the shared runtime dependencies of every agent.
type Deps struct {
	Bus           *event.Bus
	Store         *session.Store
	Gateway       *gateway.Client
	ModelSel      *model.Selector
	Tools         *tools.Registry
	Policy        *policy.Engine
	Composer      *composer.Composer
	Roles         map[Role]RoleConfig
	Workspace     string
	MaxIterations int // tool-call loop iterations per agent (0 = 100)
	// ProjectInstructions are durable notes recalled from project memory
	// (see memory.ProjectMemories) — conventions, gotchas, decisions an
	// earlier task remembered about this workspace. Composed into every
	// model call's system prompt; nil means nothing has been remembered yet.
	ProjectInstructions []string
	// Budget bounds the task's total consumption. It is shared by every agent
	// working on the task, because the limits are task-wide: "total tokens
	// across all agents" is not a per-agent allowance. Nil means unlimited.
	Budget *budget.Tracker
	// OnApprovalRequest is invoked when a tool needs human approval. When nil,
	// policy-engine approvals run through its own approver.
	OnApprovalRequest func(a *Agent, r policy.Request, reason string)
}

// Agent is a single runnable agent.
type Agent struct {
	ID          string
	SessionID   string
	TaskID      string
	Role        Role
	Model       string // resolved provider/model
	ModelReason string // why this model was chosen
	Status      task.Status
	Lifecycle   Lifecycle
	Action      string // human-readable current action

	Transcript []gateway.Message
	TokensIn   int64
	TokensOut  int64
	CostUSD    float64
	Latency    time.Duration
	ToolCalls  int

	Spec    task.Spec
	Profile task.Profile
	Summary string // running condensation summary
	// Artifacts are paths produced by artifact-marking tools.
	Artifacts []string
	// replanReason is set when a replan-marking tool call (request_replan)
	// runs. Read via ReplanReason(); empty means nothing was requested.
	replanReason string
	// repeats guards against the model spinning on one identical tool call;
	// see stall.go. Reset at the start of every Run.
	repeats repeatTracker

	deps   Deps
	mu     sync.Mutex
	cancel context.CancelFunc
	// paused + resume implement pause/resume without closed-channel traps:
	// resume is closed exactly once when a paused agent is resumed.
	paused bool
	resume chan struct{}
	ctx    context.Context
}

// New creates an agent in the Created lifecycle state.
func New(deps Deps, sessionID, taskID string, role Role, modelRef string, spec task.Spec, profile task.Profile) *Agent {
	return &Agent{
		ID:        id.New(),
		SessionID: sessionID,
		TaskID:    taskID,
		Role:      role,
		Model:     modelRef,
		Status:    task.StatusPending,
		Lifecycle: LifecycleCreated,
		Spec:      spec,
		Profile:   profile,
		deps:      deps,
	}
}

// Deps exposes the agent's dependencies (used by the orchestrator).
func (a *Agent) Deps() Deps { return a.deps }

// ResolveModel picks the concrete provider/model and records the reason for
// the choice. An explicitly provided ref (e.g. from a repair plan) wins;
// otherwise the role's model intent is resolved through the selector, which
// consults performance memory when available.
func (a *Agent) ResolveModel() error {
	if a.Model != "" {
		a.ModelReason = "explicit model reference"
		return nil
	}
	roleCfg, ok := a.deps.Roles[a.Role]
	if !ok {
		return fmt.Errorf("unknown role %q", a.Role)
	}
	m, reason, err := a.deps.ModelSel.ResolveExplain(roleCfg.ModelIntent)
	if err != nil {
		return err
	}
	a.Model = m
	a.ModelReason = reason
	return nil
}

// overBudget reports the exceeded dimension, announcing it once so the CLI and
// TUI can show why a run stopped. Returns "" when there is no budget or the
// task is still inside it.
func (a *Agent) overBudget() string {
	if a.deps.Budget == nil {
		return ""
	}
	reason := a.deps.Budget.Exceeded()
	if reason == "" {
		return ""
	}
	a.publish(&event.BudgetExceededData{Dimension: reason, TaskID: a.TaskID})
	return reason
}

func (a *Agent) publish(p event.Payload) {
	e := event.New(p)
	e.SessionID = a.SessionID
	e.TaskID = a.TaskID
	e.AgentID = a.ID
	a.deps.Bus.Publish(e)
}

func (a *Agent) setLifecycle(l Lifecycle, status task.Status, msg string) {
	// Tokens/cost/latency are written concurrently by callModel; read them
	// under the lock to avoid a data race.
	a.mu.Lock()
	a.Lifecycle = l
	a.Status = status
	a.Action = msg
	tokens := a.TokensIn + a.TokensOut
	cost := a.CostUSD
	latency := a.Latency
	a.mu.Unlock()
	a.publish(&event.AgentStateData{
		Role: string(a.Role), Status: status, Model: a.Model, Action: msg,
		Tokens: tokens, CostUSD: cost, Latency: latency,
	})
}

// Pause suspends the agent at its next checkpoint.
func (a *Agent) Pause() {
	a.mu.Lock()
	if !a.paused {
		a.paused = true
		a.resume = make(chan struct{})
	}
	a.mu.Unlock()
	a.setLifecycle(LifecyclePaused, task.StatusPaused, "paused")
}

// Resume continues a paused agent.
func (a *Agent) Resume() {
	a.mu.Lock()
	if a.paused {
		a.paused = false
		close(a.resume)
	}
	a.mu.Unlock()
	a.setLifecycle(LifecycleThinking, task.StatusRunning, "resumed")
}

// Cancel requests graceful termination.
func (a *Agent) Cancel() {
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Unlock()
	a.setLifecycle(LifecycleCancelled, task.StatusCancelled, "cancelled")
}

// Persist writes the agent's durable state (transcript included) to the store.
func (a *Agent) Persist() error {
	a.mu.Lock()
	transcript, err := json.Marshal(a.Transcript)
	status := string(a.Status)
	model := a.Model
	role := string(a.Role)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	return a.deps.Store.UpsertAgent(&session.AgentRecord{
		ID: a.ID, SessionID: a.SessionID, TaskID: a.TaskID, Role: role,
		Model: model, Status: status, Transcript: transcript,
	})
}

// TranscriptJSON returns the persisted JSON form of the transcript.
func (a *Agent) TranscriptJSON() []byte {
	b, _ := json.Marshal(a.Transcript)
	return b
}

// SetTranscript restores a transcript (used when resuming).
func (a *Agent) SetTranscript(messages []gateway.Message) {
	a.Transcript = messages
}

// Run executes the agent loop until completion, cancellation or failure.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.ResolveModel(); err != nil {
		a.setLifecycle(LifecycleFailed, task.StatusFailed, err.Error())
		return err
	}
	if a.deps.MaxIterations <= 0 {
		a.deps.MaxIterations = 100
	}

	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.ctx = runCtx
	a.cancel = cancel
	a.mu.Unlock()
	defer cancel()

	a.setLifecycle(LifecycleReady, task.StatusRunning, "ready")
	a.publish(&event.AgentCreatedData{Role: string(a.Role), Model: a.Model, TaskID: a.TaskID, SessionID: a.SessionID})
	if err := a.Persist(); err != nil {
		return fmt.Errorf("persist agent: %w", err)
	}
	a.setLifecycle(LifecycleThinking, task.StatusRunning, "thinking")

	roleCfg := a.deps.Roles[a.Role]
	toolSpecs := a.toolSpecs(roleCfg.ToolAllow)
	a.repeats = repeatTracker{}

	for iter := 0; iter < a.deps.MaxIterations; iter++ {
		// Pause checkpoint: block until resumed or cancelled.
		a.mu.Lock()
		paused := a.paused
		resume := a.resume
		a.mu.Unlock()
		if paused {
			select {
			case <-runCtx.Done():
				a.setLifecycle(LifecycleCancelled, task.StatusCancelled, "cancelled")
				return context.Canceled
			case <-resume:
			}
		}
		select {
		case <-runCtx.Done():
			a.setLifecycle(LifecycleCancelled, task.StatusCancelled, "cancelled")
			return context.Canceled
		default:
		}

		// Stop before spending anything more. Checked at the top of each
		// iteration so the ceiling bounds what is spent, rather than being
		// noticed after the fact.
		if reason := a.overBudget(); reason != "" {
			a.setLifecycle(LifecycleFailed, task.StatusFailed, reason)
			return fmt.Errorf("%s", reason)
		}

		// Checkpoint the transcript each iteration for resumability.
		if err := a.Persist(); err != nil {
			return fmt.Errorf("persist agent: %w", err)
		}
		a.publish(&event.AgentTranscriptData{Messages: len(a.Transcript)})

		resp, err := a.callModel(runCtx, toolSpecs, roleCfg)
		if err != nil {
			if runCtx.Err() != nil {
				a.setLifecycle(LifecycleCancelled, task.StatusCancelled, "cancelled")
				return context.Canceled
			}
			a.setLifecycle(LifecycleFailed, task.StatusFailed, err.Error())
			return err
		}
		if resp == nil {
			continue
		}
		// Check again now the call has been paid for. Checking only before the
		// next iteration would let a single-turn agent blow any ceiling and
		// still report success, because it never comes round again.
		if reason := a.overBudget(); reason != "" {
			a.setLifecycle(LifecycleFailed, task.StatusFailed, reason)
			return fmt.Errorf("%s", reason)
		}

		msg := resp.Choices[0].Message
		if msg.Content != "" {
			a.Summary = composer.Summarize([]composer.Message{{Role: "assistant", Content: msg.Content}}, 1500)
		}
		a.setLifecycle(LifecycleThinking, task.StatusRunning, "thinking")

		if len(msg.ToolCalls) == 0 {
			// Final answer.
			if msg.Content != "" {
				a.Transcript = append(a.Transcript, msg)
			}
			a.setLifecycle(LifecycleCompleted, task.StatusCompleted, "completed")
			if err := a.Persist(); err != nil {
				return err
			}
			return nil
		}

		// The assistant message carrying the tool_calls MUST precede the tool
		// results in the transcript: OpenAI wire format rejects tool messages
		// without a matching assistant tool_calls message, and the model needs
		// the history to continue coherently.
		a.Transcript = append(a.Transcript, msg)

		// Execute tool calls.
		a.setLifecycle(LifecycleActing, task.StatusRunning, "acting")
		for _, tc := range msg.ToolCalls {
			if reason := a.overBudget(); reason != "" {
				a.setLifecycle(LifecycleFailed, task.StatusFailed, reason)
				return fmt.Errorf("%s", reason)
			}
			a.ToolCalls++
			if a.deps.Budget != nil {
				a.deps.Budget.AddToolCall()
			}
			nudge, stalled := a.repeats.observe(tc)
			if stalled {
				reason := a.repeats.reason(tc.Function.Name)
				a.setLifecycle(LifecycleFailed, task.StatusFailed, reason)
				return fmt.Errorf("%s", reason)
			}
			obs := nudge
			if obs == "" {
				obs = a.executeToolCall(runCtx, tc, roleCfg)
			}
			if runCtx.Err() != nil {
				a.setLifecycle(LifecycleCancelled, task.StatusCancelled, "cancelled")
				return context.Canceled
			}
			a.Transcript = append(a.Transcript, gateway.Message{
				Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: obs,
			})
			a.publish(&event.ObservationCreatedData{
				Tool: tc.Function.Name, AgentID: a.ID, Summary: truncate(obs, 200), OutputLen: len(obs),
			})
		}
		a.setLifecycle(LifecycleObserving, task.StatusRunning, "observing")
	}

	return fmt.Errorf("agent exceeded %d iterations", a.deps.MaxIterations)
}

// callModel composes context and performs one model call.
func (a *Agent) callModel(ctx context.Context, toolSpecs []gateway.ToolSpec, roleCfg RoleConfig) (*gateway.ChatResponse, error) {
	in := composer.Input{
		Spec:                a.Spec,
		Profile:             a.Profile,
		SystemPrompt:        roleCfg.Prompt,
		ProjectInstructions: a.deps.ProjectInstructions,
		History:             toContextMessages(a.Transcript),
		Summary:             a.Summary,
	}
	out, err := a.deps.Composer.Compose(in)
	if err != nil {
		return nil, err
	}
	if out.Condensed {
		a.publish(&event.ContextData{Reason: "history condensed at token limit"})
	}

	a.setLifecycle(LifecycleThinking, task.StatusRunning, "thinking")
	a.publish(&event.ModelRequestedData{Model: a.Model, TaskID: a.TaskID, AgentID: a.ID, Stream: false, Reason: a.ModelReason})

	start := time.Now()
	req := gateway.ChatRequest{
		Model:    a.Model,
		Messages: toGatewayMessages(out.Messages),
		Tools:    toolSpecs,
	}
	resp, err := a.deps.Gateway.Chat(ctx, req)
	latency := time.Since(start)
	if err != nil {
		a.publish(&event.ModelFailedData{Model: a.Model, TaskID: a.TaskID, AgentID: a.ID, Error: err.Error()})
		_ = a.recordModelCall(req, nil, latency, err)
		return nil, err
	}

	usage := resp.Usage
	cost := model.EstimateCost(a.Model, usage.PromptTokens, usage.CompletionTokens)
	a.mu.Lock()
	a.TokensIn += usage.PromptTokens
	a.TokensOut += usage.CompletionTokens
	a.CostUSD += cost
	a.Latency += latency
	a.mu.Unlock()
	if a.deps.Budget != nil {
		a.deps.Budget.AddTokens(usage.PromptTokens+usage.CompletionTokens, cost)
	}
	a.publish(&event.ModelRespondedData{
		Model: a.Model, TaskID: a.TaskID, AgentID: a.ID,
		TokensIn: usage.PromptTokens, TokensOut: usage.CompletionTokens, CostUSD: cost, Latency: latency,
	})
	_ = a.recordModelCall(req, resp, latency, nil)
	return resp, nil
}

func (a *Agent) recordModelCall(req gateway.ChatRequest, resp *gateway.ChatResponse, latency time.Duration, err error) error {
	status := "ok"
	errMsg := ""
	if err != nil {
		status = "failed"
		errMsg = err.Error()
	}
	var in, out int64
	if resp != nil {
		in, out = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	provider, _ := gateway.SplitModel(a.Model)
	return a.deps.Store.RecordModelCall(&session.ModelCall{
		SessionID: a.SessionID, TaskID: a.TaskID, AgentID: a.ID,
		Provider: provider, Model: a.Model, TokensIn: in, TokensOut: out,
		CostUSD: model.EstimateCost(a.Model, in, out), LatencyMS: latency.Milliseconds(),
		Status: status, Error: errMsg,
	})
}

// executeToolCall runs one tool call through policy and returns the
// observation string fed back to the model.
func (a *Agent) executeToolCall(ctx context.Context, tc gateway.ToolCall, roleCfg RoleConfig) string {
	name := tc.Function.Name
	args, err := tools.DecodeArgs(tc.Function.Arguments)
	if err != nil {
		return "tool arguments error: " + err.Error()
	}

	tool, ok := a.deps.Tools.Get(name)
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", name)
	}
	spec := tool.Spec()

	// Role tool allowlist.
	if len(roleCfg.ToolAllow) > 0 {
		allowed := false
		for _, t := range roleCfg.ToolAllow {
			if t == name {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Sprintf("error: tool %q is not allowed for role %s", name, a.Role)
		}
	}

	a.publish(&event.ToolRequestedData{
		Tool: name, Input: truncate(tc.Function.Arguments, 200), Risk: string(spec.Risk), AgentID: a.ID,
	})

	req := policy.Request{Tool: name, Input: args, Risk: spec.Risk, AgentID: a.ID}
	decision, err := a.deps.Policy.EvaluateAndExecute(ctx, req)
	if err != nil {
		a.publish(&event.ToolFinishedData{Tool: name, AgentID: a.ID, Status: "denied", Error: err.Error()})
		_ = a.recordToolCall(name, "denied", spec.Risk, 0, err.Error())
		return fmt.Sprintf("tool %s was denied by policy: %v", name, err)
	}
	if decision != policy.Allow {
		a.publish(&event.ToolFinishedData{Tool: name, AgentID: a.ID, Status: "denied"})
		_ = a.recordToolCall(name, "denied", spec.Risk, 0, "denied by policy")
		return fmt.Sprintf("tool %s was denied by policy", name)
	}

	a.publish(&event.ToolStartedData{Tool: name, AgentID: a.ID})
	a.setLifecycle(LifecycleActing, task.StatusRunning, "tool: "+name)
	start := time.Now()
	result, runErr := tool.Run(ctx, args)
	duration := time.Since(start)

	if result.Artifact {
		if p, ok := args["path"].(string); ok {
			a.mu.Lock()
			a.Artifacts = append(a.Artifacts, p)
			a.mu.Unlock()
		}
	}
	if result.Replan {
		a.mu.Lock()
		if a.replanReason == "" {
			a.replanReason = result.Output
		}
		a.mu.Unlock()
	}

	if runErr != nil {
		a.publish(&event.ToolFinishedData{Tool: name, AgentID: a.ID, Status: "failed", Duration: duration, Error: runErr.Error()})
		_ = a.recordToolCall(name, "failed", spec.Risk, duration.Milliseconds(), runErr.Error())
		return fmt.Sprintf("tool %s failed: %v\n%s", name, runErr, truncate(result.Output, 2000))
	}
	a.publish(&event.ToolFinishedData{Tool: name, AgentID: a.ID, Status: "completed", Duration: duration, OutputLen: len(result.Output)})
	_ = a.recordToolCall(name, "completed", spec.Risk, duration.Milliseconds(), "")
	return result.Output
}

func (a *Agent) recordToolCall(name, status string, risk tools.Risk, durationMS int64, errMsg string) error {
	return a.deps.Store.RecordToolCall(&session.ToolCall{
		SessionID: a.SessionID, TaskID: a.TaskID, AgentID: a.ID,
		Tool: name, Status: status, Risk: string(risk), DurationMS: durationMS, Error: errMsg,
	})
}

// toolSpecs builds the gateway tool list for the agent.
func (a *Agent) toolSpecs(allow []string) []gateway.ToolSpec {
	var out []gateway.ToolSpec
	for _, gs := range a.deps.Tools.ToGatewaySpecs() {
		if len(allow) > 0 {
			ok := false
			for _, t := range allow {
				if t == gs.Name {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, gateway.ToolSpec{
			Type: "function",
		})
		// set fields via index to keep type literal simple
		out[len(out)-1].Function.Name = gs.Name
		out[len(out)-1].Function.Description = gs.Description
		out[len(out)-1].Function.Parameters = gs.Parameters
	}
	return out
}

// Usage returns token/cost/latency snapshots.
func (a *Agent) Usage() (in, out, calls int64, cost float64, latency time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.TokensIn, a.TokensOut, int64(a.ToolCalls), a.CostUSD, a.Latency
}

// ArtifactPaths returns the artifact paths produced by the agent.
func (a *Agent) ArtifactPaths() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.Artifacts...)
}

// ReplanReason returns why the agent asked for the task to be restructured
// (see the request_replan tool), or "" if it never did.
func (a *Agent) ReplanReason() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.replanReason
}

// LastOutput returns the final assistant message content, if any.
func (a *Agent) LastOutput() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.Transcript) - 1; i >= 0; i-- {
		if a.Transcript[i].Role == "assistant" && a.Transcript[i].Content != "" {
			return a.Transcript[i].Content
		}
	}
	return ""
}

func toContextMessages(msgs []gateway.Message) []composer.Message {
	out := make([]composer.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, composer.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name})
	}
	return out
}

func toGatewayMessages(msgs []composer.Message) []gateway.Message {
	out := make([]gateway.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, gateway.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name})
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
