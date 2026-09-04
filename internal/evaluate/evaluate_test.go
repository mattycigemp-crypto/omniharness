package evaluate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"omniharness/internal/task"
)

func TestGoBuildPassAndFail(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() { println(\"hi\") }\n"), 0o644)

	e := &GoBuildEvaluator{}
	outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Pass {
		t.Fatalf("outcome %s: %s", outcome, detail)
	}

	// Break the build.
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() { broken(\n"), 0o644)
	outcome, detail, _ = e.Evaluate(context.Background(), Request{CWD: dir})
	if outcome != Fail {
		t.Fatalf("expected fail, got %s: %s", outcome, detail)
	}
}

func TestGoTestEvaluator(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package main\nimport \"testing\"\nfunc TestOk(t *testing.T) {}\n"), 0o644)
	e := &GoTestEvaluator{}
	outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Pass {
		t.Fatalf("outcome %s: %s", outcome, detail)
	}
}

func TestNpmBuildSkipsWithNoPackageJSON(t *testing.T) {
	e := &NpmBuildEvaluator{}
	outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s (%s), want NeedsReview for a non-Node workspace", outcome, detail)
	}
}

// A Node project with no "build" script (a plain library, a script) is not
// a failure to build — there is nothing to build, same as GoBuildEvaluator
// reads "no go.mod" as "not applicable" rather than "broken".
func TestNpmBuildSkipsWithNoBuildScript(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"test":"echo ok"}}`), 0o644)
	e := &NpmBuildEvaluator{}
	outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s (%s), want NeedsReview with no build script", outcome, detail)
	}
}

func TestNpmBuildPassAndFail(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not available")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"build":"node -e \"process.exit(0)\""}}`), 0o644)
	e := &NpmBuildEvaluator{}
	outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Pass {
		t.Fatalf("outcome %s: %s", outcome, detail)
	}

	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"build":"node -e \"process.exit(1)\""}}`), 0o644)
	outcome, detail, _ = e.Evaluate(context.Background(), Request{CWD: dir})
	if outcome != Fail {
		t.Fatalf("expected fail, got %s: %s", outcome, detail)
	}
}

func TestNpmTestSkipsWithNoTestScript(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"build":"echo ok"}}`), 0o644)
	e := &NpmTestEvaluator{}
	outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s (%s), want NeedsReview with no test script", outcome, detail)
	}
}

func TestNpmTestPassAndFail(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not available")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"test":"node -e \"process.exit(0)\""}}`), 0o644)
	e := &NpmTestEvaluator{}
	outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Pass {
		t.Fatalf("outcome %s: %s", outcome, detail)
	}

	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"test":"node -e \"process.exit(1)\""}}`), 0o644)
	outcome, detail, _ = e.Evaluate(context.Background(), Request{CWD: dir})
	if outcome != Fail {
		t.Fatalf("expected fail, got %s: %s", outcome, detail)
	}
}

func TestDiffCheck(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "t@t")
	run(t, dir, "git", "config", "user.name", "t")
	// Deliberately not a Go module: diff-check must work in a workspace of any
	// language, so nothing here identifies the project's toolchain.
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "init")

	e := &DiffCheckEvaluator{}
	outcome, _, err := e.Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Fail {
		t.Fatalf("clean tree must fail diff check, got %s", outcome)
	}

	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("y"), 0o644)
	outcome, _, _ = e.Evaluate(context.Background(), Request{CWD: dir})
	if outcome != Pass {
		t.Fatalf("dirty tree must pass diff check, got %s", outcome)
	}

	// Artifacts short-circuit the git check.
	r := Request{CWD: dir, Result: task.Result{Artifacts: []string{"z.go"}}}
	outcome, _, _ = e.Evaluate(context.Background(), r)
	if outcome != Pass {
		t.Fatal("artifacts must pass")
	}
}

// A workspace nested inside someone else's repository must not be judged on
// that repository's diff. This is the case that made the harness's own tests
// inherit the developer's checkout state: a clean tree failed every task, a
// dirty one passed, so the suite's result depended on uncommitted work.
func TestDiffCheckSkipsWorkspaceNestedInAnotherRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	outer := t.TempDir()
	run(t, outer, "git", "init")
	run(t, outer, "git", "config", "user.email", "t@t")
	run(t, outer, "git", "config", "user.name", "t")
	os.WriteFile(filepath.Join(outer, "tracked.txt"), []byte("x"), 0o644)
	run(t, outer, "git", "add", ".")
	run(t, outer, "git", "commit", "-m", "init")

	// A sub-directory of the outer repo, with nothing of its own to report.
	nested := filepath.Join(outer, "workspace")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	e := &DiffCheckEvaluator{}
	outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: nested})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s (%s), want NEEDS_REVIEW for a non-root workspace", outcome, detail)
	}
}

