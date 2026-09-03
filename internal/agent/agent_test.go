package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	composer "omniharness/internal/context"
	"omniharness/internal/event"
	"omniharness/internal/gateway"
	"omniharness/internal/model"
	"omniharness/internal/policy"
	"omniharness/internal/session"
	"omniharness/internal/task"
	"omniharness/internal/testutil"
	"omniharness/internal/tools"
)

func testDeps(t *testing.T, fake *testutil.FakeOmniRoute, workspace string) Deps {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	reg := tools.NewRegistry()
	if err := tools.NewNative(workspace).Register(reg); err != nil {
		t.Fatal(err)
	}
	pol := policy.NewEngine(policy.Config{
		RiskAction: map[string]string{
			"low": "allow", "medium": "allow", "high": "ask", "critical": "block",
		},
		ShellAllowed:            true,
		GitPushRequiresApproval: true,
	}, nil)

	// A simple approver that grants everything (used only when policy asks).
	pol.SetApprover(policy.ApproverFunc(func(context.Context, policy.Request, string) (bool, error) {
		return true, nil
	}))

	return Deps{
		Bus:           event.NewBus(),
		Store:         store,
		Gateway:       fake.Client(),
		ModelSel:      model.NewSelector("fake/m1", nil),
		Tools:         reg,
		Policy:        pol,
		Composer:      composer.NewComposer(composer.Limits{CondenseAt: 1 << 18}),
		Roles:         DefaultRoles(),
		Workspace:     workspace,
		MaxIterations: 10,
	}
}

func runAgent(t *testing.T, deps Deps, spec task.Spec, role Role) (*Agent, error) {
	t.Helper()
	profile := (&task.Analyzer{}).Analyze(spec)
	ag := New(deps, "sess1", "task1", role, "", spec, profile)
	err := ag.Run(context.Background())
	return ag, err
}

func TestAgentTranscriptKeepsAssistantToolCallsMessage(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("c1", "read_file", `{"path":"a.txt"}`),
		}},
		testutil.FakeStep{Content: "done"},
	)
	deps := testDeps(t, fake, dir)
	ag := New(deps, "sess1", "task1", RoleImplementer, "", task.Spec{Prompt: "do the thing"}, task.Profile{})
	if err := ag.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if ag.Status != task.StatusCompleted {
		t.Fatalf("status = %s", ag.Status)
	}
	// Wire protocol invariant: every tool message must be preceded by the
	// assistant message that declared its tool_calls.
	var pendingToolCallIDs []string
	for _, m := range ag.Transcript {
		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				pendingToolCallIDs = append(pendingToolCallIDs, tc.ID)
			}
		case "tool":
			found := -1
			for i, id := range pendingToolCallIDs {
				if id == m.ToolCallID {
					found = i
					break
				}
			}
			if found < 0 {
				t.Fatalf("tool message %q has no preceding assistant tool_calls entry", m.ToolCallID)
			}
			pendingToolCallIDs = append(pendingToolCallIDs[:found], pendingToolCallIDs[found+1:]...)
		}
	}
}

func TestAgentCompletesWithToolLoop(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "greeting.txt"), []byte("hello"), 0o644)
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("call_1", "read_file", `{"path":"greeting.txt"}`),
		}},
		testutil.FakeStep{Content: "The file says hello."},
	)
	deps := testDeps(t, fake, dir)
	ag, err := runAgent(t, deps, task.Spec{Prompt: "read greeting.txt and summarize it"}, RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	if ag.Status != task.StatusCompleted {
		t.Fatalf("status = %s", ag.Status)
	}
	if !strings.Contains(ag.LastOutput(), "hello") {
		t.Fatalf("output %q", ag.LastOutput())
	}
	// Tool call must have been executed and recorded.
	calls, _ := deps.Store.ToolCalls("sess1")
	if len(calls) != 1 || calls[0].Tool != "read_file" || calls[0].Status != "completed" {
		t.Fatalf("tool calls %+v", calls)
	}
	// Model calls recorded with the resolved model ref.
	mcs, _ := deps.Store.ModelCalls("sess1")
	if len(mcs) != 2 {
		t.Fatalf("model calls = %d", len(mcs))
	}
	if mcs[0].Model != "fake/m1" {
		t.Fatalf("model = %q", mcs[0].Model)
	}
	// Transcript persisted for resumability.
	rec, err := deps.Store.Agent(ag.ID)
	if err != nil {
		t.Fatal(err)
	}
	var transcript []gateway.Message
	_ = json.Unmarshal(rec.Transcript, &transcript)
	if len(transcript) < 2 {
		t.Fatalf("transcript too short: %d", len(transcript))
	}
}

