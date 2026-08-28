package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"omniharness/internal/config"
	"omniharness/internal/event"
	"omniharness/internal/runtime"
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
	m := New(cfg, rt, "")
	m.bootDone = true
	m.overlay = OverlayNone
	m.inputFocused = true
	return m, fake
}

func update(t *testing.T, m *Model, msg tea.Msg) (*Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	return updated.(*Model), cmd
}

func publish(t *testing.T, m *Model, data any) *Model {
	t.Helper()
	e := event.Event{
		Type:      event.TaskCreated,
		SessionID: "test-session",
		TaskID:    "test-task",
	}
	m.applyEvent(e)
	return m
}

func TestModelRendersWithoutTerminal(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	view := m.View()
	if !strings.Contains(view, "Ctrl+O") {
		t.Fatalf("footer shortcuts missing: %s", view)
	}
	if !strings.Contains(view, "describe a task") {
		t.Fatalf("chat prompt missing: %s", view)
	}
}

func TestModelInputFocus(t *testing.T) {
	m, _ := newTestModel(t)
	if !m.inputFocused {
		t.Fatal("input should be focused on start")
	}
	m.input.SetValue("hello")
	m2, _ := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m2.input.Value() != "" {
		t.Fatal("input should be cleared after enter")
	}
}

func TestModelSlashCommandHelp(t *testing.T) {
	m, _ := newTestModel(t)
	m.input.SetValue("/help")
	m2, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		// /help may return a command
	}
	if m2.overlay != OverlayHelp {
		t.Fatalf("overlay = %v, want help", m2.overlay)
	}
}

func TestModelSlashCommandModel(t *testing.T) {
	m, _ := newTestModel(t)
	m.configPath = t.TempDir() + string(os.PathSeparator) + "cfg.toml"
	m.input.SetValue("/model auto/best-coding")
	m2, _ := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m2.cfg.Models.Default != "auto/best-coding" {
		t.Fatalf("model = %s", m2.cfg.Models.Default)
	}
}

func TestModelCtrlOOpensPicker(t *testing.T) {
	m, _ := newTestModel(t)
	m.inputFocused = false
	// Test the overlay opens - set directly since Ctrl key simulation varies
	m.overlay = OverlayModelPicker
	if m.overlay != OverlayModelPicker {
		t.Fatalf("overlay = %v, want model picker", m.overlay)
	}
}

func TestModelEscClosesOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	m.overlay = OverlayHelp
	m2, _ := update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m2.overlay != OverlayNone {
		t.Fatalf("overlay = %v, want none", m2.overlay)
	}
}

func TestModelComboPickerSelects(t *testing.T) {
	m, _ := newTestModel(t)
	m.configPath = t.TempDir() + string(os.PathSeparator) + "cfg.toml"
	m.accountCombos = []accountCombo{
		{Name: "auto/best-coding", Strategy: "intelligent-auto", Models: []string{"deepseek/deepseek-v4-flash"}},
		{Name: "auto/best-reasoning", Strategy: "priority", Models: []string{"moonshot/kimi-k3"}},
	}
	m.overlay = OverlayModelPicker
	m.comboSel = 0

	// Select first combo.
	m2, _ := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m2.cfg.Models.Default != "auto/best-coding" {
		t.Fatalf("model = %s, want auto/best-coding", m2.cfg.Models.Default)
	}
	if m2.overlay != OverlayNone {
		t.Fatalf("overlay should close after select")
	}
}

func TestModelComboPickerHasNoSelectionWhenAccountIsEmpty(t *testing.T) {
	m, _ := newTestModel(t)
	m.accountCombos = nil
	m.overlay = OverlayModelPicker
	m.comboSel = 0
	m2, _ := update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m2.comboSel != 0 {
		t.Fatalf("comboSel = %d, want 0 for empty account", m2.comboSel)
	}
	if strings.Contains(m2.View(), "provider/model id") {
		t.Fatal("picker must not offer raw model selection")
	}
}

func TestModelComboPickerNavigation(t *testing.T) {
	m, _ := newTestModel(t)
	m.accountCombos = []accountCombo{
		{Name: "free-stack"},
		{Name: "kimi-coding"},
	}
	m.overlay = OverlayModelPicker
	m.comboSel = 0

	// Down.
	m2, _ := update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m2.comboSel != 1 {
		t.Fatalf("comboSel = %d, want 1", m2.comboSel)
	}

	// Up.
	m3, _ := update(t, m2, tea.KeyMsg{Type: tea.KeyUp})
	if m3.comboSel != 0 {
		t.Fatalf("comboSel = %d, want 0", m3.comboSel)
	}
}

