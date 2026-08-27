package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"omniharness/internal/combo"
	"omniharness/internal/event"
	"omniharness/internal/version"
)

// ---------------------------------------------------------------------------
// Palette + animation helpers
// ---------------------------------------------------------------------------

var (
	pAccent   = lipgloss.Color("#5EA5FF") // Gemini-ish blue
	pAccentBg = lipgloss.Color("#14263F")
	pOK       = lipgloss.Color("#3DDC84")
	pErr      = lipgloss.Color("#FF6B6B")
	pWarn     = lipgloss.Color("#FFB020")
	pMuted    = lipgloss.Color("#8A93A3")
	pBorder   = lipgloss.Color("#2E3A4C")
	pBg       = lipgloss.Color("#0B1018")
	pUserBg   = lipgloss.Color("#173A5E")
)

// brandLogo is the compact logo shown on the home screen.
var brandLogo = []string{
	"  ╔══════════════════════════════════════╗",
	"  ║          ◉  O M N I H A R N E S S    ║",
	"  ╚══════════════════════════════════════╝",
}

// spinnerFrames is the activity spinner (braille).
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m *Model) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

// hueToHex converts an HSL hue (degrees) to a #rrggbb hex at fixed
// saturation/lightness — used for the animated brand gradient.
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

// last4 returns the last 4 characters of a string, used for masking secrets.
func last4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}

// ---------------------------------------------------------------------------
// Top-level view
// ---------------------------------------------------------------------------

// View renders the full screen.
func (m *Model) View() string {
	if m.width == 0 {
		m.width = 100
	}
	if m.height == 0 {
		m.height = 24
	}
	var body string
	switch m.view {
	case ViewHome:
		body = m.renderHome()
	case ViewMain:
		body = m.renderMain()
	case ViewAgents:
		body = m.renderAgents()
	case ViewGraph:
		body = m.renderGraph()
	case ViewRouting:
		body = m.renderRouting()
	case ViewSessions:
		body = m.renderSessions()
	case ViewCombo:
		body = m.renderCombo()
	case ViewHelp:
		body = m.renderHelp()
	}
	header := m.renderHeader()
	footer := m.renderFooter()
	content := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	return lipgloss.NewStyle().Width(m.width).Height(m.height).Render(content)
}

// ---------------------------------------------------------------------------
// Header
// ---------------------------------------------------------------------------

// viewLabels maps view indices to short labels for the tab bar.
var viewLabels = []string{"home", "chat", "agents", "graph", "route", "sessions", "combos", "help"}

func (m *Model) renderHeader() string {
	// Animated brand: hue drifts slowly across the blue range.
	hue := 200 + (m.frame*3)%70
	brand := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(hueToHex(hue))).
		Render("◉ omniharness")

	// View tabs.
	var tabs []string
	for i := 0; i < int(viewCount); i++ {
		label := viewLabels[i]
		if View(i) == m.view {
			tabs = append(tabs, m.styles.accent.Render("["+label+"]"))
		} else {
			tabs = append(tabs, m.styles.muted.Render(label))
		}
	}
	tabBar := strings.Join(tabs, "  ")

	status := string(m.status)
	statusStyle := m.styles.muted
	switch m.status {
	case "running":
		status = m.spinner() + " " + status
		statusStyle = m.styles.accent
	case "completed":
		status = "✓ " + status
		statusStyle = m.styles.ok
	case "failed", "cancelled":
		status = "✗ " + status
		statusStyle = m.styles.err
	case "pending", "":
		status = "ready"
		statusStyle = m.styles.muted
	}

	var right []string
	if m.strategy != "" {
		right = append(right, m.styles.muted.Render("strategy "+m.strategy))
	}
	if m.lastModel != "" {
		right = append(right, m.styles.accent.Render("model "+m.lastModel))
	}
	if len(m.agents) > 0 {
		right = append(right, m.styles.muted.Render("agents "+fmt.Sprint(len(m.agents))))
	}
	right = append(right, m.styles.muted.Render("$"+fmt.Sprintf("%.3f", m.metrics.CostUSD)))
	right = append(right, m.styles.muted.Render("session "+shortID(m.sessionID)))

	left := brand + "   " + statusStyle.Render(status)
	return lipgloss.JoinHorizontal(lipgloss.Left, left, " "+tabBar, lipgloss.NewStyle().Width(m.width).Align(lipgloss.Right).Render(strings.Join(right, "  ")))
}

