package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"omniharness/internal/event"
	"omniharness/internal/version"
)

var (
	pAccent = lipgloss.Color("#5EA5FF")
	pOK     = lipgloss.Color("#3DDC84")
	pErr    = lipgloss.Color("#FF6B6B")
	pWarn   = lipgloss.Color("#FFB020")
	pMuted  = lipgloss.Color("#8A93A3")
)

var spinnerFrames = []string{"|", "/", "-", "\\"}

func (m *Model) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

func last4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}

// View renders the full screen.
func (m *Model) View() string {
	if m.width == 0 {
		m.width = 100
	}
	if m.height == 0 {
		m.height = 24
	}

	// Boot screen.
	if m.overlay == OverlayBoot {
		return m.renderBoot()
	}

	// Main chat area.
	body := m.renderChat()

	// Footer with input and shortcuts.
	footer := m.renderFooter()

	content := lipgloss.JoinVertical(lipgloss.Left, body, footer)

	// Overlay on top.
	if m.overlay != OverlayNone {
		overlay := m.renderOverlay()
		content = m.placeOverlay(content, overlay)
	}

	return lipgloss.NewStyle().Width(m.width).Height(m.height).Render(content)
}

// placeOverlay centers the overlay on top of the content.
func (m *Model) placeOverlay(base, overlay string) string {
	overlayW := m.width * 3 / 4
	if overlayW > 60 {
		overlayW = 60
	}
	overlayH := m.height * 2 / 3
	if overlayH > 20 {
		overlayH = 20
	}

	// Wrap overlay in border.
	wrapped := m.styles.border.Width(overlayW).Render(overlay)

	// Pad to center vertically and horizontally.
	lines := strings.Split(wrapped, "\n")
	padTop := (m.height - len(lines)) / 2
	if padTop < 0 {
		padTop = 0
	}

	var out []string
	for i := 0; i < padTop; i++ {
		out = append(out, "")
	}
	out = append(out, lines...)
	for len(out) < m.height {
		out = append(out, "")
	}

	// Truncate to height.
	if len(out) > m.height {
		out = out[:m.height]
	}

	return lipgloss.JoinVertical(lipgloss.Left, out...)
}