// A workspace that is not a git repository at all cannot be judged on its
// diff, and must not be failed for it.
func TestDiffCheckSkipsNonGitWorkspace(t *testing.T) {
	e := &DiffCheckEvaluator{}
	outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s (%s), want NEEDS_REVIEW", outcome, detail)
	}
}

func TestEvidenceEvaluator(t *testing.T) {
	e := &EvidenceEvaluator{}
	outcome, _, _ := e.Evaluate(context.Background(), Request{Result: task.Result{Summary: "see https://example.com"}})
	if outcome != Pass {
		t.Fatal("URL should pass evidence check")
	}
	// Deliberate contract change: an uncited result warns, it does not fail.
	// This assertion used to require Fail. A substring scan cannot tell an
	// unsupported claim from an answer that cites the workspace instead of a
	// URL, and as a hard failure it drove repair cycles over honest answers
	// while passing anything containing the word "source". The distinction it
	// can actually make — references present or not — is still asserted here
	// and above, so this is not the assertion being relaxed to go green.
	outcome, _, _ = e.Evaluate(context.Background(), Request{Result: task.Result{Summary: "trust me"}})
	if outcome != PassWithWarnings {
		t.Fatal("uncited research must warn")
	}
}

func TestRegistrySelection(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterDefaults(); err != nil {
		t.Fatal(err)
	}
	// A software task that changes files gets build/test/diff.
	evs := r.ForTask(task.Profile{Domain: task.DomainSoftware, ModifiesFiles: true})
	names := map[string]bool{}
	for _, e := range evs {
		names[e.Name()] = true
	}
	if !names["go-build"] || !names["go-test"] || !names["npm-build"] || !names["npm-test"] || !names["diff-check"] {
		t.Fatalf("software evaluators %v", names)
	}
	// A read-only software task still gets build/test, but no diff check: it
	// never meant to write, so a clean tree is the expected outcome, not a
	// failure. Selecting diff-check here is what made every task fail on a
	// clean checkout.
	evs = r.ForTask(task.Profile{Domain: task.DomainSoftware})
	names = map[string]bool{}
	for _, e := range evs {
		names[e.Name()] = true
	}
	if names["diff-check"] {
		t.Fatalf("read-only software task must not run diff-check: %v", names)
	}
	if !names["go-build"] || !names["go-test"] || !names["npm-build"] || !names["npm-test"] {
		t.Fatalf("read-only software task still needs build/test: %v", names)
	}
	// Research task gets evidence.
	evs = r.ForTask(task.Profile{Domain: task.DomainResearch})
	if len(evs) != 1 || evs[0].Name() != "evidence" {
		t.Fatalf("research evaluators %v", evs)
	}
	// General tasks get nothing.
	if len(r.ForTask(task.Profile{Domain: task.DomainGeneral})) != 0 {
		t.Fatal("general task should have no evaluators")
	}
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func TestAcceptanceCriteriaEvaluatorNamesTheCriteria(t *testing.T) {
	e := &AcceptanceCriteriaEvaluator{}
	req := Request{Task: task.Task{Profile: task.Profile{
		AcceptanceCriteria: []string{"empty input returns 0", "go test ./... passes"},
	}}}
	outcome, detail, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// Never Pass or Fail: this evaluator cannot judge prose criteria, and a
	// fabricated verdict would be worse than an honest "nobody checked".
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s, want NeedsReview — this evaluator must never claim a verdict it cannot measure", outcome)
	}
	for _, want := range []string{"empty input returns 0", "go test ./... passes", "not machine-checked"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q: %s", want, detail)
		}
	}
	if !strings.Contains(detail, "2 acceptance criteria") {
		t.Errorf("detail should count the criteria: %s", detail)
	}
}

func TestAcceptanceCriteriaEvaluatorWithNoCriteria(t *testing.T) {
	e := &AcceptanceCriteriaEvaluator{}
	outcome, detail, err := e.Evaluate(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s (%s), want NeedsReview", outcome, detail)
	}
}

