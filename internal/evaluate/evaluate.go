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
	"runtime"
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
// tasks get build/test checks; diff-check is added only when the task set out
// to change files. Research tasks get evidence checks.
func (r *Registry) ForTask(p task.Profile) []Evaluator {
	var out []Evaluator
	// The Go evaluators are offered to every software task rather than
	// gated on a detected toolchain: each one checks for a module itself
	// and skips when there is none. An earlier hasToolchain helper looked
	// like that gate but returned true unconditionally.
	if p.Domain == task.DomainSoftware {
		if e, ok := r.evaluators["go-build"]; ok {
			out = append(out, e)
		}
		if e, ok := r.evaluators["go-test"]; ok {
			out = append(out, e)
		}
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
	// Killing the command is not enough on its own: CombinedOutput waits for the
	// output pipe, which any surviving grandchild still holds open. Bound that
	// wait so an evaluator timeout is honoured. `go test` in particular spawns
	// the compiled test binaries.
	cmd.WaitDelay = 2 * time.Second
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
	// The check is only meaningful when the workspace is itself the root of a
	// git work tree. A directory that merely sits inside some other repository
	// would otherwise be judged on that repository's diff, which this task never
	// touched — that is what made a bare temp workspace inherit the harness's own
	// checkout state. Deliberately not keyed on go.mod: the harness drives
	// projects in any language, and a TypeScript or Python workspace needs this
	// check just as much as a Go one.
	top, err := runCmd(ctx, r.CWD, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return NeedsReview, "not a git repo — diff check skipped", nil
	}
	if !sameDir(strings.TrimSpace(top), r.CWD) {
		return NeedsReview, "workspace is not a repository root — diff check skipped", nil
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

// sameDir reports whether two paths name the same directory. git prints the
// work-tree root with forward slashes, temp directories are often reached
// through a symlink, and Windows paths differ only by case — so compare the
// resolved, cleaned forms rather than the raw strings.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
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