// ---------------------------------------------------------------------------
// Footer
// ---------------------------------------------------------------------------

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

	// Status bar info (cost and model)
	statusInfo := m.styles.muted.Render(fmt.Sprintf("$%.3f (sub) %s", m.metrics.CostUSD, m.cfg.Models.Default))

	var keys []string
	switch m.view {
	case ViewHome:
		keys = []string{"enter start", "k api key", "e endpoint", "p pick model", "tab views"}
	case ViewMain:
		keys = []string{"enter run", "i input", "k api key", "e endpoint", "c cancel", "p combo", "tab views", "q quit"}
	case ViewSessions:
		keys = []string{"↑↓ select", "enter resume", "tab views", "q quit"}
	case ViewCombo:
		keys = []string{"↑↓ select", "enter set", "esc back"}
	}
	hint := m.styles.footer.Render(strings.Join(keys, "  ·  "))

	// Layout: input [status] [shortcuts]
	return lipgloss.JoinHorizontal(lipgloss.Left,
		input,
		lipgloss.NewStyle().Width(m.width/3).Align(lipgloss.Center).Render(statusInfo),
		lipgloss.NewStyle().Width(m.width).Align(lipgloss.Right).Render(hint),
	)
}

// renderHome shows the welcome screen with tips and model info.
func (m *Model) renderHome() string {
	var b strings.Builder
	b.WriteString("\n")
	for _, l := range brandLogo {
		b.WriteString(m.styles.accent.Render(l) + "\n")
	}
	b.WriteString("\n  " + m.styles.muted.Render("v"+version.Version+" — free AI gateway for multi-provider LLMs"))
	b.WriteString("\n  " + m.styles.muted.Render("gateway: "+m.cfg.OmniRoute.Endpoint))
	b.WriteString("\n")

	// API key status
	if m.cfg.OmniRoute.APIKey != "" {
		b.WriteString("  " + m.styles.ok.Render("✓ connected") + m.styles.muted.Render(" (key_"+last4(m.cfg.OmniRoute.APIKey)+")"))
	} else {
		b.WriteString("  " + m.styles.warn.Render("⚠ no API key") + m.styles.muted.Render(" — press 'k' or type /key"))
	}
	b.WriteString("\n")

	// Current combo
	b.WriteString("  " + m.styles.muted.Render("combo: "+m.styles.accent.Render(m.cfg.Models.Default)))
	b.WriteString("\n\n")

	// Tips
	b.WriteString("  " + m.styles.accent.Render("Getting started") + "\n")
	b.WriteString("    Type a task in the input bar below and press enter\n")
	b.WriteString("    Press 'p' to choose a model combo\n")
	b.WriteString("    Press 'k' to set your OmniRoute API key\n")
	b.WriteString("    Press 'e' to change the OmniRoute endpoint\n")
	b.WriteString("    Press '?' for all shortcuts\n")
	b.WriteString("\n")

	// Input hint
	b.WriteString("  " + m.styles.muted.Render("↓ type below to begin"))
	return b.String()
}

// ---------------------------------------------------------------------------
// Main: chat layout (conversation + status sidebar)
// ---------------------------------------------------------------------------

func (m *Model) renderMain() string {
	if m.approval != nil {
		return m.renderApproval()
	}
	bodyH := m.height - 3
	if bodyH < 6 {
		bodyH = 6
	}
	rightW := m.width * 2 / 5
	if rightW < 24 {
		rightW = 24
	}
	if m.width-40 > 12 && rightW > m.width-40 {
		rightW = m.width - 40
	}
	leftW := m.width - rightW - 2
	if leftW < 20 {
		leftW = 20
	}
	// Narrow terminals: never let the panes exceed the screen; lipgloss
	// truncates, but negative/absurd widths would panic.
	if leftW > m.width-2 {
		leftW = m.width - 2
	}
	if rightW > m.width-2 {
		rightW = m.width - 2
	}

	conversation := m.renderConversation(leftW, bodyH)
	sidebar := m.renderSidebar(rightW, bodyH)

	body := lipgloss.JoinHorizontal(lipgloss.Top, conversation, sidebar)
	return lipgloss.NewStyle().Width(m.width).Height(bodyH).Render(body)
}