// One criterion reads as "1 acceptance criterion", not "1 acceptance criteria".
func TestAcceptanceCriteriaEvaluatorSingular(t *testing.T) {
	e := &AcceptanceCriteriaEvaluator{}
	req := Request{Task: task.Task{Profile: task.Profile{
		AcceptanceCriteria: []string{"only one thing"},
	}}}
	_, detail, _ := e.Evaluate(context.Background(), req)
	if !strings.Contains(detail, "1 acceptance criterion,") {
		t.Errorf("detail = %q, want the singular noun", detail)
	}
}

// Selection is not gated on domain: criteria describe what done means for
// whatever the task is, and only exist when the deepening pass ran.
func TestAcceptanceCriteriaSelectedForAnyDomainWhenPresent(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterDefaults(); err != nil {
		t.Fatal(err)
	}
	withCriteria := task.Profile{Domain: task.DomainGeneral, AcceptanceCriteria: []string{"x"}}
	names := map[string]bool{}
	for _, e := range r.ForTask(withCriteria) {
		names[e.Name()] = true
	}
	if !names["acceptance-criteria"] {
		t.Fatalf("a general task with criteria did not select the evaluator: %v", names)
	}

	// Without criteria it must not be selected at all — an always-on
	// NeedsReview row on every task would be noise, not signal.
	names = map[string]bool{}
	for _, e := range r.ForTask(task.Profile{Domain: task.DomainSoftware}) {
		names[e.Name()] = true
	}
	if names["acceptance-criteria"] {
		t.Fatalf("selected the evaluator with no criteria to report: %v", names)
	}
}

func TestCargoEvaluatorsSkipWithoutCargoToml(t *testing.T) {
	dir := t.TempDir()
	for _, e := range []Evaluator{&CargoBuildEvaluator{}, &CargoTestEvaluator{}} {
		outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: dir})
		if err != nil {
			t.Fatal(err)
		}
		if outcome != NeedsReview {
			t.Errorf("%s: outcome = %s (%s), want NeedsReview for a non-Rust workspace", e.Name(), outcome, detail)
		}
	}
}

// A Rust repo on a machine with no cargo installed is not a broken result.
func TestCargoEvaluatorsSkipWithoutTheToolchain(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err == nil {
		t.Skip("cargo is installed; this covers the machine that lacks it")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname = \"x\"\n"), 0o644)
	outcome, detail, err := (&CargoBuildEvaluator{}).Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s (%s), want NeedsReview when cargo is absent", outcome, detail)
	}
	if !strings.Contains(detail, "cargo not installed") {
		t.Errorf("detail = %q, want it to name the missing toolchain", detail)
	}
}

func TestPytestSkipsWithoutPythonMarkers(t *testing.T) {
	outcome, detail, err := (&PytestEvaluator{}).Evaluate(context.Background(), Request{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s (%s), want NeedsReview for a non-Python workspace", outcome, detail)
	}
}

// Every marker must count — a project with only requirements.txt is still a
// Python project, and gets past the marker gate to the toolchain check.
func TestPytestRecognisesEveryProjectMarker(t *testing.T) {
	for _, marker := range pythonMarkers {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, marker), []byte("\n"), 0o644)
		_, detail, err := (&PytestEvaluator{}).Evaluate(context.Background(), Request{CWD: dir})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "no Python project markers") {
			t.Errorf("%s did not mark the directory as a Python project: %s", marker, detail)
		}
	}
}

// pytest exits 5 when it collected no tests. That is "nothing to check", not
// a failing suite: a project with no tests yet has not broken anything.
func TestPytestNoTestsCollectedIsNotAFailure(t *testing.T) {
	if _, err := exec.LookPath("pytest"); err != nil {
		t.Skip("pytest not available")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"x\"\n"), 0o644)
	outcome, detail, err := (&PytestEvaluator{}).Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome == Fail {
		t.Fatalf("an empty test suite must not fail: %s", detail)
	}
}

func TestGoVetSkipsWithoutGoMod(t *testing.T) {
	outcome, detail, err := (&GoVetEvaluator{}).Evaluate(context.Background(), Request{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s (%s), want NeedsReview without a go.mod", outcome, detail)
	}
}

// A vet finding is a warning on the run, not a failed task: the finding may
// predate the task entirely, and failing every run against a repo that
// already had one would make the check worse than useless.
func TestGoVetFindingWarnsRatherThanFails(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module vetmod\n\ngo 1.21\n"), 0o644)
	// Printf with a mismatched verb — a classic vet finding that still compiles.
	os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Printf(\"%d\", \"not a number\") }\n"), 0o644)

	outcome, detail, err := (&GoVetEvaluator{}).Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PassWithWarnings {
		t.Fatalf("outcome = %s (%s), want PASS_WITH_WARNINGS — a lint finding must not fail the task", outcome, detail)
	}

	// Clean code reports a clean pass, so the warning above means something.
	os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Printf(\"%s\", \"fine\") }\n"), 0o644)
	outcome, detail, _ = (&GoVetEvaluator{}).Evaluate(context.Background(), Request{CWD: dir})
	if outcome != Pass {
		t.Fatalf("outcome = %s (%s), want Pass for clean code", outcome, detail)
	}
}

