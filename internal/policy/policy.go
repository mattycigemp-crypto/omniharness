// Package policy evaluates tool requests before execution. The flow is:
// Agent → Tool Request → Policy Evaluation → Risk Assessment → Execute /
// Approve / Block. Dangerous operations are never hidden behind silent
// defaults.
package policy

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"omniharness/internal/tools"
)

// Decision is the outcome of policy evaluation.
type Decision int

const (
	Allow Decision = iota
	Ask
	Block
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	case Block:
		return "block"
	}
	return "unknown"
}

// Config is the policy configuration (mirrors config.Policy).
type Config struct {
	RiskAction              map[string]string // low|medium|high|critical → allow|ask|block
	AllowedTools            []string
	BlockedTools            []string
	WorkspaceRoot           string
	ShellAllowed            bool
	GitPushRequiresApproval bool
}

// Request is a tool invocation awaiting policy evaluation.
type Request struct {
	Tool    string
	Input   map[string]any
	Risk    tools.Risk
	AgentID string
}

// Approver is implemented by the runtime (TUI, CLI prompt, or headless
// auto-deny). It is only consulted when evaluation returns Ask.
type Approver interface {
	// RequestApproval asks a human to approve or deny an action.
	RequestApproval(ctx context.Context, r Request, reason string) (granted bool, err error)
}

// ApproverFunc adapts a function to the Approver interface.
type ApproverFunc func(ctx context.Context, r Request, reason string) (bool, error)

// RequestApproval implements Approver.
func (f ApproverFunc) RequestApproval(ctx context.Context, r Request, reason string) (bool, error) {
	return f(ctx, r, reason)
}

// Engine evaluates tool requests against policy.
type Engine struct {
	cfg      Config
	approver Approver
}

// NewEngine builds a policy engine.
func NewEngine(cfg Config, approver Approver) *Engine {
	if cfg.RiskAction == nil {
		cfg.RiskAction = map[string]string{}
	}
	return &Engine{cfg: cfg, approver: approver}
}

// SetApprover installs or replaces the approver (used by the TUI/CLI).
func (e *Engine) SetApprover(a Approver) { e.approver = a }

// Evaluate runs the policy decision for a request. The returned reason
// explains the outcome.
func (e *Engine) Evaluate(ctx context.Context, r Request) (Decision, string, error) {
	if r.Tool == "" {
		return Block, "missing tool name", nil
	}
	for _, b := range e.cfg.BlockedTools {
		if b == r.Tool {
			return Block, fmt.Sprintf("tool %q is blocked by policy", r.Tool), nil
		}
	}
	if len(e.cfg.AllowedTools) > 0 {
		allowed := false
		for _, a := range e.cfg.AllowedTools {
			if a == r.Tool {
				allowed = true
				break
			}
		}
		if !allowed {
			return Block, fmt.Sprintf("tool %q is not in the allowed set", r.Tool), nil
		}
	}

	// Special-case rules that override generic risk actions.
	if r.Tool == "shell" && !e.cfg.ShellAllowed {
		return Block, "shell execution is disabled by policy (set policy.shell_allowed = true)", nil
	}
	if r.Tool == "git" && e.cfg.GitPushRequiresApproval {
		if args, ok := r.Input["args"].([]any); ok {
			for _, a := range args {
				if s, ok := a.(string); ok && s == "push" {
					return Ask, "git push requires explicit approval", nil
				}
			}
		}
	}
	if r.Tool == "write_file" && e.cfg.WorkspaceRoot != "" {
		if p, _ := r.Input["path"].(string); p != "" {
			if outsideWorkspace(p, e.cfg.WorkspaceRoot) {
				return Block, fmt.Sprintf("path %q is outside the workspace root", p), nil
			}
		}
	}

	d, reason := e.decideForRisk(r.Risk)
	return d, reason, nil
}

