package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"omniharness/internal/combo"
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
	return New(cfg, rt, ""), fake
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
	// Switch to ViewMain so the sidebar renders agent details.
	m.view = ViewMain
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
	if !strings.Contains(view, "omniroute") {
		t.Fatalf("header missing: %s", view)
	}
	if !strings.Contains(view, "Tips") {
		t.Fatalf("home screen missing: %s", view)
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

func TestModelChatThreadBuildsConversation(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 120, 40
	m.view = ViewMain

	m = publish(t, m, &event.TaskCreatedData{Prompt: "build the feature"})
	m = publish(t, m, &event.StrategySelectedData{Strategy: "direct", Reason: "simple"})
	m = publish(t, m, &event.TaskCompletedData{Summary: "shipped it", Output: "details"})

	// Welcome messages (2 lines) precede the user prompt.
	if len(m.conversation) < 4 {
		t.Fatalf("conversation has %d lines, want >= 4", len(m.conversation))
	}
	// Find the user prompt in the conversation (after the welcome lines).
	found := false
	for _, c := range m.conversation {
		if c.Kind == chatUser && c.Text == "build the feature" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("user prompt 'build the feature' not found in conversation")
	}
	view := m.View()
	if !strings.Contains(view, "build the feature") {
		t.Fatalf("user bubble not rendered:\n%s", view)
	}
	if !strings.Contains(view, "shipped it") {
		t.Fatalf("result bubble not rendered:\n%s", view)
	}
	// Sidebar should show the strategy and session id.
	if !strings.Contains(view, "strategy") || !strings.Contains(view, "direct") {
		t.Fatalf("sidebar missing strategy:\n%s", view)
	}
}

func TestModelTypewriterRevealsResultProgressively(t *testing.T) {
	m, _ := newTestModel(t)
	full := "the final answer text"
	tsk := &task.Task{Status: task.StatusCompleted, Result: &task.Result{Summary: full}}
	m, _ = update(t, m, taskDoneMsg{Task: tsk})
	if m.running {
		t.Fatal("running must clear on completion")
	}
	if m.streamFull != full {
		t.Fatalf("streamFull = %q", m.streamFull)
	}
	// The first tick reveals a prefix; after enough ticks the whole text shows.
	m, _ = update(t, m, tickMsg{})
	if m.stream == "" || len(m.stream) >= len(full) {
		t.Fatalf("after first tick stream = %q (want a partial reveal)", m.stream)
	}
	if !strings.HasPrefix(full, m.stream) {
		t.Fatalf("stream %q is not a prefix of %q", m.stream, full)
	}
	for i := 0; i < 100 && m.streamIdx < len(full); i++ {
		m, _ = update(t, m, tickMsg{})
	}
	if m.stream != full {
		t.Fatalf("stream never reached full text: %q", m.stream)
	}
}

func TestModelSpinnerAdvancesOnTick(t *testing.T) {
	m, _ := newTestModel(t)
	f0 := m.frame
	m, _ = update(t, m, tickMsg{})
	if m.frame != f0+1 {
		t.Fatalf("frame did not advance: %d -> %d", f0, m.frame)
	}
}

func TestModelComboPickerNavigatesAndSets(t *testing.T) {
	m, _ := newTestModel(t)
	m.configPath = t.TempDir() + string(os.PathSeparator) + "cfg.toml"
	m.combos = []combo.Option{
		{ID: "auto/best-coding", Description: "coding", Kind: "auto"},
		{ID: "auto/best-reasoning", Description: "reasoning", Kind: "auto"},
	}
	m.combosLoading = false
	m.view = ViewCombo

	// Navigate to auto/best-reasoning and apply it.
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.comboSel != 1 {
		t.Fatalf("comboSel = %d", m.comboSel)
	}
	m2, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("pickCombo must not return a command")
	}
	if m2.cfg.Models.Default != "auto/best-reasoning" {
		t.Fatalf("cfg.Models.Default = %q", m2.cfg.Models.Default)
	}
	if m2.view != ViewMain {
		t.Fatalf("view = %s, want main", m2.view)
	}
	// Persisted to the config file (picker writes through config.Save), and
	// the API key must never be written.
	data, err := os.ReadFile(m2.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "auto/best-reasoning") {
		t.Fatalf("picked combo not persisted:\n%s", data)
	}
	if strings.Contains(string(data), "api_key") {
		t.Fatalf("combo picker wrote a secrets section:\n%s", data)
	}
}