func TestAgentModelErrorFails(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	fake.FailChat = &gateway.Error{Kind: gateway.KindRateLimit, Status: 429, Message: "slow down"}
	deps := testDeps(t, fake, t.TempDir())
	_, err := runAgent(t, deps, task.Spec{Prompt: "do something"}, RoleImplementer)
	if err == nil {
		t.Fatal("expected model error")
	}
	ge := &gateway.Error{}
	if !strings.Contains(err.Error(), "omniroute") {
		t.Fatalf("error %v", err)
	}
	_ = ge
}

func TestAgentDeniedToolContinues(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("c1", "shell", `{"command":"echo blocked > x.txt"}`),
		}},
		testutil.FakeStep{Content: "done"},
	)
	deps := testDeps(t, fake, dir)
	// Deny everything via a blocking approver.
	deps.Policy.SetApprover(policy.ApproverFunc(func(context.Context, policy.Request, string) (bool, error) {
		return false, nil
	}))
	ag, err := runAgent(t, deps, task.Spec{Prompt: "create x.txt via shell"}, RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	if ag.Status != task.StatusCompleted {
		t.Fatalf("status = %s", ag.Status)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); err == nil {
		t.Fatal("file must not exist after denial")
	}
	calls, _ := deps.Store.ToolCalls("sess1")
	if len(calls) != 1 || calls[0].Status != "denied" {
		t.Fatalf("tool calls %+v", calls)
	}
}

func TestAgentCancellation(t *testing.T) {
	dir := t.TempDir()
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{Delay: 2 * time.Second},
	)
	deps := testDeps(t, fake, dir)
	ctx, cancel := context.WithCancel(context.Background())
	ag := New(deps, "sess1", "task1", RoleImplementer, "", task.Spec{Prompt: "slow"}, task.Profile{})

	done := make(chan error, 1)
	go func() { done <- ag.Run(ctx) }()
	time.Sleep(150 * time.Millisecond)
	ag.Cancel()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled && err != nil {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not stop")
	}
	if ag.Status != task.StatusCancelled && ag.Status != task.StatusFailed {
		t.Fatalf("status = %s", ag.Status)
	}
}

func TestAgentPauseResume(t *testing.T) {
	dir := t.TempDir()
	// The scripted model always requests a tool call, so the agent keeps
	// looping until cancelled — ideal for exercising pause/resume.
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("c1", "read_file", `{"path":"greeting.txt"}`),
		}, Delay: 100 * time.Millisecond},
	)
	deps := testDeps(t, fake, dir)
	deps.MaxIterations = 100
	ag := New(deps, "sess1", "task1", RoleImplementer, "", task.Spec{Prompt: "pause test"}, task.Profile{})

	done := make(chan error, 1)
	go func() { done <- ag.Run(context.Background()) }()

	// Let a couple of iterations happen, then pause.
	time.Sleep(350 * time.Millisecond)
	ag.Pause()
	time.Sleep(150 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("agent must not finish while paused")
	default:
	}
	countWhilePaused := fake.RequestCount()
	time.Sleep(200 * time.Millisecond)
	if fake.RequestCount() != countWhilePaused {
		t.Fatal("agent kept calling the model while paused")
	}

	ag.Resume()
	time.Sleep(250 * time.Millisecond)
	if fake.RequestCount() <= countWhilePaused {
		t.Fatal("agent did not resume making progress")
	}
	ag.Cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not stop after cancel")
	}
}

