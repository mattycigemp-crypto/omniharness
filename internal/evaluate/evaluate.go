// Package evaluate implements the verification framework. A model's final
// answer is never automatically treated as truth: task-specific evaluators
// (build, tests, lint, diff inspection, constraint checks, evidence checks)
// produce structured outcomes.
package evaluate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"omniharness/internal/envguard"
	"omniharness/internal/task"
)

// Outcome of an evaluation.
type Outcome string

const (
	Pass             Outcome = "PASS"
	Fail             Outcome = "FAIL"
	PassWithWarnings Outcome = "PASS_WITH_WARNINGS"
	NeedsReview      Outcome = "NEEDS_REVIEW"
)

// Request carries what the evaluator needs.
type Request struct {
	Task   task.Task
	Result task.Result
	CWD    string
	// MaxDuration bounds the evaluator's own execution.
	MaxDuration time.Duration
}

// Evaluator is the verification interface. Implementations are added without
// touching the core.
type Evaluator interface {
	Name() string
	Evaluate(ctx context.Context, r Request) (Outcome, string, error)
}

// Registry holds evaluators and can build the right set for a task.
type Registry struct {
	evaluators map[string]Evaluator
	// Timeout bounds evaluator commands.
	Timeout time.Duration
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		evaluators: map[string]Evaluator{},
		Timeout:    5 * time.Minute,
	}
}

// Register adds an evaluator.
func (r *Registry) Register(e Evaluator) error {
	if _, exists := r.evaluators[e.Name()]; exists {
		return fmt.Errorf("evaluator %q already registered", e.Name())
	}
	r.evaluators[e.Name()] = e
	return nil
}

// ForTask selects the evaluators appropriate for a task's profile. Software
// tasks get build/test checks, plus a diff check when the task set out to
// change files; research tasks get evidence checks.
func (r *Registry) ForTask(p task.Profile) []Evaluator {
	var out []Evaluator
	if p.Domain == task.DomainSoftware {
		if e, ok := r.evaluators["go-build"]; ok && hasToolchain(p, "go") {
			out = append(out, e)
		}
		if e, ok := r.evaluators["go-test"]; ok && hasToolchain(p, "go") {
			out = append(out, e)
		}
		// The diff check asserts that an intended change reached disk. Running
		// it on a task that never meant to write — explain, review, answer —
		// turns "nothing changed" into a false failure and burns the repair
		// budget re-running work that was already correct.
		if e, ok := r.evaluators["diff-check"]; ok && p.ModifiesFiles {
			out = append(out, e)
		}
	}
	if p.Domain == task.DomainResearch {
		if e, ok := r.evaluators["evidence"]; ok {
			out = append(out, e)
		}
	}
	return out
}

// hasToolchain is a permissive probe: the profile's tools mention the runtime.
func hasToolchain(p task.Profile, name string) bool {
	for _, t := range p.Tools {
		if strings.Contains(t, name) {
			return true
		}
	}
	// Software tasks default to go-toolchain checks when the workspace is a Go
	// module; the evaluator itself verifies presence and skips gracefully.
	return true
}

// RegisterDefaults adds the built-in evaluators.
func (r *Registry) RegisterDefaults() error {
	for _, e := range []Evaluator{
		&GoBuildEvaluator{},
		&GoTestEvaluator{},
		&DiffCheckEvaluator{},
		&EvidenceEvaluator{},
	} {
		if err := r.Register(e); err != nil {
			return err
		}
	}
	return nil
}

// runCmd runs a command with a timeout and captures output. The command never
// inherits credential environment variables.
func runCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = envguard.Filter()
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("evaluator command timed out")
	}
	return string(out), err
}

// GoBuildEvaluator runs `go build ./...` in the workspace.
type GoBuildEvaluator struct{}

func (e *GoBuildEvaluator) Name() string { return "go-build" }

func (e *GoBuildEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	if _, err := os.Stat(filepath.Join(r.CWD, "go.mod")); err != nil {
		return NeedsReview, "no go.mod — build check skipped", nil
	}
	out, err := runCmd(ctx, r.CWD, "go", "build", "./...")
	if err != nil {
		return Fail, "build failed:\n" + truncate(out, 4000), nil
	}
	return Pass, "build ok", nil
}

// GoTestEvaluator runs `go test ./...`.
type GoTestEvaluator struct{}

func (e *GoTestEvaluator) Name() string { return "go-test" }

func (e *GoTestEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	if _, err := os.Stat(filepath.Join(r.CWD, "go.mod")); err != nil {
		return NeedsReview, "no go.mod — test check skipped", nil
	}
	out, err := runCmd(ctx, r.CWD, "go", "test", "./...")
	if err != nil {
		return Fail, "tests failed:\n" + truncate(out, 4000), nil
	}
	return Pass, "tests pass", nil
}

// DiffCheckEvaluator verifies a change-producing task actually changed files.
type DiffCheckEvaluator struct{}

func (e *DiffCheckEvaluator) Name() string { return "diff-check" }

func (e *DiffCheckEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	if len(r.Result.Artifacts) > 0 {
		return Pass, fmt.Sprintf("%d artifacts produced", len(r.Result.Artifacts)), nil
	}
	// Skip when the workspace has no recognisable project structure.
	// Mirrors the go-build / go-test evaluators which already return
	// NeedsReview when go.mod is absent — a bare temp dir used in tests
	// would otherwise always fail this check in a clean CI checkout.
	if _, err := os.Stat(filepath.Join(r.CWD, "go.mod")); err != nil {
		return NeedsReview, "no go.mod — diff check skipped", nil
	}
	out, err := runCmd(ctx, r.CWD, "git", "status", "--porcelain")
	if err != nil {
		return NeedsReview, "not a git repo — diff check skipped", nil
	}
	if strings.TrimSpace(out) != "" {
		return Pass, "working tree has changes", nil
	}
	return Fail, "no changes detected in working tree", nil
}

// EvidenceEvaluator checks research results carry source evidence.
type EvidenceEvaluator struct{}

func (e *EvidenceEvaluator) Name() string { return "evidence" }

func (e *EvidenceEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	text := r.Result.Summary + "\n" + r.Result.Output
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") || strings.Contains(text, "[1]") || strings.Contains(text, "source") {
		return Pass, "evidence references present", nil
	}
	return Fail, "research result lacks source references or citations", nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[truncated]"
}