func TestModelComboPickerCustomIdEntry(t *testing.T) {
	m, _ := newTestModel(t)
	m.configPath = t.TempDir() + string(os.PathSeparator) + "cfg.toml"
	m.combos = []combo.Option{{ID: "auto/best-coding", Description: "coding", Kind: "auto"}}
	m.combosLoading = false
	m.view = ViewCombo

	// Last row is the custom-id entry: select past the list and press enter.
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyDown}) // now at len(combos) == 1
	if m.comboSel != 1 {
		t.Fatalf("comboSel = %d, want 1 (custom entry)", m.comboSel)
	}
	m2, _ := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m2.modelInput || !m2.inputFocused || m2.view != ViewMain {
		t.Fatalf("model input mode not entered: %+v", m2)
	}

	// Type a specific model id and submit.
	m3, _ := update(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("openai/gpt-5.4")})
	m3, cmd3 := update(t, m3, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd3 != nil {
		t.Fatal("applyCombo must not return a command")
	}
	if m3.cfg.Models.Default != "openai/gpt-5.4" {
		t.Fatalf("cfg.Models.Default = %q", m3.cfg.Models.Default)
	}
	if m3.modelInput {
		t.Fatal("model input mode must clear after submit")
	}
}

func TestModelComboPickerRejectsMalformedId(t *testing.T) {
	m, _ := newTestModel(t)
	m.configPath = t.TempDir() + string(os.PathSeparator) + "cfg.toml"
	m.combos = []combo.Option{{ID: "auto/best-coding", Description: "coding", Kind: "auto"}}
	m.combosLoading = false
	m.view = ViewCombo
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("not-a-model")})
	m2, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("rejected id must not return a command")
	}
	if m2.cfg.Models.Default != "auto/best-coding" {
		t.Fatalf("malformed id must not change the combo (got %q)", m2.cfg.Models.Default)
	}
}

func TestModelComboPickerEscBackToMain(t *testing.T) {
	m, _ := newTestModel(t)
	m.view = ViewCombo
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.view != ViewHome {
		t.Fatalf("view = %s, want home", m.view)
	}
}

func TestModelTracksActualModelsUsed(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 120, 40

	m = publish(t, m, &event.ModelRequestedData{Model: "auto/best-coding", Reason: "config default"})
	m = publish(t, m, &event.ModelRespondedData{Model: "auto/best-coding", TokensIn: 100, TokensOut: 200, CostUSD: 0.001})
	m = publish(t, m, &event.ModelRequestedData{Model: "auto/best-reasoning", Reason: "repair escalation"})
	m = publish(t, m, &event.ModelFailedData{Model: "auto/best-reasoning", Error: "boom"})

	if len(m.modelStats) != 2 {
		t.Fatalf("modelStats = %d, want 2", len(m.modelStats))
	}
	coding := m.modelStats[0]
	if coding.ID != "auto/best-coding" || coding.Calls != 1 || coding.TokensIn != 100 || coding.TokensOut != 200 || coding.LastState != "ok" {
		t.Fatalf("coding stat %+v", coding)
	}
	reasoning := m.modelStats[1]
	if reasoning.Failures != 1 || reasoning.LastState != "failed" || reasoning.Reason != "repair escalation" {
		t.Fatalf("reasoning stat %+v", reasoning)
	}
	if m.lastModel != "auto/best-reasoning" {
		t.Fatalf("lastModel = %q", m.lastModel)
	}
	// Switch to ViewMain so the sidebar renders model stats.
	m.view = ViewMain
	view := m.View()
	if !strings.Contains(view, "auto/best-coding") || !strings.Contains(view, "auto/best-reasoning") {
		t.Fatalf("actual models not rendered:\n%s", view)
	}
}

func TestModelTaskResetClearsModelStats(t *testing.T) {
	m, _ := newTestModel(t)
	m = publish(t, m, &event.ModelRequestedData{Model: "auto/best-coding"})
	if len(m.modelStats) != 1 {
		t.Fatalf("modelStats = %d", len(m.modelStats))
	}
	m, _ = update(t, m, taskStartedMsg{SessionID: "s2", TaskID: "t2"})
	if len(m.modelStats) != 0 || m.lastModel != "" {
		t.Fatalf("model stats not reset: %d, last=%q", len(m.modelStats), m.lastModel)
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
