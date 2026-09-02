package benchmark

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"omniharness/internal/runtime"
	"omniharness/internal/session"
	"omniharness/internal/task"
)

func TestSummaryAggregatesPerModel(t *testing.T) {
	r := &Report{Results: []Result{
		{Model: "a/one", Passed: true, LatencyMS: 100, TokensIn: 10, TokensOut: 5, CostUSD: 0.01, Repairs: 1, ToolCalls: 2},
		{Model: "a/one", Passed: false, LatencyMS: 300, TokensIn: 20, TokensOut: 5, CostUSD: 0.02, Repairs: 0, ToolCalls: 1},
		{Model: "b/two", Passed: true, LatencyMS: 50, TokensIn: 1, TokensOut: 1, CostUSD: 0.5},
	}}

	got := r.Summary()
	if len(got) != 2 {
		t.Fatalf("Summary() returned %d models, want 2", len(got))
	}

	// Order follows first appearance, so a printed table is stable run to run.
	if got[0].Model != "a/one" || got[1].Model != "b/two" {
		t.Fatalf("model order = %q,%q, want a/one,b/two", got[0].Model, got[1].Model)
	}

	one := got[0]
	if one.Runs != 2 || one.Passes != 1 {
		t.Errorf("runs/passes = %d/%d, want 2/1", one.Runs, one.Passes)
	}
	if one.SuccessRate != 0.5 {
		t.Errorf("success rate = %v, want 0.5", one.SuccessRate)
	}
	if one.AvgLatencyMS != 200 {
		t.Errorf("avg latency = %d, want 200", one.AvgLatencyMS)
	}
	if one.TotalTokens != 40 {
		t.Errorf("total tokens = %d, want 40 (in and out summed)", one.TotalTokens)
	}
	if one.TotalCostUSD != 0.03 {
		t.Errorf("total cost = %v, want 0.03", one.TotalCostUSD)
	}
	if one.TotalRepairs != 1 || one.TotalToolCalls != 3 {
		t.Errorf("repairs/toolcalls = %d/%d, want 1/3", one.TotalRepairs, one.TotalToolCalls)
	}
}

func TestSummaryOfEmptyReport(t *testing.T) {
	// A run that measured nothing must render nothing, not divide by zero.
	if got := (&Report{}).Summary(); len(got) != 0 {
		t.Fatalf("Summary() of an empty report = %v, want empty", got)
	}
}

