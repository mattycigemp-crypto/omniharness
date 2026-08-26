// Package repair turns failures into intelligent recovery. A failed execution
// is classified, its probable root cause estimated, and the next attempt
// alters variables (model, role, context, instructions, tools, strategy)
// instead of blindly repeating the identical execution.
package repair

import (
	"fmt"
	"strings"

	"omniharness/internal/gateway"
	"omniharness/internal/task"
)

// Stage identifies where a failure occurred.
type Stage string

const (
	StageModel     Stage = "model"
	StageTool      Stage = "tool"
	StageEvaluate  Stage = "evaluate"
	StageOrchestra Stage = "orchestration"
)

// Failure is a classified failure.
type Failure struct {
	Stage   Stage  `json:"stage"`
	Kind    string `json:"kind"` // rate_limit | auth | network | server | build | test | evaluate | tool | timeout | budget | unknown
	Error   string `json:"error"`
	Attempt int    `json:"attempt"` // 1-based
	// Strategy is the strategy that produced the failure; repair may override
	// it when a structural change is more likely to help than another retry.
	Strategy string `json:"strategy,omitempty"`
}

// Classify turns a runtime error into a Failure.
func Classify(stage Stage, err error) Failure {
	f := Failure{Stage: stage, Error: err.Error()}
	var gwErr *gateway.Error
	if asErr(err, &gwErr) {
		f.Kind = string(gwErr.Kind)
		return f
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "budget"):
		f.Kind = "budget"
	case strings.Contains(msg, "timed out"), strings.Contains(msg, "timeout"):
		f.Kind = "timeout"
	case strings.Contains(msg, "build failed"):
		f.Kind = "build"
	case strings.Contains(msg, "tests failed"):
		f.Kind = "test"
	case strings.Contains(msg, "tool"):
		f.Kind = "tool"
	case strings.Contains(msg, "no rows"), strings.Contains(msg, "sqlite"):
		f.Kind = "persistence"
	default:
		f.Kind = "unknown"
	}
	return f
}

func asErr(err error, target **gateway.Error) bool {
	for err != nil {
		if e, ok := err.(*gateway.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Plan describes what the next attempt should change.
type Plan struct {
	// Strategy is the repair strategy name (e.g. "backoff-switch-model").
	Strategy string   `json:"strategy"`
	Changed  []string `json:"changed"` // human-readable variable changes
	// Role to use for the retry (empty = keep).
	Role string `json:"role,omitempty"`
	// ModelCapability to resolve for the retry (empty = keep).
	ModelCapability string `json:"modelCapability,omitempty"`
	// ExtraInstructions appended to the next attempt's prompt.
	ExtraInstructions string `json:"extraInstructions,omitempty"`
	// ExecutionStrategy, when set, overrides the task's execution strategy
	// (strategy-level repair): direct → sequential → plan-implement-verify,
	// single-agent → multi-agent etc.
	ExecutionStrategy string `json:"executionStrategy,omitempty"`
	// SkipRepair reports that repair is futile (terminal failure).
	SkipRepair bool `json:"skipRepair,omitempty"`
}

// Engine plans repairs. Attempt is the 1-based attempt that just failed;
// maxAttempts bounds the cycle.
type Engine struct {
	MaxAttempts int
}

// Plan produces the next-attempt plan for a failure.
func (e *Engine) Plan(f Failure, attempt, maxAttempts int) (Plan, error) {
	if maxAttempts <= 0 {
		maxAttempts = e.MaxAttempts
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if attempt >= maxAttempts {
		return Plan{SkipRepair: true,
			Strategy: "give-up",
			Changed:  []string{fmt.Sprintf("reached repair limit (%d)", maxAttempts)}}, nil
	}

	switch f.Kind {
	case "rate_limit", "server":
		// Transient: back off and switch to a cheaper/faster model to reduce
		// provider pressure.
		return Plan{
			Strategy:        "backoff-switch-model",
			ModelCapability: "fast",
			Changed:         []string{"wait before retry", "switch to fast model"},
		}, nil
	case "auth":
		return Plan{Strategy: "auth", SkipRepair: true, Changed: []string{"credentials rejected; cannot repair automatically"}}, nil
	case "network", "timeout":
		return Plan{
			Strategy: "retry-once",
			Changed:  []string{"network failure; single retry with longer timeout"},
		}, nil
	case "budget":
		return Plan{Strategy: "budget", SkipRepair: true, Changed: []string{"budget exhausted; cannot repair"}}, nil
	case "build", "test", "evaluate":
		// Engineering failures: bring in the debugger role with explicit
		// instructions to fix the failing verification. At the final attempt
		// the execution structure itself changes (strategy-level repair) —
		// blind retries are never repeated indefinitely.
		instruction := "The previous attempt failed verification. Reproduce the failure, fix the root cause, and re-run the verification until it passes."
		switch attempt {
		case 1:
			return Plan{
				Strategy:          "debugger-attempt",
				Role:              "debugger",
				ExtraInstructions: instruction,
				Changed:           []string{"role → debugger", "instructions: reproduce and fix"},
			}, nil
		case 2:
			return Plan{
				Strategy:          "debugger-context",
				Role:              "debugger",
				ModelCapability:   "reasoning",
				ExtraInstructions: instruction + " Use smaller steps and verify incrementally.",
				Changed:           []string{"role → debugger", "model → reasoning", "incremental verification"},
			}, nil
		default:
			// Structural repair: switch the execution strategy so the next run
			// is not a repeat of the failed one.
			exec := "plan-implement-verify"
			if f.Strategy == "direct" || f.Strategy == "swarm" || f.Strategy == "" {
				exec = "sequential"
			}
			return Plan{
				Strategy:           "rewrite-restructure",
				Role:               "debugger",
				ModelCapability:    "reasoning",
				ExecutionStrategy:  exec,
				ExtraInstructions:  "The prior execution failed verification repeatedly under the same structure. Reproduce the failure, trace the root cause precisely, and implement a fix; then re-run verification.",
				Changed:            []string{"restructured into " + exec + " execution", "role → debugger", "model → reasoning", "targeted fix"},
			}, nil
		}
	case "tool":
		return Plan{
			Strategy:          "restrict-tools",
			ExtraInstructions: "A tool call failed. Prefer reading and precise edits over broad shell commands.",
			Changed:           []string{"instructions: prefer precise tools"},
		}, nil
	case "persistence":
		return Plan{Strategy: "persistence", SkipRepair: true, Changed: []string{"storage failure; cannot repair automatically"}}, nil
	default:
		// Unknown: escalate with more capable model + reviewer.
		switch attempt {
		case 1:
			return Plan{
				Strategy:          "escalate",
				ModelCapability:   "reasoning",
				ExtraInstructions: "The previous attempt failed. Diagnose the failure precisely before acting again.",
				Changed:           []string{"model → reasoning", "diagnose first"},
			}, nil
		default:
			return Plan{
				Strategy:          "review",
				Role:              "reviewer",
				ModelCapability:   "reasoning",
				ExtraInstructions: "Review the previous attempt, identify the defect, and produce a corrected result.",
				Changed:           []string{"role → reviewer", "model → reasoning"},
			}, nil
		}
	}
}

// TaskOutcomeOfOutcome maps evaluator outcomes to task statuses.
func TaskOutcomeOfOutcome(o string) task.Status {
	switch o {
	case "PASS", "PASS_WITH_WARNINGS":
		return task.StatusCompleted
	case "NEEDS_REVIEW":
		return task.StatusCompleted
	default:
		return task.StatusFailed
	}
}