func TestNpmLintSkipsWithoutALintScript(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"test":"echo ok"}}`), 0o644)
	outcome, detail, err := (&NpmLintEvaluator{}).Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NeedsReview {
		t.Fatalf("outcome = %s (%s), want NeedsReview with no lint script", outcome, detail)
	}
}

func TestNpmLintFindingWarnsRatherThanFails(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not available")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"x","scripts":{"lint":"node -e \"process.exit(1)\""}}`), 0o644)
	outcome, detail, err := (&NpmLintEvaluator{}).Evaluate(context.Background(), Request{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PassWithWarnings {
		t.Fatalf("outcome = %s (%s), want PASS_WITH_WARNINGS", outcome, detail)
	}
}

// Every new evaluator is offered to software tasks, so an ecosystem the
// harness has a check for is never silently unverified.
func TestSoftwareTasksSelectEveryEcosystemEvaluator(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterDefaults(); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range r.ForTask(task.Profile{Domain: task.DomainSoftware, ModifiesFiles: true}) {
		names[e.Name()] = true
	}
	for _, want := range []string{
		"go-build", "go-test", "npm-build", "npm-test",
		"cargo-build", "cargo-test", "pytest", "go-vet", "npm-lint", "diff-check",
	} {
		if !names[want] {
			t.Errorf("software task did not select %q: %v", want, names)
		}
	}
}

// A missing toolchain is the operator's environment, not a broken result.
// go-build on a machine without Go used to report FAIL ("executable file
// not found"), which failed every task run against a Go repo on that
// machine. Every toolchain-backed evaluator must skip instead.
func TestEvaluatorsSkipWhenTheToolchainIsMissing(t *testing.T) {
	realLookPath := lookPath
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { lookPath = realLookPath })

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname = \"x\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"x\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"x","scripts":{"build":"true","test":"true","lint":"true"}}`), 0o644)

	for _, e := range []Evaluator{
		&GoBuildEvaluator{}, &GoTestEvaluator{}, &GoVetEvaluator{},
		&NpmBuildEvaluator{}, &NpmTestEvaluator{},
		&CargoBuildEvaluator{}, &CargoTestEvaluator{}, &PytestEvaluator{},
	} {
		outcome, detail, err := e.Evaluate(context.Background(), Request{CWD: dir})
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		if outcome != NeedsReview {
			t.Errorf("%s: outcome = %s (%s), want NeedsReview when its toolchain is absent", e.Name(), outcome, detail)
		}
		if !strings.Contains(detail, "not installed") {
			t.Errorf("%s: detail = %q, want it to name the missing toolchain", e.Name(), detail)
		}
	}
}

// The evidence check is a substring scan. It cannot tell an unsupported
// claim from a well-supported answer that cites the workspace rather than a
// URL — which is what research against a local repository produces — so
// finding nothing must warn rather than fail a task into repair cycles.
func TestEvidenceMissingCitationsWarnsRatherThanFails(t *testing.T) {
	e := &EvidenceEvaluator{}
	r := Request{Result: task.Result{
		Summary: "The parser lives in internal/parse and is called from the CLI entry point.",
		Output:  "The parser lives in internal/parse and is called from the CLI entry point.",
	}}
	outcome, detail, err := e.Evaluate(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PassWithWarnings {
		t.Fatalf("outcome = %s (%s), want PASS_WITH_WARNINGS — a substring scan must not fail a task", outcome, detail)
	}
	if !strings.Contains(detail, "cannot tell") {
		t.Errorf("detail = %q, want it to be honest about what the scan cannot determine", detail)
	}
}

// A cited answer still passes cleanly, so the warning above means something.
func TestEvidenceWithCitationsStillPasses(t *testing.T) {
	e := &EvidenceEvaluator{}
	r := Request{Result: task.Result{Output: "See https://go.dev/doc for the details."}}
	outcome, detail, err := e.Evaluate(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Pass {
		t.Fatalf("outcome = %s (%s), want Pass when references are present", outcome, detail)
	}
}
