package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"omniharness/internal/combo"
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

var brandLogo = []string{
	"╔══════════════════════════════════════╗",
	"║          ◉  O M N I H A R N E S S    ║",
	"╚══════════════════════════════════════╝",
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m *Model) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

func hueToHex(h int) string {
	s, l := 0.72, 0.62
	c := (1 - abs(2*l-1)) * s
	x := c * (1 - abs(float64((h/60)%2)-1))
	var r, g, b float64
	switch h / 60 {
	case 0:
		r, g, b = c, x, 0
	case 1:
		r, g, b = x, c, 0
	case 2:
		r, g, b = 0, c, x
	case 3:
		r, g, b = 0, x, c
	case 4:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	mn := l - c/2
	return fmt.Sprintf("#%02X%02X%02X",
		int((r+mn)*255), int((g+mn)*255), int((b+mn)*255))
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
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
		blocks = append(blocks, m.styles.muted.Render("  describe a task to get started…"))
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

func (m *Model) renderUserBubble(text string) string {
	inner := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#EAF2FF")).
		Background(lipgloss.Color("#173A5E")).
		Padding(0, 1).
		MaxWidth(m.width - 4).
		Render(text)
	return m.styles.muted.Render("you") + "\n" + inner
}

func (m *Model) renderResultBubble(text string) string {
	body := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#DCE6F2")).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pOK).
		Padding(0, 1).
		MaxWidth(m.width - 2).
		Render(text)
	return m.styles.ok.Render("✓ result") + "\n" + body
}

func (m *Model) renderStreamingBubble(text string) string {
	body := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#DCE6F2")).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pAccent).
		Padding(0, 1).
		MaxWidth(m.width - 2).
		Render(text + m.spinner())
	return m.styles.accent.Render("◉ result") + "\n" + body
}

func (m *Model) renderErrorBubble(text string) string {
	body := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFD9D9")).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pErr).
		Padding(0, 1).
		MaxWidth(m.width - 2).
		Render(text)
	return m.styles.err.Render("✗") + "\n" + body
}

func (m *Model) renderHarnessLine(text, tm string) string {
	label := m.styles.accent.Render("●")
	time := m.styles.muted.Render(tm)
	body := lipgloss.NewStyle().MaxWidth(m.width - 6).Render(text)
	return label + " " + time + "  " + body
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
	return m.styles.accent.Render(m.spinner()) + " " + m.styles.muted.Render(action)
}

// renderFooter renders the input bar and status.
func (m *Model) renderFooter() string {
	if m.approval != nil {
		return m.styles.warn.Render("⚠ approval required  ") +
			m.styles.footer.Render("[y] approve    [n] deny")
	}

	input := m.input.View()
	if m.inputFocused {
		input = m.styles.accent.Render("> ") + m.input.View()
	} else {
		input = m.styles.muted.Render("⌨ ") + m.input.View()
	}

	status := m.styles.muted.Render(fmt.Sprintf("$%.3f  %s", m.metrics.CostUSD, m.cfg.Models.Default))
	shortcuts := m.styles.footer.Render("Ctrl+O model  Ctrl+A sessions  Ctrl+K key  ? help")

	return lipgloss.JoinHorizontal(lipgloss.Left,
		input,
		lipgloss.NewStyle().Width(m.width/4).Align(lipgloss.Center).Render(status),
		lipgloss.NewStyle().Width(m.width).Align(lipgloss.Right).Render(shortcuts),
	)
}

// renderBoot shows the animated boot sequence.
func (m *Model) renderBoot() string {
	var b strings.Builder
	b.WriteString("\n\n")

	hue := 200 + (m.frame*5)%60
	for _, l := range brandLogo {
		b.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(hueToHex(hue))).
			Bold(true).
			Render(l) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(m.styles.muted.Render("  v" + version.Version))
	b.WriteString("\n\n")

	totalPhases := 4
	filled := (m.bootPhase * 40) / totalPhases
	if filled > 40 {
		filled = 40
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", 40-filled)
	b.WriteString("  " + m.styles.accent.Render(bar) + "\n\n")

	for _, msg := range m.bootMsgs {
		if strings.HasPrefix(msg, "⚠") {
			b.WriteString("  " + m.styles.warn.Render(msg) + "\n")
		} else {
			b.WriteString("  " + m.styles.ok.Render(msg) + "\n")
		}
	}

	if !m.bootDone {
		b.WriteString("\n  " + m.spinner() + " " + m.styles.muted.Render("booting...") + "\n")
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
	b.WriteString(m.styles.muted.Render("↑↓ navigate  enter select  esc close") + "\n\n")

	if len(m.accountCombos) == 0 {
		b.WriteString(m.styles.muted.Render("no combos found on your OmniRoute account") + "\n")
		b.WriteString(m.styles.muted.Render("set up combos at https://omniroute.online") + "\n")
	} else {
		for i, c := range m.accountCombos {
			marker := "  "
			name := c.Name
			if i == m.comboSel {
				marker = m.styles.accent.Render("▸ ")
				name = m.styles.accent.Render(c.Name)
			}
			if c.Name == m.cfg.Models.Default {
				name = name + m.styles.ok.Render("  ✓")
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

	// Custom entry.
	marker := "  "
	label := m.styles.muted.Render("type a provider/model id…")
	if m.comboSel == len(m.accountCombos) {
		marker = m.styles.accent.Render("▸ ")
		label = m.styles.accent.Render("type a provider/model id…")
	}
	fmt.Fprintf(&b, "\n%s%s\n", marker, label)

	b.WriteString("\n" + m.styles.muted.Render("current: "+m.cfg.Models.Default))
	return b.String()
}

// renderSessionsOverlay shows the session list.
func (m *Model) renderSessionsOverlay() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("sessions") + "\n")
	b.WriteString(m.styles.muted.Render("↑↓ navigate  enter resume  esc close") + "\n\n")

	if len(m.sessions) == 0 {
		b.WriteString(m.styles.muted.Render("no sessions yet"))
	} else {
		for i, ss := range m.sessions {
			marker := "  "
			if i == m.selected {
				marker = m.styles.accent.Render("▸ ")
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
		return "→ " + d.Model
	case event.ModelResponded:
		var d event.ModelRespondedData
		decode(e, &d)
		return fmt.Sprintf("← %s %d+%d tok $%.4f", d.Model, d.TokensIn, d.TokensOut, d.CostUSD)
	case event.ModelFailed:
		var d event.ModelFailedData
		decode(e, &d)
		return "✗ " + d.Model + " " + truncate(d.Error, 80)
	case event.ToolRequested:
		var d event.ToolRequestedData
		decode(e, &d)
		return "tool " + d.Tool + " [" + d.Risk + "]"
	case event.ToolCompleted:
		var d event.ToolFinishedData
		decode(e, &d)
		return "✓ " + d.Tool
	case event.ToolFailed:
		var d event.ToolFailedData
		decode(e, &d)
		return "✗ tool " + d.Tool + ": " + truncate(d.Error, 80)
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

var _ = combo.Describe
