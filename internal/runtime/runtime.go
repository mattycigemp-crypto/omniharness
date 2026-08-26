// Package runtime wires every subsystem together: configuration, event bus,
// durable store, gateway, tools, policy, context, evaluation, repair and the
// orchestrator. The CLI and TUI are thin consumers of this layer.
package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"omniharness/internal/agent"
	"omniharness/internal/budget"
	"omniharness/internal/config"
	composer "omniharness/internal/context"
	"omniharness/internal/event"
	"omniharness/internal/evaluate"
	"omniharness/internal/gateway"
	"omniharness/internal/id"
	"omniharness/internal/mcp"
	"omniharness/internal/memory"
	"omniharness/internal/model"
	"omniharness/internal/orchestrator"
	"omniharness/internal/policy"
	"omniharness/internal/repair"
	"omniharness/internal/session"
	"omniharness/internal/strategy"
	"omniharness/internal/task"
	"omniharness/internal/tools"
)

// Runtime is the composed application.
type Runtime struct {
	Cfg          config.Config
	Bus          *event.Bus
	Store        *session.Store
	Gateway      *gateway.Client
	ModelSel     *model.Selector
	Tools        *tools.Registry
	Policy       *policy.Engine
	Composer     *composer.Composer
	Evaluators   *evaluate.Registry
	Repair       *repair.Engine
	Analyzer     *task.Analyzer
	Orchestrator *orchestrator.Orchestrator
	Workspace    string

	MCPClients []*mcp.Client
	stopSinks  []func()

	sink *persistenceSink
}

// persistenceSink drains bus events into the store asynchronously. idle is
// true only between writes, so FlushEvents can rely on it for durability.
type persistenceSink struct {
	ch     <-chan event.Event
	mu     sync.Mutex
	idle   bool
	closed bool
}

// Options tweak construction (used in tests).
type Options struct {
	// Store overrides the default SQLite store location (tests).
	Store *session.Store
	// Gateway overrides the OmniRoute client (tests).
	Gateway *gateway.Client
	// DisablePersistenceSink skips event→store persistence.
	DisablePersistenceSink bool
}

// New builds a fully wired runtime from configuration.
func New(cfg config.Config, opts Options) (*Runtime, error) {
	bus := event.NewBus()

	var store *session.Store
	var err error
	if opts.Store != nil {
		store = opts.Store
	} else {
		store, err = session.Open(cfg.Persistence.Dir)
		if err != nil {
			return nil, err
		}
	}

	gw := opts.Gateway
	if gw == nil {
		gw = gateway.New(cfg.OmniRoute.Endpoint, cfg.OmniRoute.Timeout, cfg.OmniRoute.APIKey)
	}

	workspace, err := os.Getwd()
	if err != nil {
		workspace = "."
	}
	workspace, _ = filepath.Abs(workspace)
	if cfg.Policy.WorkspaceRoot != "" {
		workspace = cfg.Policy.WorkspaceRoot
	}

	reg := tools.NewRegistry()
	if err := tools.NewNative(workspace).Register(reg); err != nil {
		return nil, fmt.Errorf("register native tools: %w", err)
	}

	pol := policy.NewEngine(policy.Config{
		RiskAction:              cfg.Policy.RiskAction,
		AllowedTools:            cfg.Policy.AllowedTools,
		BlockedTools:            cfg.Policy.BlockedTools,
		WorkspaceRoot:           workspace,
		ShellAllowed:            cfg.Policy.ShellAllowed,
		GitPushRequiresApproval: cfg.Policy.GitPushRequiresApproval,
	}, nil)

	evals := evaluate.NewRegistry()
	if err := evals.RegisterDefaults(); err != nil {
		return nil, err
	}

	composerLimits := composer.Limits{CondenseAt: 96 << 10}
	advisor := &memory.Advisor{Store: store}
	modelSel := model.NewSelector(cfg.Models.Default, cfg.Models.Capabilities)
	// Performance memory can substitute an empirically better model among the
	// configured candidates, with an explainable reason; cold start declines.
	modelSel.Empirical = func(resolved string, candidates []string) (string, string, bool) {
		advice, ok := advisor.RecommendModel(candidates)
		if !ok {
			return "", "", false
		}
		return advice.Model, advice.Reason, true
	}
	r := &Runtime{
		Cfg:        cfg,
		Bus:        bus,
		Store:      store,
		Gateway:    gw,
		ModelSel:   modelSel,
		Tools:      reg,
		Policy:     pol,
		Composer:   composer.NewComposer(composerLimits),
		Evaluators: evals,
		Repair:     &repair.Engine{MaxAttempts: 2},
		Analyzer:   &task.Analyzer{RepoRoot: workspace},
		Workspace:  workspace,
	}

	r.Orchestrator = orchestrator.New(orchestrator.Deps{
		Bus:            bus,
		Store:          store,
		Gateway:        gw,
		ModelSel:       r.ModelSel,
		Roles:          agent.DefaultRoles(),
		Evaluators:     evals,
		Repair:         r.Repair,
		Analyzer:       r.Analyzer,
		Strategist:     strategy.Selector{},
		Tools:          reg,
		Policy:         pol,
		Composer:       r.Composer,
		Workspace:      workspace,
		Advisor:        advisor,
		MaxConcurrency: cfg.Budgets.MaxAgents,
		MaxTaskRepairs: cfg.Budgets.MaxRepairCycl,
	})

	if !opts.DisablePersistenceSink {
		r.startPersistenceSink()
	}
	return r, nil
}// startPersistenceSink persists every event to the session store.
func (r *Runtime) startPersistenceSink() {
	ch, cancel := r.Bus.Subscribe(1024)
	s := &persistenceSink{ch: ch}
	r.sink = s
	r.stopSinks = append(r.stopSinks, cancel)
	go func() {
		for e := range ch {
			s.mu.Lock()
			s.idle = false
			s.mu.Unlock()
			if e.SessionID != "" {
				if err := r.Store.AppendEvent(e.SessionID, e); err != nil {
					// Persistence failure must not kill the runtime, but it must
					// not vanish silently either: surface it on the bus.
					r.Bus.Publish(event.New(&event.LogMessageData{Message: "persist event: " + err.Error()}))
				}
			}
			s.mu.Lock()
			s.idle = true
			s.mu.Unlock()
		}
		s.mu.Lock()
		s.closed = true
		s.idle = true
		s.mu.Unlock()
	}()
}