// renderChat renders the conversation thread.
func (m *Model) renderChat() string {
	bodyH := m.height - 2
	if bodyH < 4 {
		bodyH = 4
	}

	var blocks []string
	for _, c := range m.conversation {
		switch c.Kind {
		case chatUser:
			blocks = append(blocks, m.renderUserBubble(c.Text))
		case chatResult:
			blocks = append(blocks, m.renderResultBubble(c.Text))
		case chatError:
			blocks = append(blocks, m.renderErrorBubble(c.Text))
		default:
			blocks = append(blocks, m.renderHarnessLine(c.Text, c.Time))
		}
	}

	// Streaming result.
	if m.streamFull != "" && m.streamIdx < len(m.streamFull) {
		blocks = append(blocks, m.renderStreamingBubble(m.stream))
	} else if m.streamFull != "" && m.stream != "" {
		blocks = append(blocks, m.renderResultBubble(m.streamFull))
	}

	// Live activity.
	if m.running {
		blocks = append(blocks, m.renderActivity())
	}

	if len(blocks) == 0 {
		blocks = append(blocks, m.styles.muted.Render("  describe a task to get started"))
	}

	// Flatten to lines, keep tail.
	var lines []string
	for _, b := range blocks {
		for _, l := range strings.Split(b, "\n") {
			lines = append(lines, l)
		}
	}
	if len(lines) > bodyH {
		lines = lines[len(lines)-bodyH:]
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// block renders a labelled body with a single left rule — no filled bubbles,
// no rounded borders.
func (m *Model) block(label string, labelStyle lipgloss.Style, rule lipgloss.Color, text string) string {
	body := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#DCE6F2")).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(rule).
		PaddingLeft(1).
		MaxWidth(m.width - 2).
		Render(text)
	return labelStyle.Render(label) + "\n" + body
}

func (m *Model) renderUserBubble(text string) string {
	return m.styles.accent.Render("> ") +
		lipgloss.NewStyle().MaxWidth(m.width-4).Render(text)
}

func (m *Model) renderResultBubble(text string) string {
	return m.block("[ result ]", m.styles.ok, pMuted, text)
}

func (m *Model) renderStreamingBubble(text string) string {
	return m.block("[ working ]", m.styles.accent, pAccent, text+" "+m.spinner())
}

func (m *Model) renderErrorBubble(text string) string {
	return m.block("[ error ]", m.styles.err, pErr, text)
}

func (m *Model) renderHarnessLine(text, tm string) string {
	return m.styles.muted.Render(tm) + "  " +
		lipgloss.NewStyle().MaxWidth(m.width-6).Render(text)
}

func (m *Model) renderActivity() string {
	var action string
	if len(m.agents) > 0 {
		a := m.agents[len(m.agents)-1]
		action = fmt.Sprintf("agent %s · %s", a.ID, a.State)
		if a.Action != "" {
			action += " · " + a.Action
		}
	} else {
		action = "working"
	}
	return m.styles.muted.Render(m.spinner() + " " + action)
}

// renderFooter renders the input bar and status.
func (m *Model) renderFooter() string {
	if m.approval != nil {
		return m.styles.warn.Render("approval required  ") +
			m.styles.footer.Render("[y] approve    [n] deny")
	}

	input := m.input.View()
	if m.inputFocused {
		input = m.styles.accent.Render("> ") + m.input.View()
	}

	status := m.styles.muted.Render(fmt.Sprintf("$%.3f  %s", m.metrics.CostUSD, m.cfg.Models.Default))
	shortcuts := m.styles.footer.Render("Ctrl+O model  Ctrl+A sessions  Ctrl+K key  ? help")

	return lipgloss.JoinHorizontal(lipgloss.Left,
		input,
		lipgloss.NewStyle().Width(m.width/4).Align(lipgloss.Center).Render(status),
		lipgloss.NewStyle().Width(m.width).Align(lipgloss.Right).Render(shortcuts),
	)
}

// renderBoot shows the boot sequence: a plain wordmark and the checks as they
// resolve. No colour cycling, no fake progress bar.
func (m *Model) renderBoot() string {
	var b strings.Builder
	b.WriteString("\n\n")

	b.WriteString("  " +
		m.styles.accent.Render("omni") +
		lipgloss.NewStyle().Bold(true).Render("harness") + "\n")
	b.WriteString("  " + m.styles.muted.Render("v"+version.Version) + "\n\n")

	total := 4
	step := m.bootPhase + 1
	if step > total {
		step = total
	}
	b.WriteString("  " + m.styles.muted.Render(fmt.Sprintf("step %d/%d", step, total)) + "\n\n")

	for _, msg := range m.bootMsgs {
		if strings.HasPrefix(msg, "!") {
			b.WriteString("  " + m.styles.warn.Render(msg) + "\n")
		} else {
			b.WriteString("  " + m.styles.ok.Render(msg) + "\n")
		}
	}

	if !m.bootDone {
		b.WriteString("\n  " + m.styles.muted.Render(m.spinner()+" booting") + "\n")
	}

	return b.String()
}

// renderOverlay renders the active overlay dialog.
func (m *Model) renderOverlay() string {
	switch m.overlay {
	case OverlayModelPicker:
		return m.renderModelPicker()
	case OverlaySessions:
		return m.renderSessionsOverlay()
	case OverlayHelp:
		return m.renderHelpOverlay()
	}
	return ""
}

// renderModelPicker shows only user's account combos.
func (m *Model) renderModelPicker() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("pick a combo") + "\n")
	b.WriteString(m.styles.muted.Render("up/down navigate  enter select  esc close") + "\n\n")

	if len(m.accountCombos) == 0 {
		b.WriteString(m.styles.muted.Render("no combos found on your OmniRoute account") + "\n")
		b.WriteString(m.styles.muted.Render("set up combos at https://omniroute.online") + "\n")
	} else {
		for i, c := range m.accountCombos {
			marker := "  "
			name := c.Name
			if i == m.comboSel {
				marker = m.styles.accent.Render("> ")
				name = m.styles.accent.Render(c.Name)
			}
			if c.Name == m.cfg.Models.Default {
				name = name + m.styles.ok.Render("  *")
			}
			strategy := ""
			if c.Strategy != "" {
				strategy = " " + m.styles.muted.Render("["+c.Strategy+"]")
			}
			fmt.Fprintf(&b, "%s%s%s\n", marker, name, strategy)
			if len(c.Models) > 0 {
				models := strings.Join(c.Models, " · ")
				if len(models) > 50 {
					models = models[:47] + "…"
				}
				fmt.Fprintf(&b, "    %s\n", m.styles.muted.Render(models))
			}
		}
	}

	b.WriteString("\n" + m.styles.muted.Render("current: "+m.cfg.Models.Default))
	return b.String()
}