// decideForRisk maps a risk class to a decision using cfg.RiskAction alone —
// no tool name, no allow/block lists, no shell/git/workspace special cases.
// Evaluate falls through to this once every tool-specific rule has cleared;
// EvaluateTaskRisk uses it directly, since none of those tool-specific rules
// mean anything for a task considered as a whole rather than one action.
func (e *Engine) decideForRisk(risk tools.Risk) (Decision, string) {
	// An unrecognised risk class is not a licence to run. The four known
	// classes are always present in the config (Default seeds them and a
	// partial TOML table merges into it rather than replacing it), so reaching
	// this fallback means something declared a risk outside that set — empty,
	// or a value this build does not know. Allowing it would let a caller opt
	// out of the gate simply by mislabelling itself.
	action, ok := e.cfg.RiskAction[string(risk)]
	if !ok {
		return Ask, fmt.Sprintf("unrecognised risk class %q; approval required", risk)
	}
	switch action {
	case "block":
		return Block, fmt.Sprintf("risk class %q is blocked by policy", risk)
	case "ask":
		// Critical is the one class that cannot be talked down to a prompt.
		// Configuring it as "ask" does not make it promptable; it still
		// blocks, whether or not an approver is connected. The old wording
		// here blamed a missing approver, which read as a setup problem when
		// it is a deliberate refusal.
		if risk == tools.RiskCritical {
			return Block, "critical-risk actions cannot be approved interactively; set an explicit policy for them"
		}
		return Ask, fmt.Sprintf("risk class %q requires approval", risk)
	default:
		return Allow, "allowed by policy"
	}
}

// EvaluateTaskRisk decides whether a task as a whole — not any one tool call
// — may proceed, using the same cfg.RiskAction config every tool risk class
// resolves against. It skips every tool-specific rule in Evaluate
// (allow/block lists, shell/git/workspace special cases): those describe one
// action, and a task-level decision happens before any tool has even been
// chosen.
func (e *Engine) EvaluateTaskRisk(risk tools.Risk) (Decision, string) {
	return e.decideForRisk(risk)
}

// EvaluateAndExecuteTaskRisk is EvaluateTaskRisk's EvaluateAndExecute
// counterpart: evaluate, and if the decision is Ask, consult the approver
// with a Request whose Tool is deliberately empty — the approver prompt (CLI
// and TUI both) renders that as "this task" rather than a specific action.
func (e *Engine) EvaluateAndExecuteTaskRisk(ctx context.Context, risk tools.Risk) (Decision, error) {
	d, reason := e.EvaluateTaskRisk(risk)
	if d != Ask {
		return d, nil
	}
	if e.approver == nil {
		return Block, fmt.Errorf("approval required (%s) but no approver is connected", reason)
	}
	granted, err := e.approver.RequestApproval(ctx, Request{Risk: risk}, reason)
	if err != nil {
		return Block, err
	}
	if !granted {
		return Block, nil
	}
	return Allow, nil
}

// EvaluateAndExecute is a convenience used by the agent loop: evaluate, and
// if Ask, consult the approver. Returns the decision actually taken.
func (e *Engine) EvaluateAndExecute(ctx context.Context, r Request) (Decision, error) {
	d, reason, err := e.Evaluate(ctx, r)
	if err != nil {
		return Block, err
	}
	if d == Ask {
		if e.approver == nil {
			return Block, fmt.Errorf("approval required (%s) but no approver is connected", reason)
		}
		granted, err := e.approver.RequestApproval(ctx, r, reason)
		if err != nil {
			return Block, err
		}
		if !granted {
			return Block, nil
		}
		return Allow, nil
	}
	return d, nil
}

func outsideWorkspace(p, root string) bool {
	// A relative path is what a model normally emits, and it means "inside
	// the workspace" — the tools layer resolves it against the root before
	// touching disk. Judging it by literal prefix marked every relative path
	// as an escape, so with a workspace root configured (which the runtime
	// always sets) every write_file that did not spell out an absolute path
	// was blocked. Resolve first, then ask whether the result escapes.
	//
	// Symlink escapes are still handled by the tools layer's resolvePath;
	// this stays defense in depth against traversal.
	root = filepath.Clean(root)
	// Only a genuinely relative path is resolved against the root. A path
	// that starts with a separator is rooted even where Go does not call it
	// absolute — on Windows "/etc/passwd" has no drive letter, so IsAbs is
	// false, and joining it would have quietly relocated a system path into
	// the workspace and allowed it.
	rooted := filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`)
	if !rooted {
		p = filepath.Join(root, p)
	}
	rel, err := filepath.Rel(root, filepath.Clean(p))
	if err != nil {
		// Different volume, or otherwise unrelatable: not inside.
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
