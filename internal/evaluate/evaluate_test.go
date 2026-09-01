package evaluate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	outcome, _, _ = e.Evaluate(context.Background(), Request{Result: task.Result{Summary: "trust me"}})
	if outcome != Fail {
		t.Fatal("uncited research must fail")
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
	if !names["go-build"] || !names["go-test"] || !names["diff-check"] {
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
	if !names["go-build"] || !names["go-test"] {
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