// renderConversation renders the chat thread with bubbles, the live activity
// line and the streaming result, scrolled to fit avail lines.
func (m *Model) renderConversation(w, avail int) string {
	var blocks []string

	// Bubble for each conversation entry.
	for _, c := range m.conversation {
		switch c.Kind {
		case chatUser:
			blocks = append(blocks, m.renderUserBubble(c.Text, w))
		case chatResult:
			blocks = append(blocks, m.renderResultBubble(c.Text, w))
		case chatError:
			blocks = append(blocks, m.renderErrorBubble(c.Text, w))
		default:
			blocks = append(blocks, m.renderHarnessLine(c.Text, c.Time, w))
		}
	}

	// Streaming result (typewriter).
	if m.streamFull != "" && m.streamIdx < len(m.streamFull) {
		blocks = append(blocks, m.renderStreamingBubble(m.stream, w))
	} else if m.streamFull != "" && m.stream != "" {
		blocks = append(blocks, m.renderResultBubble(m.streamFull, w))
	}

	// Live activity while a task runs.
	if m.running {
		blocks = append(blocks, m.renderActivity(w))
	}

	if len(blocks) == 0 {
		blocks = append(blocks, m.styles.muted.Render("  awaiting events — describe a task and press enter"))
	}

	// Flatten to lines, then keep the tail that fits the pane.
	var lines []string
	for _, b := range blocks {
		for _, l := range strings.Split(b, "\n") {
			lines = append(lines, l)
		}
	}
	if len(lines) > avail {
		lines = lines[len(lines)-avail:]
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m *Model) renderUserBubble(text string, w int) string {
	inner := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#EAF2FF")).
		Background(pUserBg).
		Padding(0, 1).
		MaxWidth(w - 4).
		Render(text)
	// Right-align each line within the pane (Gemini style).
	var out []string
	for _, l := range strings.Split(inner, "\n") {
		out = append(out, lipgloss.NewStyle().Width(w).Align(lipgloss.Right).Render(l))
	}
	label := lipgloss.NewStyle().Width(w).Align(lipgloss.Right).Render(m.styles.muted.Render("you"))
	return label + "\n" + strings.Join(out, "\n")
}

func (m *Model) renderResultBubble(text string, w int) string {
	body := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#DCE6F2")).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pOK).
		Padding(0, 1).
		MaxWidth(w - 2).
		Render(text)
	return m.styles.ok.Render("✓ result") + "\n" + body
}

func (m *Model) renderStreamingBubble(text string, w int) string {
	body := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#DCE6F2")).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pAccent).
		Padding(0, 1).
		MaxWidth(w - 2).
		Render(text + m.spinner())
	return m.styles.accent.Render("◉ result") + "\n" + body
}

func (m *Model) renderErrorBubble(text string, w int) string {
	body := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFD9D9")).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pErr).
		Padding(0, 1).
		MaxWidth(w - 2).
		Render(text)
	return m.styles.err.Render("✗") + "\n" + body
}

func (m *Model) renderHarnessLine(text, tm string, w int) string {
	label := m.styles.accent.Render("●")
	time := m.styles.muted.Render(tm)
	body := lipgloss.NewStyle().MaxWidth(w - 6).Render(text)
	return label + " " + time + "  " + body
}

// renderActivity is the live "what is happening now" line with a spinner.
func (m *Model) renderActivity(w int) string {
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
	line := m.styles.accent.Render(m.spinner()) + " " + m.styles.muted.Render(action)
	return lipgloss.NewStyle().MaxWidth(w).Render(line)
}

// renderSidebar renders the live status card + event tail.
func (m *Model) renderSidebar(w, avail int) string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("task") + "\n")
	fmt.Fprintf(&b, "  %s\n", truncate(m.prompt, w-4))
	if m.strategy != "" {
		fmt.Fprintf(&b, "  %s %s\n", m.styles.muted.Render("strategy"), m.styles.accent.Render(m.strategy))
		if m.strategyReason != "" {
			fmt.Fprintf(&b, "  %s\n", m.styles.muted.Render("↳ "+truncate(m.strategyReason, w-6)))
		}
	}
	mm := m.metrics
	fmt.Fprintf(&b, "  %s %d\n", m.styles.muted.Render("agents"), len(m.agents))
	if len(m.modelStats) > 0 {
		b.WriteString("  " + m.styles.muted.Render("models") + "\n")
		shown := len(m.modelStats)
		extra := 0
		if shown > 3 {
			extra = shown - 3
			shown = 3
		}
		for i := len(m.modelStats) - shown; i < len(m.modelStats); i++ {
			s := m.modelStats[i]
			b.WriteString("    " + m.modelMarker(s) + " " + truncate(s.ID, w-30) +
				m.styles.muted.Render(fmt.Sprintf(" %dx %d+%d $%.4f", s.Calls, s.TokensIn, s.TokensOut, s.Cost)) + "\n")
		}
		if extra > 0 {
			b.WriteString("    " + m.styles.muted.Render(fmt.Sprintf("…+%d more (routing view)", extra)) + "\n")
		}
	}
	fmt.Fprintf(&b, "  %s %s\n", m.styles.muted.Render("tokens"), fmt.Sprintf("%d+%d", mm.TokensIn, mm.TokensOut))
	fmt.Fprintf(&b, "  %s $%.4f\n", m.styles.muted.Render("cost"), mm.CostUSD)
	fmt.Fprintf(&b, "  %s %s\n", m.styles.muted.Render("latency"), formatDurationMS(mm.LatencyMS))
	if m.repairs > 0 {
		fmt.Fprintf(&b, "  %s %d\n", m.styles.warn.Render("repairs"), m.repairs)
	}
	fmt.Fprintf(&b, "  %s %s\n", m.styles.muted.Render("combo"), m.cfg.Models.Default)

	b.WriteString("\n" + m.styles.title.Render("events") + "\n")
	start := 0
	limit := avail - 12
	if limit < 3 {
		limit = 3
	}
	if len(m.events) > limit {
		start = len(m.events) - limit
	}
	for _, e := range m.events[start:] {
		fmt.Fprintf(&b, "  %s %s\n", m.styles.muted.Render(e.Time), truncate(e.Text, w-6))
	}
	if b.Len() == 0 {
		b.WriteString(m.styles.muted.Render("  awaiting events…"))
	}
	return m.styles.border.Render(b.String())
}

