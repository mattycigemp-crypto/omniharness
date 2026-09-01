// Package policy evaluates tool requests before execution. The flow is:
// Agent → Tool Request → Policy Evaluation → Risk Assessment → Execute /
// Approve / Block. Dangerous operations are never hidden behind silent
// defaults.
package policy

import (
	"context"
	"fmt"
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

	// An unrecognised risk class is not a licence to run. The four known
	// classes are always present in the config (Default seeds them and a
	// partial TOML table merges into it rather than replacing it), so reaching
	// this fallback means a tool declared something outside that set — an
	// empty risk, or a value this build does not know. Allowing it would let a
	// tool opt out of the gate simply by mislabelling itself.
	action, ok := e.cfg.RiskAction[string(r.Risk)]
	if !ok {
		return Ask, fmt.Sprintf("tool %q declares an unrecognised risk class %q; approval required", r.Tool, r.Risk), nil
	}
	switch action {
	case "block":
		return Block, fmt.Sprintf("risk class %q is blocked by policy", r.Risk), nil
	case "ask":
		if r.Risk == tools.RiskCritical {
			return Block, "critical-risk tools always require explicit approval; none configured", nil
		}
		return Ask, fmt.Sprintf("risk class %q requires approval", r.Risk), nil
	default:
		return Allow, "allowed by policy", nil
	}
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
	// Cheap containment check on the literal path. Symlink escapes are
	// handled by the tools layer's resolvePath; this is defense in depth.
	root = strings.TrimRight(root, `/\`)
	return p != root && !strings.HasPrefix(p, root+"/") && !strings.HasPrefix(p, root+`\`)
}