// renderSessionsOverlay shows the session list.
func (m *Model) renderSessionsOverlay() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("sessions") + "\n")
	b.WriteString(m.styles.muted.Render("up/down navigate  enter resume  esc close") + "\n\n")

	if len(m.sessions) == 0 {
		b.WriteString(m.styles.muted.Render("no sessions yet"))
	} else {
		for i, ss := range m.sessions {
			marker := "  "
			if i == m.selected {
				marker = m.styles.accent.Render("> ")
			}
			fmt.Fprintf(&b, "%s%-9s %-40s %s\n", marker, ss.Status, truncate(ss.Title, 40),
				ss.CreatedAt.Local().Format("2006-01-02 15:04"))
		}
	}
	return b.String()
}

// renderHelpOverlay shows keyboard shortcuts.
func (m *Model) renderHelpOverlay() string {
	rows := [][2]string{
		{"enter", "send message / focus input"},
		{"i", "focus input"},
		{"esc", "unfocus input / close overlay"},
		{"c", "cancel running task"},
		{"Ctrl+O", "pick model combo"},
		{"Ctrl+A", "switch session"},
		{"Ctrl+K", "set API key"},
		{"Ctrl+E", "change endpoint"},
		{"?", "toggle this help"},
		{"q / Ctrl+C", "quit"},
	}

	var b strings.Builder
	b.WriteString(m.styles.title.Render("keyboard shortcuts") + "\n\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-16s %s\n", r[0], r[1])
	}
	b.WriteString("\n" + m.styles.muted.Render("esc or ? to close"))
	return b.String()
}

func compactEvent(e event.Event) string {
	switch e.Type {
	case event.StrategySelected:
		var d event.StrategySelectedData
		decode(e, &d)
		return d.Strategy
	case event.AgentCreated:
		var d event.AgentCreatedData
		decode(e, &d)
		return fmt.Sprintf("agent %s (%s)", shortID(e.AgentID), d.Role)
	case event.AgentUpdated, event.AgentCompleted, event.AgentFailed, event.AgentPaused, event.AgentCancelled:
		var d event.AgentStateData
		decode(e, &d)
		s := string(d.Status)
		if d.Action != "" {
			s += " " + d.Action
		}
		return s
	case event.ModelRequested:
		var d event.ModelRequestedData
		decode(e, &d)
		return "-> " + d.Model
	case event.ModelResponded:
		var d event.ModelRespondedData
		decode(e, &d)
		return fmt.Sprintf("<- %s %d+%d tok $%.4f", d.Model, d.TokensIn, d.TokensOut, d.CostUSD)
	case event.ModelFailed:
		var d event.ModelFailedData
		decode(e, &d)
		return "failed " + d.Model + " " + truncate(d.Error, 80)
	case event.ToolRequested:
		var d event.ToolRequestedData
		decode(e, &d)
		return "tool " + d.Tool + " [" + d.Risk + "]"
	case event.ToolCompleted:
		var d event.ToolFinishedData
		decode(e, &d)
		return "ok " + d.Tool
	case event.ToolFailed:
		var d event.ToolFailedData
		decode(e, &d)
		return "failed tool " + d.Tool + ": " + truncate(d.Error, 80)
	case event.TaskCompleted:
		return "task completed"
	case event.TaskFailed:
		var d event.TaskFailedData
		decode(e, &d)
		return "task failed: " + truncate(d.Error, 80)
	case event.TaskCancelled:
		return "task cancelled"
	default:
		return ""
	}
}

func formatDurationMS(ms int64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		return fmt.Sprintf("%dm%02ds", ms/60000, (ms%60000)/1000)
	}
}
