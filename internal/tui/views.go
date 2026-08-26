package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"omniharness/internal/event"
)

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
	case ViewHelp:
		body = m.renderHelp()
	}
	header := m.renderHeader()
	footer := m.renderFooter()
	content := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	return lipgloss.NewStyle().Width(m.width).Height(m.height).Render(content)
}

func (m *Model) renderHeader() string {
	status := string(m.status)
	statusStyle := m.styles.muted
	switch m.status {
	case "running":
		statusStyle = m.styles.accent
	case "completed":
		statusStyle = m.styles.ok
	case "failed", "cancelled":
		statusStyle = m.styles.err
	}
	right := fmt.Sprintf("session %s  task %s", shortID(m.sessionID), shortID(m.taskID))
	if m.strategy != "" {
		right = "strategy " + m.strategy + "  " + right
	}
	title := "omniharness"
	if m.running {
		title += " ●"
	}
	return m.styles.header.Render(title + "  " + statusStyle.Render(status)) +
		m.styles.muted.Render("  "+right)
}

func (m *Model) renderFooter() string {
	keys := []string{"tab views", "i input", "enter run", "c cancel", "q quit"}
	if m.view == ViewSessions {
		keys = []string{"↑↓ select", "enter resume", "tab views", "q quit"}
	}
	if m.approval != nil {
		keys = []string{"y approve", "n deny"}
	}
	return m.styles.footer.Render(strings.Join(keys, "  ·  ") + "  " + viewIndicator(m.view))
}

func viewIndicator(v View) string {
	names := make([]string, viewCount)
	for i := 0; i < int(viewCount); i++ {
		n := View(i).String()
		if i == int(v) {
			n = "[" + n + "]"
		}
		names[i] = n
	}
	return strings.Join(names, " ")
}

func (m *Model) renderMain() string {
	if m.approval != nil {
		return m.renderApproval()
	}
	var sections []string

	// Task + strategy panel.
	taskPanel := fmt.Sprintf("prompt: %s\n", truncate(m.prompt, 90))
	taskPanel += fmt.Sprintf("strategy: %s\n", m.strategy)
	if m.strategyReason != "" {
		taskPanel += m.styles.muted.Render("  " + m.strategyReason)
	}
	sections = append(sections, m.styles.border.Render(taskPanel))

	// Metrics line.
	mm := m.metrics
	metrics := fmt.Sprintf("model calls %d  tools %d  tokens %d  cost $%.4f  latency %s  repairs %d",
		mm.ModelCalls, mm.ToolCalls, mm.TokensIn+mm.TokensOut, mm.CostUSD,
		formatDurationMS(mm.LatencyMS), m.repairs)
	sections = append(sections, m.styles.muted.Render(metrics))

	// Agents panel.
	var b strings.Builder
	if len(m.agents) == 0 {
		b.WriteString(m.styles.muted.Render("no agents yet"))
	} else {
		fmt.Fprintf(&b, "%-10s %-32s %-12s %s\n", "ROLE", "MODEL", "STATE", "ACTION")
		for _, a := range m.agents {
			fmt.Fprintf(&b, "%-10s %-32s %-12s %s\n", a.Role, truncate(a.Model, 32), a.State, truncate(a.Action, 40))
		}
	}
	sections = append(sections, m.styles.border.Render(m.styles.title.Render("agents")+"\n"+b.String()))

	// Event stream (tail).
	var ev strings.Builder
	start := 0
	limit := m.height - 14
	if len(m.events) > limit {
		start = len(m.events) - limit
	}
	for _, e := range m.events[start:] {
		ev.WriteString(e.Time + " " + e.Text + "\n")
	}
	if ev.Len() == 0 {
		ev.WriteString(m.styles.muted.Render("awaiting events…"))
	}
	sections = append(sections, m.styles.border.Render(m.styles.title.Render("events")+"\n"+ev.String()))

	// Prompt input.
	inputLine := "task"
	if m.inputFocused {
		inputLine = "task >"
	}
	sections = append(sections, inputLine+" "+m.input.View())

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (m *Model) renderApproval() string {
	a := m.approval
	panel := fmt.Sprintf("%s  tool: %s  risk: %s\nreason: %s\n\n[y] approve    [n] deny",
		m.styles.warn.Render("approval required"), a.Tool, a.Risk, a.Reason)
	return m.styles.border.Render(panel)
}

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
	b.WriteString(m.styles.title.Render("task graph — " + m.strategy) + "\n")
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

func (m *Model) renderRouting() string {
	mm := m.metrics
	var b strings.Builder
	b.WriteString(m.styles.title.Render("routing") + "\n")
	fmt.Fprintf(&b, "endpoint:  %s\n", m.cfg.OmniRoute.Endpoint)
	fmt.Fprintf(&b, "session:   %s\n", shortID(m.sessionID))
	b.WriteString("\n" + m.styles.muted.Render("model execution (this session)"))
	fmt.Fprintf(&b, "\ncalls:     %d  (failed: %d)\n", mm.ModelCalls, mm.FailedCalls)
	fmt.Fprintf(&b, "tokens:    in %d  out %d\n", mm.TokensIn, mm.TokensOut)
	fmt.Fprintf(&b, "cost:      $%.4f\n", mm.CostUSD)
	fmt.Fprintf(&b, "latency:   %s\n", formatDurationMS(mm.LatencyMS))
	fmt.Fprintf(&b, "evaluations: %d\n", mm.Evaluations)
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
		{"tab / shift+tab", "cycle views"},
		{"i", "focus the task input"},
		{"enter", "run the task"},
		{"c / ctrl+c", "cancel the running task"},
		{"q", "quit (when idle)"},
		{"up / down", "select agent / session"},
		{"enter (sessions)", "resume the selected session"},
		{"y / n", "answer an approval prompt"},
	}
	var b strings.Builder
	b.WriteString(m.styles.title.Render("keys") + "\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-22s %s\n", r[0], r[1])
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