// renderApproval shows the approval modal.
func (m *Model) renderApproval() string {
	a := m.approval
	panel := fmt.Sprintf("%s  tool: %s  risk: %s\nreason: %s\n\n[y] approve    [n] deny",
		m.styles.warn.Render("⚠ approval required"), a.Tool, a.Risk, a.Reason)
	return m.styles.border.Render(panel)
}

// ---------------------------------------------------------------------------
// Combo picker (model combos)
// ---------------------------------------------------------------------------

func (m *Model) renderCombo() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("choose your model combo — enter to select") + "\n")
	if m.combosLoading {
		b.WriteString(m.spinner() + " " + m.styles.muted.Render("loading combos from "+m.cfg.OmniRoute.Endpoint+"…"))
		return m.styles.border.Render(b.String())
	}

	// Show user's configured combos from their OmniRoute account first.
	if len(m.accountCombos) > 0 {
		b.WriteString(m.styles.accent.Render("your combos") + " " + m.styles.muted.Render("(from your OmniRoute account)") + "\n")
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
			strategyTag := ""
			if c.Strategy != "" {
				strategyTag = " " + m.styles.muted.Render("["+c.Strategy+"]")
			}
			fmt.Fprintf(&b, "%s%s%s\n", marker, name, strategyTag)
			if len(c.Models) > 0 {
				models := strings.Join(c.Models, " · ")
				if len(models) > 60 {
					models = models[:57] + "…"
				}
				fmt.Fprintf(&b, "    %s\n", m.styles.muted.Render(models))
			}
		}
		b.WriteString("\n")
	}

	// Show connected providers.
	if len(m.providers) > 0 {
		b.WriteString(m.styles.accent.Render("connected providers") + "\n")
		for _, p := range m.providers {
			statusIcon := m.styles.muted.Render("·")
			if p.Status == "active" || p.Status == "test_passed" {
				statusIcon = m.styles.ok.Render("✓")
			} else if p.Status == "inactive" {
				statusIcon = m.styles.warn.Render("⚠")
			}
			fmt.Fprintf(&b, "  %s %s (%s)\n", statusIcon, m.styles.accent.Render(p.Name), m.styles.muted.Render(p.Status))
		}
		b.WriteString("\n")
	}

	// Auto combos.
	autoCount := 0
	for _, c := range m.combos {
		if c.Kind != "auto" {
			autoCount++
			continue
		}
	}
	if autoCount > 0 {
		b.WriteString(m.styles.muted.Render("auto/* combos route to whatever provider OmniRoute has provisioned") + "\n")
	}

	current := m.cfg.Models.Default
	offset := len(m.accountCombos)
	for i, c := range m.combos {
		marker := "  "
		name := c.ID
		if i+offset == m.comboSel {
			marker = m.styles.accent.Render("▸ ")
			name = m.styles.accent.Render(c.ID)
		}
		if c.ID == current {
			name = name + m.styles.ok.Render("  ✓")
		}
		fmt.Fprintf(&b, "%s%-30s %s\n", marker, name, m.styles.muted.Render(c.Description))
	}
	// Custom id entry (last row).
	marker := "  "
	label := m.styles.muted.Render("type a provider/model id…")
	if m.comboSel == offset+len(m.combos) {
		marker = m.styles.accent.Render("▸ ")
		label = m.styles.accent.Render("type a provider/model id…")
	}
	fmt.Fprintf(&b, "%s%-30s %s\n", marker, label, m.styles.muted.Render("enter any provider/model id"))
	fmt.Fprintf(&b, "\n%s\n", m.styles.muted.Render("current combo: "+current+" — "+combo.Describe(current)))
	return m.styles.border.Render(b.String())
}