func TestAgentToolArgumentsDecoded(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "data.json"), []byte(`{"k":"v"}`), 0o644)
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("c1", "read_file", `{"path": "data.json"}`),
		}},
		testutil.FakeStep{Content: "finished"},
	)
	deps := testDeps(t, fake, dir)
	ag, err := runAgent(t, deps, task.Spec{Prompt: "read data.json"}, RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	if ag.Status != task.StatusCompleted {
		t.Fatalf("status = %s", ag.Status)
	}
	// The transcript must include the tool observation.
	found := false
	for _, m := range ag.Transcript {
		if m.Role == "tool" && strings.Contains(m.Content, `"k":"v"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("tool observation missing from transcript")
	}
}

// A role's ToolAllow is a hardcoded list, not derived from the tool
// registry — adding a new native tool does nothing for a role until that
// role's list names it explicitly. "remember" was added as a tool without
// updating any role's list, which silently denied every call to it (an
// error observation the model saw, not a task failure) until this was
// caught. Pinned here so a future tool cannot go dark the same way for
// every role at once.
func TestEveryDefaultRoleCanRemember(t *testing.T) {
	for role, cfg := range DefaultRoles() {
		found := false
		for _, name := range cfg.ToolAllow {
			if name == "remember" {
				found = true
			}
		}
		if !found {
			t.Errorf("role %s cannot call \"remember\": %v", role, cfg.ToolAllow)
		}
	}
}

// The same regression class as TestEveryDefaultRoleCanRemember, for the
// other tool that ships without needing a dependency wired up.
func TestEveryDefaultRoleCanRequestReplan(t *testing.T) {
	for role, cfg := range DefaultRoles() {
		found := false
		for _, name := range cfg.ToolAllow {
			if name == "request_replan" {
				found = true
			}
		}
		if !found {
			t.Errorf("role %s cannot call \"request_replan\": %v", role, cfg.ToolAllow)
		}
	}
}

func TestAgentRecordsAReplanRequest(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("call_1", "request_replan", `{"reason":"found two unrelated bugs, not one"}`),
		}},
		testutil.FakeStep{Content: "noted"},
	)
	deps := testDeps(t, fake, t.TempDir())
	ag, err := runAgent(t, deps, task.Spec{Prompt: "fix the bug"}, RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	if ag.Status != task.StatusCompleted {
		t.Fatalf("status = %s", ag.Status)
	}
	if got := ag.ReplanReason(); !strings.Contains(got, "found two unrelated bugs") {
		t.Fatalf("ReplanReason() = %q, want it to carry the tool call's reason", got)
	}
}

// The first request_replan call wins — a second one in the same run must
// not silently overwrite the reason the orchestrator will act on.
func TestAgentKeepsTheFirstReplanReason(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("call_1", "request_replan", `{"reason":"first reason"}`),
		}},
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{
			testutil.ToolCall("call_2", "request_replan", `{"reason":"second reason"}`),
		}},
		testutil.FakeStep{Content: "done"},
	)
	deps := testDeps(t, fake, t.TempDir())
	ag, err := runAgent(t, deps, task.Spec{Prompt: "fix the bug"}, RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	if got := ag.ReplanReason(); got != "replan requested: first reason" {
		t.Fatalf("ReplanReason() = %q, want the first request to have won", got)
	}
}

// A normal run — no request_replan call anywhere — must report no reason at
// all, not an empty-but-non-nil signal.
func TestAgentWithNoReplanRequestHasNoReason(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "done"})
	deps := testDeps(t, fake, t.TempDir())
	ag, err := runAgent(t, deps, task.Spec{Prompt: "fix the bug"}, RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	if got := ag.ReplanReason(); got != "" {
		t.Fatalf("ReplanReason() = %q, want empty", got)
	}
}