func caseByID(t *testing.T, id string) Case {
	t.Helper()
	for _, c := range BuiltinCases() {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("builtin case %q not found", id)
	return Case{}
}

func TestWriteFibCheck(t *testing.T) {
	c := caseByID(t, "write-fib")
	pass, detail := c.Check(&task.Task{Result: &task.Result{Artifacts: []string{"/tmp/x/fib.go"}}})
	if !pass {
		t.Errorf("fib.go among artifacts should pass, got %q", detail)
	}
	pass, detail = c.Check(&task.Task{Result: &task.Result{Artifacts: []string{"/tmp/x/main.go"}}})
	if pass {
		t.Error("a run that never wrote fib.go must not pass")
	}
	if detail == "" {
		t.Error("a failure must say why")
	}
	if pass, _ := c.Check(&task.Task{Result: &task.Result{}}); pass {
		t.Error("no artifacts at all must not pass")
	}
}

func TestExplainHTTPCheck(t *testing.T) {
	c := caseByID(t, "explain-http")
	// The model's casing is its own business; the check must not be fooled.
	pass, detail := c.Check(&task.Task{Result: &task.Result{
		Output: "HTTP/2 adds MULTIPLEXING over a single connection.",
	}})
	if !pass {
		t.Errorf("both terms present should pass, got %q", detail)
	}
	for _, output := range []string{
		"HTTP/1.1 opens one connection per request.", // no http/2, no multiplexing
		"HTTP/2 is a binary protocol.",               // no multiplexing
		"Multiplexing lets requests share a link.",   // no http/2
		"",
	} {
		if pass, _ := c.Check(&task.Task{Result: &task.Result{Output: output}}); pass {
			t.Errorf("output %q must not pass", output)
		}
	}
}

func TestRunRejectsAnEmptyMatrix(t *testing.T) {
	// Each of these used to produce an empty report and a nil error: the
	// benchmark measured nothing and a script gating on it carried on.
	cases := BuiltinCases()
	runner := &Runner{} // never reaches a runtime, because none of these run

	for _, tc := range []struct {
		name string
		opts RunOptions
	}{
		{"no models", RunOptions{}},
		{"unknown case", RunOptions{Models: []string{"a/one"}, Cases: []string{"no-such-case"}}},
		{"no cases at all", RunOptions{Models: []string{"a/one"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := cases
			if tc.name == "no cases at all" {
				in = nil
			}
			report, err := runner.Run(context.Background(), tc.opts, in)
			if err == nil {
				t.Fatalf("Run() = nil error with %d results, want an error", len(report.Results))
			}
		})
	}
}

func TestRunReportsUnknownCaseByName(t *testing.T) {
	_, err := (&Runner{}).Run(context.Background(),
		RunOptions{Models: []string{"a/one"}, Cases: []string{"write-fib", "typo-case"}},
		BuiltinCases())
	if err == nil {
		t.Fatal("a mistyped case id must be an error")
	}
	if got := err.Error(); !strings.Contains(got, "typo-case") || !strings.Contains(got, "write-fib") {
		t.Errorf("error %q should name the bad id and the available ones", got)
	}
}

func TestRunStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := (&Runner{}).Run(ctx, RunOptions{Models: []string{"a/one"}}, BuiltinCases())
	if err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if len(report.Results) != 0 {
		t.Errorf("a cancelled run must not report results, got %d", len(report.Results))
	}
}

func TestSelectCases(t *testing.T) {
	cases := BuiltinCases()
	got, unknown := selectCases(cases, nil)
	if len(got) != len(cases) || len(unknown) != 0 {
		t.Errorf("no filter should select everything, got %d/%d", len(got), len(cases))
	}

	got, unknown = selectCases(cases, []string{"explain-http"})
	if len(unknown) != 0 || len(got) != 1 || got[0].ID != "explain-http" {
		t.Errorf("filter selected %v (unknown %v)", got, unknown)
	}

	// Selection follows the declared order, not the order the ids were given,
	// so two invocations of the same set produce comparable reports.
	got, _ = selectCases(cases, []string{"explain-http", "write-fib"})
	if len(got) != 2 || got[0].ID != "write-fib" {
		t.Errorf("selection must keep declaration order, got %v", ids(got))
	}

	if _, unknown = selectCases(cases, []string{"nope", "write-fib"}); len(unknown) != 1 || unknown[0] != "nope" {
		t.Errorf("unknown = %v, want [nope]", unknown)
	}
}

func ids(cs []Case) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

// The three aggregation helpers read what the run actually recorded. They
// swallow store errors by design — a missing metric must not fail a benchmark
// that otherwise ran — so the risk is that they quietly aggregate the wrong
// rows. These exercise them against a real store.
func TestAggregationHelpersReadOnlyTheirOwnRows(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.CreateSession(&session.Session{ID: "sess-1"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.CreateSession(&session.Session{ID: "sess-2"}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	calls := []session.ModelCall{
		{ID: "m1", SessionID: "sess-1", TaskID: "task-1", TokensIn: 100, TokensOut: 10, CostUSD: 0.10},
		{ID: "m2", SessionID: "sess-1", TaskID: "task-1", TokensIn: 5, TokensOut: 2, CostUSD: 0.01},
		{ID: "m3", SessionID: "sess-1", TaskID: "task-2", TokensIn: 999, TokensOut: 999, CostUSD: 9.99},
	}
	for i := range calls {
		if err := store.RecordModelCall(&calls[i]); err != nil {
			t.Fatalf("record model call: %v", err)
		}
	}

	tools := []session.ToolCall{
		{ID: "t1", SessionID: "sess-1", TaskID: "task-1", Tool: "read_file", Status: "completed"},
		{ID: "t2", SessionID: "sess-1", TaskID: "task-1", Tool: "write_file", Status: "failed"},
		{ID: "t3", SessionID: "sess-1", TaskID: "task-2", Tool: "read_file", Status: "completed"},
		{ID: "t4", SessionID: "sess-2", TaskID: "task-1", Tool: "read_file", Status: "completed"},
	}
	for i := range tools {
		if err := store.RecordToolCall(&tools[i]); err != nil {
			t.Fatalf("record tool call: %v", err)
		}
	}

	rt := &runtime.Runtime{Store: store}

	if got := tokensFor(rt, "task-1", "in"); got != 105 {
		t.Errorf("tokensFor(in) = %d, want 105", got)
	}
	if got := tokensFor(rt, "task-1", "out"); got != 12 {
		t.Errorf("tokensFor(out) = %d, want 12", got)
	}
	if got := costFor(rt, "task-1"); got < 0.10999 || got > 0.11001 {
		t.Errorf("costFor = %v, want 0.11", got)
	}

	// A failed tool call still happened, and a benchmark comparing models on
	// tool use wants it counted.
	if got := toolCallsFor(rt, "sess-1", "task-1"); got != 2 {
		t.Errorf("toolCallsFor = %d, want 2 (another session's task-1 must not count)", got)
	}

	// A task with no rows reports zero rather than failing.
	if got := tokensFor(rt, "task-absent", "in"); got != 0 {
		t.Errorf("tokensFor of an unknown task = %d, want 0", got)
	}
	if got := toolCallsFor(rt, "sess-1", "task-absent"); got != 0 {
		t.Errorf("toolCallsFor of an unknown task = %d, want 0", got)
	}
}

// A case that cannot prepare its workspace must be reported as a failure with
// a reason, not abandon the whole run.
func TestRunReportsWorkspacePrepFailure(t *testing.T) {
	var prepped string
	cases := []Case{{
		ID:     "needs-prep",
		Prompt: "irrelevant",
		WorkspacePrep: func(dir string) error {
			prepped = dir
			return errors.New("disk full")
		},
	}}

	report, err := (&Runner{}).Run(context.Background(),
		RunOptions{Models: []string{"a/one"}, Iterations: 2}, cases)
	if err != nil {
		t.Fatalf("Run() = %v, want the failure carried in the results", err)
	}
	if len(report.Results) != 2 {
		t.Fatalf("got %d results, want one per iteration", len(report.Results))
	}
	for i, res := range report.Results {
		if res.Passed {
			t.Errorf("result %d passed despite prep failing", i)
		}
		if !strings.Contains(res.Detail, "disk full") {
			t.Errorf("result %d detail = %q, want the underlying reason", i, res.Detail)
		}
		if res.Iteration != i+1 {
			t.Errorf("result %d iteration = %d, want %d", i, res.Iteration, i+1)
		}
	}
	// The scratch directory is per-case and cleaned up behind it.
	if prepped == "" {
		t.Fatal("WorkspacePrep was never called")
	}
	if _, err := os.Stat(prepped); !os.IsNotExist(err) {
		t.Errorf("scratch dir %q outlived the run (stat err %v)", prepped, err)
	}
}