// ---------------------------------------------------------------------------
// Other views (kept, lightly restyled)
// ---------------------------------------------------------------------------

func (m *Model) renderAgents() string {
	if len(m.agents) == 0 {
		return m.styles.muted.Render("no agents yet")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-8s %-10s %-32s %-12s %-12s %-8s %s\n", "ID", "ROLE", "MODEL", "STATE", "TOKENS", "COST", "ACTION")
	for i, a := range m.agents {
		marker := " "
		if i == m.selected {
			marker = ">"
		}
		fmt.Fprintf(&b, "%s%-8s %-10s %-32s %-12s %-12d $%-7.4f %s\n",
			marker, a.ID, a.Role, truncate(a.Model, 32), a.State, a.Tokens, a.Cost, truncate(a.Action, 40))
	}
	return m.styles.border.Render(b.String())
}

func (m *Model) renderGraph() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("task graph — "+m.strategy) + "\n")
	if len(m.steps) == 0 {
		b.WriteString(m.styles.muted.Render("no plan selected yet"))
		return m.styles.border.Render(b.String())
	}
	for i, s := range m.steps {
		marker := " "
		if i < len(m.agents) {
			marker = m.agentStateMarker(m.agents[i].State)
		}
		fmt.Fprintf(&b, "%s %d. %s\n", marker, i+1, s)
	}
	b.WriteString("\n" + m.styles.muted.Render("agents: "+fmt.Sprint(len(m.agents))))
	return m.styles.border.Render(b.String())
}

func (m *Model) agentStateMarker(state string) string {
	switch state {
	case "completed":
		return m.styles.ok.Render("✓")
	case "failed", "cancelled":
		return m.styles.err.Render("✗")
	case "running":
		return m.styles.accent.Render("▶")
	default:
		return m.styles.muted.Render("·")
	}
}

// modelMarker renders the per-model state glyph.
func (m *Model) modelMarker(s modelStat) string {
	switch s.LastState {
	case "ok":
		return m.styles.ok.Render("✓")
	case "failed":
		return m.styles.err.Render("✗")
	default:
		return m.styles.accent.Render("●")
	}
}

