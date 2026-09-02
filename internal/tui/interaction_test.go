package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"omniharness/internal/config"
)

// key builds the message Bubble Tea delivers for a keystroke.
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// answer runs the command handleKey returned and reports the approval it
// carries, so a test asserts on the answer actually sent rather than on the
// fact that some command came back.
func answer(t *testing.T, cmd tea.Cmd) (approvalAnswerMsg, bool) {
	t.Helper()
	if cmd == nil {
		return approvalAnswerMsg{}, false
	}
	msg, ok := cmd().(approvalAnswerMsg)
	return msg, ok
}

func TestApprovalModalGrantsOnlyOnYes(t *testing.T) {
	req := &approvalReq{Tool: "write_file", Risk: "high", Reason: "risk class high requires approval", Reply: make(chan bool, 1)}

	for _, k := range []string{"y", "Y"} {
		m := &Model{approval: req}
		_, cmd := m.handleKey(key(k))
		got, ok := answer(t, cmd)
		if !ok || !got.Grant {
			t.Errorf("%q: got %+v (sent=%v), want a grant", k, got, ok)
		}
	}

	for _, k := range []string{"n", "N", "esc", "ctrl+c"} {
		m := &Model{approval: req}
		_, cmd := m.handleKey(key(k))
		got, ok := answer(t, cmd)
		if !ok || got.Grant {
			t.Errorf("%q: got %+v (sent=%v), want a denial", k, got, ok)
		}
	}
}

func TestApprovalModalSwallowsEveryOtherKey(t *testing.T) {
	// A stray keystroke while the prompt is up must neither answer it nor
	// leak through to the task input underneath.
	for _, k := range []string{"a", "z", "1", "q", "enter", " "} {
		m := &Model{
			approval:     &approvalReq{Tool: "shell", Reply: make(chan bool, 1)},
			inputFocused: true,
		}
		_, cmd := m.handleKey(key(k))
		if got, ok := answer(t, cmd); ok {
			t.Errorf("%q answered the prompt with grant=%v", k, got.Grant)
		}
		if m.approval == nil {
			t.Errorf("%q dismissed the prompt without an answer", k)
		}
		if len(m.conversation) != 0 {
			t.Errorf("%q reached the transcript underneath: %+v", k, m.conversation)
		}
	}
}

func newBareModel() *Model {
	m := New(config.Default(), nil, "")
	m.conversation = nil // drop the greeting so assertions read cleanly
	m.overlay = OverlayNone
	return m
}

func TestPastedAPIKeyIsMaskedAndNotLeftInTheInput(t *testing.T) {
	const secret = "sk-live-abcdefghijklmnop-9999"
	m := newBareModel()
	m.keyInput = true
	m.input.SetValue(secret)

	m.applyKey(secret)

	// The transcript is on screen, gets screenshotted, and scrolls back.
	for _, line := range m.conversation {
		if strings.Contains(line.Text, secret) {
			t.Fatalf("the key was echoed into the transcript: %q", line.Text)
		}
	}
	if len(m.conversation) == 0 || !strings.Contains(m.conversation[0].Text, "key_9999") {
		t.Errorf("expected a masked confirmation, got %+v", m.conversation)
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("the key is still sitting in the input: %q", got)
	}
	if m.keyInput {
		t.Error("key entry mode must end once the key is applied")
	}
	if m.cfg.OmniRoute.APIKey != secret {
		t.Error("the key must actually be applied to the config")
	}
}

func TestEmptyKeyIsRejectedWithoutClobberingTheExistingOne(t *testing.T) {
	m := newBareModel()
	m.cfg.OmniRoute.APIKey = "sk-already-set"
	m.keyInput = true

	m.applyKey("")

	if m.cfg.OmniRoute.APIKey != "sk-already-set" {
		t.Errorf("an empty entry replaced a working key with %q", m.cfg.OmniRoute.APIKey)
	}
}

func TestSlashCommands(t *testing.T) {
	t.Run("help opens the overlay", func(t *testing.T) {
		m := newBareModel()
		m.handleCommand("/help")
		if m.overlay != OverlayHelp {
			t.Errorf("overlay = %v, want the help overlay", m.overlay)
		}
	})

	t.Run("unknown command says so instead of doing nothing", func(t *testing.T) {
		m := newBareModel()
		m.handleCommand("/wat")
		if len(m.conversation) != 1 || !strings.Contains(m.conversation[0].Text, "/wat") {
			t.Fatalf("conversation = %+v, want an error naming the command", m.conversation)
		}
		if m.conversation[0].Kind != chatError {
			t.Errorf("kind = %v, want it rendered as an error", m.conversation[0].Kind)
		}
	})

	t.Run("empty input is ignored", func(t *testing.T) {
		m := newBareModel()
		if cmd := m.handleCommand("   "); cmd != nil {
			t.Error("blank input must not do anything")
		}
		if len(m.conversation) != 0 {
			t.Errorf("blank input wrote %+v", m.conversation)
		}
	})

	t.Run("settings does not print the API key", func(t *testing.T) {
		m := newBareModel()
		m.cfg.OmniRoute.APIKey = "sk-secret-value"
		m.handleCommand("/settings")
		for _, line := range m.conversation {
			if strings.Contains(line.Text, "sk-secret-value") {
				t.Fatalf("/settings printed the key: %q", line.Text)
			}
		}
	})
}

func TestEscapeLeavesKeyEntryMode(t *testing.T) {
	m := newBareModel()
	m.keyInput = true
	m.inputFocused = true
	m.input.Placeholder = "paste your OmniRoute API key (sk-…)"

	m.handleKey(key("esc"))

	if m.keyInput || m.endpointInput || m.inputFocused {
		t.Errorf("esc left the model in key entry: keyInput=%v endpointInput=%v focused=%v",
			m.keyInput, m.endpointInput, m.inputFocused)
	}
	if m.input.Placeholder == "paste your OmniRoute API key (sk-…)" {
		t.Error("the placeholder must go back to the task prompt")
	}
}
