// Package evaluate implements the verification framework. A model's final
// answer is never automatically treated as truth: task-specific evaluators
// (build, tests, lint, diff inspection, constraint checks, evidence checks)
// produce structured outcomes.
package evaluate

import (
	"context"
	"encoding/json"
	"errors"
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
		// Rust and Python, same pattern again: offered to every software
		// task, each one checking for its own project marker (and its own
		// toolchain) rather than the harness guessing the ecosystem.
		if e, ok := r.evaluators["cargo-build"]; ok {
			out = append(out, e)
		}
		if e, ok := r.evaluators["cargo-test"]; ok {
			out = append(out, e)
		}
		if e, ok := r.evaluators["pytest"]; ok {
			out = append(out, e)
		}
		// Lint-class checks. These report PASS_WITH_WARNINGS rather than
		// FAIL, so a finding is surfaced on the run without failing a task
		// that may not have caused it.
		if e, ok := r.evaluators["go-vet"]; ok {
			out = append(out, e)
		}
		if e, ok := r.evaluators["npm-lint"]; ok {
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
	// Not gated on domain: criteria describe what "done" means for whatever
	// this task is, and only exist at all when the deepening pass ran.
	if len(p.AcceptanceCriteria) > 0 {
		if e, ok := r.evaluators["acceptance-criteria"]; ok {
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
		&CargoBuildEvaluator{},
		&CargoTestEvaluator{},
		&PytestEvaluator{},
		&GoVetEvaluator{},
		&NpmLintEvaluator{},
		&AcceptanceCriteriaEvaluator{},
		&DiffCheckEvaluator{},
		&EvidenceEvaluator{},
	} {
		if err := r.Register(e); err != nil {
			return err
		}
	}
	return nil
}

// lookPath resolves an executable. It is a variable so tests can exercise
// the "toolchain is not installed" path on a machine that has the toolchain.
var lookPath = exec.LookPath

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
	if _, err := lookPath("go"); err != nil {
		return NeedsReview, "go not installed — build check skipped", nil
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
	if _, err := lookPath("go"); err != nil {
		return NeedsReview, "go not installed — test check skipped", nil
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
	if _, err := lookPath("npm"); err != nil {
		return NeedsReview, "npm not installed — build check skipped", nil
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
	if _, err := lookPath("npm"); err != nil {
		return NeedsReview, "npm not installed — test check skipped", nil
	}
	out, err := runCmd(ctx, r.CWD, "npm", "test")
	if err != nil {
		return Fail, "npm test failed:\n" + truncate(out, 4000), nil
	}
	return Pass, "npm test passed", nil
}

// AcceptanceCriteriaEvaluator surfaces the acceptance criteria the optional
// deepening pass produced (task.DeepAnalyzer) into the verification record.
//
// It deliberately never returns Pass or Fail. Whether prose criteria are met
// is a judgment this evaluator cannot make from a string — matching keywords
// against the result would manufacture a verdict rather than measure one, and
// a fabricated pass is worse than an honest "nobody checked". So it reports
// NeedsReview and names the criteria, which puts them in the evaluations
// table and in front of whoever reads the run. The criteria also reach every
// agent through the composed system prompt, so a reviewer on a verify step
// can judge them with tools; this row records that the harness itself did not.
type AcceptanceCriteriaEvaluator struct{}

func (e *AcceptanceCriteriaEvaluator) Name() string { return "acceptance-criteria" }

func (e *AcceptanceCriteriaEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	criteria := r.Task.Profile.AcceptanceCriteria
	if len(criteria) == 0 {
		return NeedsReview, "no acceptance criteria were produced for this task", nil
	}
	return NeedsReview, fmt.Sprintf("%s, not machine-checked:\n- %s",
		plural(len(criteria), "acceptance criterion", "acceptance criteria"),
		strings.Join(criteria, "\n- ")), nil
}

// plural renders a count with the right noun form.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
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

// EvidenceEvaluator looks for reference markers in a research result.
//
// Finding none reports PASS_WITH_WARNINGS, not FAIL. This is a substring
// scan, and a substring scan cannot tell an unsupported claim from a
// well-supported answer that happens to cite the workspace rather than a
// URL — which is exactly what research against a local repository produces.
// As a hard failure it did two bad things at once: it failed honest answers,
// driving real repair spend over a non-problem, while passing any answer
// that used the word "source" anywhere. A check that is trivially satisfied
// but not trivially passed honestly is worse than an explicit warning.
type EvidenceEvaluator struct{}

func (e *EvidenceEvaluator) Name() string { return "evidence" }

func (e *EvidenceEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	text := r.Result.Summary + "\n" + r.Result.Output
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") || strings.Contains(text, "[1]") || strings.Contains(text, "source") {
		return Pass, "evidence references present", nil
	}
	return PassWithWarnings, "no source references or citations found — this scan cannot tell whether this answer needed any", nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[truncated]"
}

// exitCode extracts a command's exit status, or -1 when the error is not an
// exit status at all (the binary was missing, the context was cancelled).
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// CargoBuildEvaluator runs `cargo build` for a Rust workspace. Skips when
// there is no Cargo.toml, and when cargo itself is not installed — a Rust
// repo on a machine without the toolchain is not a broken result, the same
// way a workspace with no Cargo.toml is not one.
type CargoBuildEvaluator struct{}

func (e *CargoBuildEvaluator) Name() string { return "cargo-build" }

func (e *CargoBuildEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	if _, err := os.Stat(filepath.Join(r.CWD, "Cargo.toml")); err != nil {
		return NeedsReview, "no Cargo.toml — cargo build check skipped", nil
	}
	if _, err := lookPath("cargo"); err != nil {
		return NeedsReview, "cargo not installed — build check skipped", nil
	}
	out, err := runCmd(ctx, r.CWD, "cargo", "build")
	if err != nil {
		return Fail, "cargo build failed:\n" + truncate(out, 4000), nil
	}
	return Pass, "cargo build ok", nil
}

// CargoTestEvaluator runs `cargo test` for a Rust workspace.
type CargoTestEvaluator struct{}

func (e *CargoTestEvaluator) Name() string { return "cargo-test" }

func (e *CargoTestEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	if _, err := os.Stat(filepath.Join(r.CWD, "Cargo.toml")); err != nil {
		return NeedsReview, "no Cargo.toml — cargo test check skipped", nil
	}
	if _, err := lookPath("cargo"); err != nil {
		return NeedsReview, "cargo not installed — test check skipped", nil
	}
	out, err := runCmd(ctx, r.CWD, "cargo", "test")
	if err != nil {
		return Fail, "cargo tests failed:\n" + truncate(out, 4000), nil
	}
	return Pass, "cargo tests pass", nil
}

// pythonMarkers are the files that identify a Python project root.
var pythonMarkers = []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt"}

// PytestEvaluator runs pytest for a Python workspace. Skips when nothing
// marks the directory as a Python project and when pytest is not installed.
// Exit status 5 is pytest's "no tests were collected", which is not a
// failing suite — a project that has no tests yet has not broken anything.
type PytestEvaluator struct{}

func (e *PytestEvaluator) Name() string { return "pytest" }

func (e *PytestEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	marked := false
	for _, m := range pythonMarkers {
		if _, err := os.Stat(filepath.Join(r.CWD, m)); err == nil {
			marked = true
			break
		}
	}
	if !marked {
		return NeedsReview, "no Python project markers — pytest check skipped", nil
	}
	if _, err := lookPath("pytest"); err != nil {
		return NeedsReview, "pytest not installed — test check skipped", nil
	}
	out, err := runCmd(ctx, r.CWD, "pytest", "-q")
	if err != nil {
		if exitCode(err) == 5 {
			return NeedsReview, "pytest collected no tests — nothing to check", nil
		}
		return Fail, "pytest failed:\n" + truncate(out, 4000), nil
	}
	return Pass, "pytest passed", nil
}

// GoVetEvaluator runs `go vet ./...`. Unlike the build and test evaluators,
// findings here report PASS_WITH_WARNINGS rather than FAIL: vet reports
// suspicious constructs, which are worth surfacing on the run but are not by
// themselves evidence that the task's own result is wrong — and a repository
// that already had a vet finding before the task started would otherwise
// fail every task run against it.
type GoVetEvaluator struct{}

func (e *GoVetEvaluator) Name() string { return "go-vet" }

func (e *GoVetEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	if _, err := os.Stat(filepath.Join(r.CWD, "go.mod")); err != nil {
		return NeedsReview, "no go.mod — vet check skipped", nil
	}
	if _, err := lookPath("go"); err != nil {
		return NeedsReview, "go not installed — vet check skipped", nil
	}
	out, err := runCmd(ctx, r.CWD, "go", "vet", "./...")
	if err != nil {
		return PassWithWarnings, "go vet reported findings:\n" + truncate(out, 4000), nil
	}
	return Pass, "go vet clean", nil
}

// NpmLintEvaluator runs `npm run lint` when the project declares that
// script. Same warning-not-failure reasoning as GoVetEvaluator.
type NpmLintEvaluator struct{}

func (e *NpmLintEvaluator) Name() string { return "npm-lint" }

func (e *NpmLintEvaluator) Evaluate(ctx context.Context, r Request) (Outcome, string, error) {
	if _, err := os.Stat(filepath.Join(r.CWD, "package.json")); err != nil {
		return NeedsReview, "no package.json — npm lint check skipped", nil
	}
	if !hasNpmScript(r.CWD, "lint") {
		return NeedsReview, `no "lint" script in package.json — npm lint check skipped`, nil
	}
	out, err := runCmd(ctx, r.CWD, "npm", "run", "lint")
	if err != nil {
		return PassWithWarnings, "npm run lint reported findings:\n" + truncate(out, 4000), nil
	}
	return Pass, "npm run lint clean", nil
}