func (m *Model) renderRouting() string {
	mm := m.metrics
	var b strings.Builder
	b.WriteString(m.styles.title.Render("routing") + "\n")
	fmt.Fprintf(&b, "endpoint:  %s\n", m.cfg.OmniRoute.Endpoint)
	fmt.Fprintf(&b, "session:   %s\n", shortID(m.sessionID))
	fmt.Fprintf(&b, "combo:     %s\n", m.cfg.Models.Default)
	b.WriteString("\n" + m.styles.muted.Render("model execution (this session)"))
	fmt.Fprintf(&b, "\ncalls:     %d  (failed: %d)\n", mm.ModelCalls, mm.FailedCalls)
	fmt.Fprintf(&b, "tokens:    in %d  out %d\n", mm.TokensIn, mm.TokensOut)
	fmt.Fprintf(&b, "cost:      $%.4f\n", mm.CostUSD)
	fmt.Fprintf(&b, "latency:   %s\n", formatDurationMS(mm.LatencyMS))
	fmt.Fprintf(&b, "evaluations: %d\n", mm.Evaluations)
	if len(m.modelStats) > 0 {
		b.WriteString("\n" + m.styles.muted.Render("by model"))
		for _, s := range m.modelStats {
			fmt.Fprintf(&b, "\n  %s %-28s %2dx  %d+%d tok  $%.4f", m.modelMarker(s), truncate(s.ID, 28), s.Calls, s.TokensIn, s.TokensOut, s.Cost)
			if s.Failures > 0 {
				fmt.Fprintf(&b, "  %s", m.styles.err.Render(fmt.Sprintf("%dx failed", s.Failures)))
			}
			if s.Reason != "" {
				fmt.Fprintf(&b, "\n      %s", m.styles.muted.Render("why: "+truncate(s.Reason, 60)))
			}
		}
	}
	return m.styles.border.Render(b.String())
}

func (m *Model) renderSessions() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("sessions — enter to resume") + "\n")
	if len(m.sessions) == 0 {
		b.WriteString(m.styles.muted.Render("no sessions"))
		return m.styles.border.Render(b.String())
	}
	for i, ss := range m.sessions {
		marker := " "
		if i == m.selected {
			marker = ">"
		}
		fmt.Fprintf(&b, "%s %-9s %-40s %s\n", marker, ss.Status, truncate(ss.Title, 40),
			ss.CreatedAt.Local().Format("2006-01-02 15:04"))
	}
	return m.styles.border.Render(b.String())
}

func (m *Model) renderHelp() string {
	rows := [][2]string{
		{"enter", "run the task in the input bar"},
		{"i", "focus the task input"},
		{"k", "set your OmniRoute API key"},
		{"e", "change the OmniRoute endpoint"},
		{"c / ctrl+c", "cancel the running task"},
		{"p", "choose your stack"},
		{"tab / shift+tab", "cycle views"},
		{"up / down", "select agent / session / stack"},
		{"enter (sessions)", "resume the selected session"},
		{"enter (stack)", "set the selected stack"},
		{"y / n", "answer an approval prompt"},
		{"q", "quit (when idle)"},
	}
	var b strings.Builder
	b.WriteString(m.styles.title.Render("keys") + "\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-28s %s\n", r[0], r[1])
	}
	return m.styles.border.Render(b.String())
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

// compactEvent renders an event as a short stream line.
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
	case event.ObservationCreated:
		var d event.ObservationCreatedData
		decode(e, &d)
		return "obs " + d.Summary
	case event.EvaluationStarted:
		var d event.EvaluationData
		decode(e, &d)
		return "evaluate " + d.Evaluator
	case event.EvaluationComplete:
		var d event.EvaluationCompletedData
		decode(e, &d)
		return "evaluate " + d.Evaluator + " → " + d.Outcome
	case event.RepairStarted:
		var d event.RepairData
		decode(e, &d)
		return fmt.Sprintf("repair #%d %s", d.Attempt, d.Strategy)
	case event.TaskCompleted:
		return "task completed"
	case event.TaskFailed:
		var d event.TaskFailedData
		decode(e, &d)
		return "task failed: " + truncate(d.Error, 80)
	case event.TaskCancelled:
		return "task cancelled"
	case event.ContextCondensed:
		return "context condensed"
	default:
		return ""
	}
}

var _ = json.Marshal
