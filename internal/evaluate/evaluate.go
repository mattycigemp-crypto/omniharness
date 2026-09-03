// Package evaluate implements the verification framework. A model's final
// answer is never automatically treated as truth: task-specific evaluators
// (build, tests, lint, diff inspection, constraint checks, evidence checks)
// produce structured outcomes.
package evaluate

import (
	"context"
	"encoding/json"
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
		// Same offer-unconditionally-and-self-skip pattern as the Go pair
		// above: every software task gets these regardless of language, and
		// each one checks for package.json (and the script it needs) itself
		// rather than the harness trying to detect "this is a Node project"
		// up front.
		if e, ok := r.evaluators["npm-build"]; ok {
			out = append(out, e)
		}
		if e, ok := r.evaluators["npm-test"]; ok {
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

// RegisterDefaults adds the built-in evaluators.
func (r *Registry) RegisterDefaults() error {
	for _, e := range []Evaluator{
		&GoBuildEvaluator{},
		&GoTestEvaluator{},
		&NpmBuildEvaluator{},
		&NpmTestEvaluator{},
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

// hasNpmScript reports whether dir's package.json declares the named script.
// A missing or unparseable package.json reads the same as "no such script" —
// the caller's own os.Stat check on package.json distinguishes "not a Node
// project" from "malformed package.json" for the message it reports.
func hasNpmScript(dir, script string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return false
	}
	_, ok := pkg.Scripts[script]
	return ok
}

// NpmBuildEvaluator runs `npm run build` for a Node/TypeScript workspace.
// Skips when there is no package.json (not a Node project) or no "build"
// script (many Node projects — libraries, plain scripts — have nothing to
// build), same as GoBuildEvaluator skips a workspace with no go.mod.
type NpmBuildEvaluator struct{}

func (e *NpmBuildEvaluator) Name() string { return "npm-build" }

func (e *NpmBuildEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	if _, err := os.Stat(filepath.Join(r.CWD, "package.json")); err != nil {
		return NeedsReview, "no package.json — npm build check skipped", nil
	}
	if !hasNpmScript(r.CWD, "build") {
		return NeedsReview, `no "build" script in package.json — npm build check skipped`, nil
	}
	out, err := runCmd(ctx, r.CWD, "npm", "run", "build")
	if err != nil {
		return Fail, "npm run build failed:\n" + truncate(out, 4000), nil
	}
	return Pass, "npm run build ok", nil
}

// NpmTestEvaluator runs `npm test` for a Node/TypeScript workspace. Skips
// when there is no package.json or no "test" script, same reasoning as
// NpmBuildEvaluator.
type NpmTestEvaluator struct{}

func (e *NpmTestEvaluator) Name() string { return "npm-test" }

func (e *NpmTestEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	if _, err := os.Stat(filepath.Join(r.CWD, "package.json")); err != nil {
		return NeedsReview, "no package.json — npm test check skipped", nil
	}
	if !hasNpmScript(r.CWD, "test") {
		return NeedsReview, `no "test" script in package.json — npm test check skipped`, nil
	}
	out, err := runCmd(ctx, r.CWD, "npm", "test")
	if err != nil {
		return Fail, "npm test failed:\n" + truncate(out, 4000), nil
	}
	return Pass, "npm test passed", nil
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
