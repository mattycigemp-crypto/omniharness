package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"omniharness/internal/config"
	"omniharness/internal/event"
	"omniharness/internal/runtime"
	"omniharness/internal/task"
	"omniharness/internal/testutil"
)

func newTestModel(t *testing.T) (*Model, *testutil.FakeOmniRoute) {
	t.Helper()
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "ok"})
	cfg := config.Default()
	cfg.Persistence.Dir = t.TempDir()
	rt, err := runtime.New(cfg, runtime.Options{Gateway: fake.Client()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return New(cfg, rt), fake
}

// update routes a message through the model, asserting the result stays a *Model.
func update(t *testing.T, m *Model, msg tea.Msg) (*Model, tea.Cmd) {
	t.Helper()
	got, cmd := m.Update(msg)
	nm, ok := got.(*Model)
	if !ok {
		t.Fatalf("update(%T) returned %T, want *Model", msg, got)
	}
	return nm, cmd
}

func publish(t *testing.T, m *Model, p event.Payload) *Model {
	t.Helper()
	e := event.New(p)
	e.SessionID = "s1"
	e.TaskID = "t1"
	e.AgentID = "aaaaaaaaaaaaaaaa"
	m, cmd := update(t, m, eventMsg{E: e})
	if cmd != nil {
		_ = cmd
	}
	return m
}

func TestModelTracksTaskAndAgents(t *testing.T) {
	m, _ := newTestModel(t)
	m = publish(t, m, &event.TaskCreatedData{Prompt: "fix the thing"})
	m = publish(t, m, &event.StrategySelectedData{Strategy: "direct", Reason: "simple", Steps: []string{"work"}})
	m = publish(t, m, &event.AgentCreatedData{Role: "implementer", Model: "cursor/m", TaskID: "t1", SessionID: "s1"})
	m = publish(t, m, &event.AgentStateData{Role: "implementer", Status: task.StatusRunning, Model: "cursor/m", Action: "thinking"})

	if m.strategy != "direct" {
		t.Fatalf("strategy = %q", m.strategy)
	}
	if len(m.agents) != 1 {
		t.Fatalf("agents = %d", len(m.agents))
	}
	if m.agents[0].Role != "implementer" || m.agents[0].State != "running" {
		t.Fatalf("agent %+v", m.agents[0])
	}
	if len(m.events) == 0 {
		t.Fatal("event stream empty")
	}
	view := m.View()
	if !strings.Contains(view, "direct") || !strings.Contains(view, "implementer") {
		t.Fatalf("view missing content:\n%s", view)
	}
}

func TestModelTaskCompletion(t *testing.T) {
	m, _ := newTestModel(t)
	m = publish(t, m, &event.TaskCreatedData{Prompt: "x"})
	m = publish(t, m, &event.TaskStateData{Status: task.StatusRunning})
	m = publish(t, m, &event.TaskCompletedData{Summary: "done", Output: "the output"})
	if m.status != task.StatusCompleted {
		t.Fatalf("status = %s", m.status)
	}
	if m.running {
		t.Fatal("should not be running")
	}
}

func TestModelTabCyclesViews(t *testing.T) {
	m, _ := newTestModel(t)
	start := m.view
	m, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatal("tab must not produce a command")
	}
	if m.view == start {
		t.Fatal("tab did not cycle view")
	}
}

func TestModelApprovalFlow(t *testing.T) {
	m, _ := newTestModel(t)
	req := &approvalReq{Tool: "git", Risk: "high", Reason: "git push", Reply: make(chan bool, 1)}
	m, _ = update(t, m, approvalMsg{Req: req})
	if m.approval == nil {
		t.Fatal("approval modal not shown")
	}
	if !strings.Contains(m.View(), "approval required") {
		t.Fatal("approval modal not rendered")
	}

	m, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("expected answer command")
	}
	ans, ok := cmd().(approvalAnswerMsg)
	if !ok {
		t.Fatalf("answer command produced %T, want approvalAnswerMsg", ans)
	}
	m, _ = update(t, m, ans)
	if got := <-req.Reply; !got {
		t.Fatal("expected grant")
	}
	_ = m
}

func TestModelDeniesApprovalOnN(t *testing.T) {
	m, _ := newTestModel(t)
	req := &approvalReq{Tool: "shell", Risk: "high", Reply: make(chan bool, 1)}
	m, _ = update(t, m, approvalMsg{Req: req})
	_, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if cmd == nil {
		t.Fatal("expected answer command")
	}
	ans, ok := cmd().(approvalAnswerMsg)
	if !ok {
		t.Fatalf("answer command produced %T, want approvalAnswerMsg", ans)
	}
	m, _ = update(t, m, ans)
	if got := <-req.Reply; got {
		t.Fatal("expected denial")
	}
	_ = m
}

func TestModelRendersWithoutTerminal(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	view := m.View()
	if !strings.Contains(view, "omniharness") {
		t.Fatalf("header missing: %s", view)
	}
	if !strings.Contains(view, "awaiting events") && !strings.Contains(view, "events") {
		t.Fatalf("event panel missing: %s", view)
	}
}

func TestModelRunningFlagBlocksDuplicateSubmission(t *testing.T) {
	m, _ := newTestModel(t)
	m.input.SetValue("do the task")
	m.inputFocused = true
	m2, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a task command")
	}
	if !m2.running {
		t.Fatal("running must be set synchronously on submit")
	}
	// A second submission while a task runs must be refused.
	m3, cmd2 := update(t, m2, tea.KeyMsg{Type: tea.KeyEnter})
	_ = m3
	if cmd2 != nil {
		t.Fatal("duplicate task submission must be blocked while running")
	}
	// Drive the task to completion.
	got := cmd()
	done, ok := got.(taskDoneMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want taskDoneMsg", got)
	}
	m4, _ := update(t, m2, done)
	if m4.running {
		t.Fatal("running must clear on completion")
	}
	// Exactly one session must exist for the submitted task — no orphans.
	sessions, err := m4.rt.ListSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want exactly 1 (no orphaned sessions)", len(sessions))
	}
}

func TestModelIgnoresOtherSessionsEvents(t *testing.T) {
	m, _ := newTestModel(t)
	m.sessionID = "mine"
	e := event.New(&event.AgentCreatedData{Role: "implementer"})
	e.SessionID = "other"
	m, _ = update(t, m, eventMsg{E: e})
	if len(m.agents) != 0 {
		t.Fatal("foreign session events must be ignored")
	}
}