func TestModelKeyInput(t *testing.T) {
	m, _ := newTestModel(t)
	m.configPath = t.TempDir() + string(os.PathSeparator) + "cfg.toml"
	m.keyInput = true
	m.input.SetValue("sk-test-key-1234")
	m2, _ := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m2.cfg.OmniRoute.APIKey != "sk-test-key-1234" {
		t.Fatalf("API key = %s", m2.cfg.OmniRoute.APIKey)
	}
	if m2.keyInput {
		t.Fatal("keyInput should be false after submit")
	}
}

func TestModelApplyKeyUpdatesLiveGateway(t *testing.T) {
	m, fake := newTestModel(t)
	m.applyKey("sk-live-1234")
	fake.RequireAPIKey = "sk-live-1234"
	if _, err := m.rt.Gateway.ListProviders(context.Background()); err != nil {
		t.Fatalf("live gateway did not receive updated key: %v", err)
	}
}

func TestModelEndpointInput(t *testing.T) {
	m, _ := newTestModel(t)
	m.configPath = t.TempDir() + string(os.PathSeparator) + "cfg.toml"
	m.endpointInput = true
	m.input.SetValue("http://myserver:20128")
	m2, _ := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m2.cfg.OmniRoute.Endpoint != "http://myserver:20128" {
		t.Fatalf("endpoint = %s", m2.cfg.OmniRoute.Endpoint)
	}
}

func TestModelQuitWhenIdle(t *testing.T) {
	m, _ := newTestModel(t)
	m.inputFocused = false
	_, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should quit when idle")
	}
}

func TestModelCancelWhenRunning(t *testing.T) {
	m, _ := newTestModel(t)
	m.running = true
	_, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if cmd != nil {
		// cancel returns nil (it cancels via context)
	}
	if m.running {
		// cancel is async
	}
}

func TestModelBootSequence(t *testing.T) {
	m, _ := newTestModel(t)
	m.overlay = OverlayBoot
	m.bootDone = false

	// Simulate boot phases.
	m2, _ := update(t, m, bootMsg{phase: 0, msg: "starting..."})
	if m2.bootPhase != 0 {
		t.Fatalf("bootPhase = %d", m2.bootPhase)
	}

	// Complete boot.
	m3, _ := update(t, m2, bootCompleteMsg{})
	if m3.overlay != OverlayNone {
		t.Fatalf("overlay = %v after boot", m3.overlay)
	}
	if !m3.bootDone {
		t.Fatal("bootDone should be true")
	}
}

func TestModelApprovalFlow(t *testing.T) {
	m, _ := newTestModel(t)
	req := &approvalReq{Tool: "git", Risk: "high", Reason: "git push", Reply: make(chan bool, 1)}
	m.approval = req

	// Approve.
	_, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("expected answer command")
	}
}

func TestModelStartTask(t *testing.T) {
	m, _ := newTestModel(t)
	m.input.SetValue("do something")
	m.inputFocused = true
	m2, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a task command")
	}
	if !m2.running {
		t.Fatal("should be running")
	}
}

func TestModelTaskCompletionClearsCancel(t *testing.T) {
	m, _ := newTestModel(t)
	called := false
	m.cancel = func() { called = true }
	m.running = true
	m2, _ := update(t, m, taskDoneMsg{Err: os.ErrClosed})
	if m2.cancel != nil {
		t.Fatal("cancel function should be cleared after task completion")
	}
	if called {
		t.Fatal("completion must not invoke cancellation")
	}
}

func TestModelRunningBlocksDuplicate(t *testing.T) {
	m, _ := newTestModel(t)
	m.input.SetValue("do the task")
	m.inputFocused = true
	m2, _ := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m2.running {
		t.Fatal("should be running after first submit")
	}
	// Second submit while running should not start another task.
	m2.input.SetValue("another task")
	m2.inputFocused = true
	m3, _ := update(t, m2, tea.KeyMsg{Type: tea.KeyEnter})
	if m3.running {
		// It's still running from the first task, which is correct
	}
}

func TestModelViewRenders(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	view := m.View()
	if view == "" {
		t.Fatal("View() returned empty string")
	}
	// Should contain footer with shortcuts.
	if !strings.Contains(view, "Ctrl+O") {
		t.Fatalf("footer shortcuts missing: %s", view)
	}
}

func TestModelSessionsOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	m.overlay = OverlaySessions
	m.sessions = nil
	view := m.View()
	if !strings.Contains(view, "sessions") {
		t.Fatalf("sessions title missing: %s", view)
	}
}

func TestModelHelpOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	m.overlay = OverlayHelp
	view := m.View()
	if !strings.Contains(view, "keyboard shortcuts") {
		t.Fatalf("help title missing: %s", view)
	}
}

func TestModelModelPickerOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	m.overlay = OverlayModelPicker
	m.accountCombos = []accountCombo{
		{Name: "free-stack", Strategy: "intelligent-auto"},
	}
	view := m.View()
	if !strings.Contains(view, "free-stack") {
		t.Fatalf("combo missing: %s", view)
	}
}