// FlushEvents waits (bounded by ctx) until every event published so far has
// been durably written. RunTask calls it before returning so a completed task
// is never represented as finished while its terminal events are still in
// flight. Returns true when fully flushed.
func (r *Runtime) FlushEvents(ctx context.Context) bool {
	s := r.sink
	if s == nil {
		return true
	}
	for {
		s.mu.Lock()
		idle := s.idle
		empty := len(s.ch) == 0
		s.mu.Unlock()
		if idle && empty {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// Close shuts down the runtime: MCP clients, sinks, store.
func (r *Runtime) Close() {
	for _, cancel := range r.stopSinks {
		cancel()
	}
	for _, c := range r.MCPClients {
		_ = c.Close()
	}
	r.Store.Close()
}

// SetApprover installs the human-approval callback into the policy engine.
func (r *Runtime) SetApprover(a policy.Approver) { r.Policy.SetApprover(a) }

// NewSession creates and records a session.
func (r *Runtime) NewSession(cwd, title string) (*session.Session, error) {
	ss := &session.Session{ID: id.New(), CWD: cwd, Title: title, Status: "active"}
	if err := r.Store.CreateSession(ss); err != nil {
		return nil, err
	}
	e := event.New(&event.SessionStartedData{CWD: cwd, Title: title})
	e.SessionID = ss.ID
	r.Bus.Publish(e)
	return ss, nil
}

// RunOptions control RunTask.
type RunOptions struct {
	// TaskID reuses an existing task (resume path).
	TaskID string
	// Headless suppresses interactive output.
	Headless bool
	// Deadline caps wall-clock time.
	Deadline time.Duration
	// Budget overrides per-task budgets.
	Budget budget.Budget
	// ApproveAll auto-grants high-risk approvals.
	ApproveAll bool
}

// RunTask analyzes, plans and executes a task, returning the durable task.
func (r *Runtime) RunTask(ctx context.Context, sessionID, prompt string, opts RunOptions) (*task.Task, error) {
	spec := task.Spec{
		Prompt:    prompt,
		CWD:       r.Workspace,
		SessionID: sessionID,
		Budget:    opts.Budget,
		Deadline:  time.Now().Add(opts.Deadline).UTC(),
	}
	if opts.ApproveAll {
		r.Policy.SetApprover(policy.ApproverFunc(func(context.Context, policy.Request, string) (bool, error) {
			return true, nil
		}))
	}
	if opts.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Deadline)
		defer cancel()
	}
	tsk, err := r.Orchestrator.Run(ctx, sessionID, spec, opts.TaskID)
	// Durability barrier: the task row is written synchronously by the
	// orchestrator; this bounds the async event sink so the terminal event
	// (task.completed / task.failed) is durable before the caller sees the
	// result. The task row itself is already durable either way.
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer flushCancel()
	if !r.FlushEvents(flushCtx) {
		r.Bus.Publish(event.New(&event.LogMessageData{Message: "event flush timed out; terminal events may lag the task row"}))
	}
	return tsk, err
}

// Ping verifies OmniRoute connectivity.
func (r *Runtime) Ping(ctx context.Context) error { return r.Gateway.Ping(ctx) }

// LoadMCPServers starts configured MCP servers and registers their tools.
// Servers that fail to start are reported; the rest still load.
func (r *Runtime) LoadMCPServers(ctx context.Context, servers []mcp.Server) error {
	if len(servers) == 0 {
		return nil
	}
	var failures []string
	for _, srv := range servers {
		c := mcp.NewClient(srv)
		if err := c.Start(ctx); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", srv.Name, err))
			continue
		}
		toolInfos, err := c.ListTools(ctx)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: list tools: %v", srv.Name, err))
			_ = c.Close()
			continue
		}
		for _, ti := range toolInfos {
			adapter := &mcp.ToolAdapter{Client: c, Info: ti}
			if err := r.Tools.Register(adapter); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", srv.Name, err))
			}
		}
		r.MCPClients = append(r.MCPClients, c)
	}
	if len(failures) > 0 {
		return fmt.Errorf("MCP load issues: %s", strings.Join(failures, "; "))
	}
	return nil
}

// ListSessions lists recent sessions.
func (r *Runtime) ListSessions(limit int) ([]*session.Session, error) {
	return r.Store.ListSessions(limit)
}

// SessionEvents replays events for a session.
func (r *Runtime) SessionEvents(sessionID string, limit int) ([]event.Event, error) {
	return r.Store.Events(sessionID, limit)
}
