package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omniharness/internal/gateway"
	"omniharness/internal/task"
	"omniharness/internal/testutil"
)

// A model stuck re-writing the same file must be stopped rather than left to
// burn the whole budget. Watched happening against a live gateway.
func TestAgentStopsAModelSpinningOnOneCall(t *testing.T) {
	dir := t.TempDir()
	// The fake repeats its last step forever, so this agent never stops on its
	// own: every iteration asks for the identical write.
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("c1", "write_file", `{"path":"hello.txt","content":"hello"}`),
		}},
	)
	deps := testDeps(t, fake, dir)
	deps.MaxIterations = 50
	ag, err := runAgent(t, deps, task.Spec{Prompt: "write hello.txt"}, RoleImplementer)
	if err == nil {
		t.Fatal("spinning agent returned no error")
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("err = %v, want a stall", err)
	}
	if ag.Status != task.StatusFailed {
		t.Fatalf("status = %s", ag.Status)
	}
	// The work itself still happened: the guard stops the repetition, it does
	// not undo the first call.
	if b, err := os.ReadFile(filepath.Join(dir, "hello.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("file = %q, %v", b, err)
	}
	// Execution stops at the nudge threshold; the remaining calls are answered
	// without touching the workspace.
	calls, _ := deps.Store.ToolCalls("sess1")
	if len(calls) != repeatNudgeAt-1 {
		t.Fatalf("executed %d calls, want %d", len(calls), repeatNudgeAt-1)
	}
}

// The guard must not punish a legitimate re-run. Re-reading a file after
// changing it observes something new, and the change sits between the reads.
func TestAgentAllowsRepeatsSeparatedByOtherWork(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("before"), 0o644)
	read := testutil.ToolCall("c1", "read_file", `{"path":"a.txt"}`)
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{read}},
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("c2", "write_file", `{"path":"a.txt","content":"after"}`),
		}},
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{read}},
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("c3", "write_file", `{"path":"a.txt","content":"after"}`),
		}},
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{read}},
		testutil.FakeStep{Content: "done"},
	)
	deps := testDeps(t, fake, dir)
	ag, err := runAgent(t, deps, task.Spec{Prompt: "edit a.txt"}, RoleImplementer)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ag.Status != task.StatusCompleted {
		t.Fatalf("status = %s", ag.Status)
	}
	calls, _ := deps.Store.ToolCalls("sess1")
	if len(calls) != 5 {
		t.Fatalf("executed %d calls, want all 5", len(calls))
	}
}

// Two calls that differ only in how the arguments were serialized are the same
// call, and a spin dressed up in reordered keys is still a spin.
func TestFingerprintIgnoresArgumentFormatting(t *testing.T) {
	a := gateway.ToolCall{ID: "1"}
	a.Function.Name = "write_file"
	a.Function.Arguments = `{"path":"x.txt","content":"hi"}`
	b := gateway.ToolCall{ID: "2"}
	b.Function.Name = "write_file"
	b.Function.Arguments = "{ \"content\" : \"hi\" ,\n \"path\" : \"x.txt\" }"
	if fingerprint(a) != fingerprint(b) {
		t.Fatalf("same call fingerprinted differently:\n%q\n%q", fingerprint(a), fingerprint(b))
	}
	c := gateway.ToolCall{ID: "3"}
	c.Function.Name = "write_file"
	c.Function.Arguments = `{"path":"x.txt","content":"bye"}`
	if fingerprint(a) == fingerprint(c) {
		t.Fatal("different content fingerprinted the same")
	}
}

func TestRepeatTrackerResetsPerRun(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("c1", "read_file", `{"path":"a.txt"}`),
		}},
		testutil.FakeStep{Content: "done"},
	)
	deps := testDeps(t, fake, dir)
	ag := New(deps, "sess1", "task1", RoleImplementer, "", task.Spec{Prompt: "read a.txt"}, task.Profile{})
	// Prime the tracker with the very call this run is about to make: only a
	// genuine reset saves it, since a carried-over count would stop the agent
	// on its first tool call.
	primed := gateway.ToolCall{}
	primed.Function.Name = "read_file"
	primed.Function.Arguments = `{"path":"a.txt"}`
	ag.repeats = repeatTracker{last: fingerprint(primed), count: repeatStopAt - 1}
	if err := ag.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if ag.repeats.count != 1 {
		t.Fatalf("tracker not reset: %+v", ag.repeats)
	}
}
