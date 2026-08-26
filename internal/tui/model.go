// Package tui implements the OmniHarness cockpit: a thin Bubble Tea
// presentation/control layer over the core runtime. All state flows from the
// event bus into the view; the TUI never owns truth.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"omniharness/internal/config"
	"omniharness/internal/event"
	"omniharness/internal/policy"
	"omniharness/internal/runtime"
	"omniharness/internal/session"
	"omniharness/internal/task"
	"omniharness/internal/telemetry"
)

// View identifies the active TUI view.
type View int

const (
	ViewMain View = iota
	ViewAgents
	ViewGraph
	ViewRouting
	ViewSessions
	ViewHelp
	viewCount
)

func (v View) String() string {
	switch v {
	case ViewMain:
		return "main"
	case ViewAgents:
		return "agents"
	case ViewGraph:
		return "graph"
	case ViewRouting:
		return "routing"
	case ViewSessions:
		return "sessions"
	case ViewHelp:
		return "help"
	}
	return "?"
}

// agentRow is a live snapshot of one agent.
type agentRow struct {
	ID     string
	Role   string
	Model  string
	State  string
	Action string
	Tokens int64
	Cost   float64
}

// eventLine is a rendered event for the stream.
type eventLine struct {
	Time string
	Type string
	Text string
}

// approvalReq is a pending human approval.
type approvalReq struct {
	Tool    string
	Risk    string
	Reason  string
	Reply   chan bool
}

// taskDoneMsg carries the finished task.
type taskDoneMsg struct {
	Task *task.Task
	Err  error
}

// eventMsg carries a runtime event into the TUI.
type eventMsg struct{ E event.Event }

// approvalMsg asks the user to approve an action.
type approvalMsg struct{ Req *approvalReq }

// approvalAnswerMsg is the user's answer.
type approvalAnswerMsg struct {
	Req  *approvalReq
	Grant bool
}

// tickMsg is a periodic refresh pulse.
type tickMsg time.Time

// sessionsMsg carries the session list.
type sessionsMsg struct{ Sessions []*session.Session }

// taskStartedMsg fires when a task begins running.
type taskStartedMsg struct {
	SessionID string
	TaskID    string
}

// Model is the TUI state.
type Model struct {
	cfg     config.Config
	rt      *runtime.Runtime
	program *tea.Program
	view    View

	width, height int

	// Live task state.
	sessionID string
	taskID    string
	status    task.Status
	strategy  string
	strategyReason string
	steps     []string
	prompt    string
	agents    []agentRow
	events    []eventLine // ring buffer
	metrics   telemetry.SessionMetrics
	repairs   int

	// Control.
	running  bool
	cancel   context.CancelFunc
	approval *approvalReq
	input    textinput.Model
	inputFocused bool
	selected int // selected row in agent view

	// Sessions view.
	sessions []*session.Session

	// Style.
	styles Styles
}

// Styles holds the (deliberately restrained) visual language.
type Styles struct {
	base       lipgloss.Style
	header     lipgloss.Style
	muted      lipgloss.Style
	accent     lipgloss.Style
	ok         lipgloss.Style
	err        lipgloss.Style
	warn       lipgloss.Style
	border     lipgloss.Style
	footer     lipgloss.Style
	title      lipgloss.Style
}

// New builds the TUI model.
func New(cfg config.Config, rt *runtime.Runtime) *Model {
	in := textinput.New()
	in.Placeholder = "describe a task…"
	in.Prompt = "> "
	in.CharLimit = 1000
	return &Model{
		cfg:    cfg,
		rt:     rt,
		view:   ViewMain,
		input:  in,
		status: task.StatusPending,
		styles: makeStyles(cfg.TUI.Color),
	}
}

func makeStyles(color bool) Styles {
	if !color {
		return Styles{
			base:   lipgloss.NewStyle(),
			header: lipgloss.NewStyle().Bold(true).Padding(0, 1),
			muted:  lipgloss.NewStyle().Faint(true),
			accent: lipgloss.NewStyle().Bold(true),
			ok:     lipgloss.NewStyle().Bold(true),
			err:    lipgloss.NewStyle().Bold(true),
			warn:   lipgloss.NewStyle().Bold(true),
			border: lipgloss.NewStyle().Padding(0, 1),
			footer: lipgloss.NewStyle().Faint(true).Padding(0, 1),
			title:  lipgloss.NewStyle().Bold(true).Underline(true),
		}
	}
	return Styles{
		base:   lipgloss.NewStyle(),
		header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("24")).Padding(0, 1),
		muted:  lipgloss.NewStyle().Faint(true),
		accent: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")),
		ok:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")),
		err:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203")),
		warn:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")),
		border: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1),
		footer: lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244")).Padding(0, 1),
		title:  lipgloss.NewStyle().Bold(true).Underline(true),
	}
}

// Init starts the input and refresh tick. Runtime events arrive through the
// persistent subscription installed by Run (via prog.Send).
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.input.Focus(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		if m.sessionID != "" {
			if mm, err := telemetry.ForSession(m.rt.Store, m.sessionID); err == nil {
				m.metrics = mm
			}
		}
		return m, tick()

	case eventMsg:
		m.applyEvent(msg.E)
		return m, nil

	case approvalMsg:
		m.approval = msg.Req
		return m, nil

	case approvalAnswerMsg:
		if m.approval != nil && m.approval == msg.Req {
			m.approval.Reply <- msg.Grant
			m.approval = nil
		}
		return m, nil

	case taskStartedMsg:
		m.running = true
		m.sessionID = msg.SessionID
		m.taskID = msg.TaskID
		m.status = task.StatusRunning
		m.agents = nil
		m.events = nil
		m.strategy = ""
		m.strategyReason = ""
		m.steps = nil
		return m, nil

	case taskDoneMsg:
		m.running = false
		if msg.Task != nil {
			m.status = msg.Task.Status
		} else if msg.Err != nil {
			m.status = task.StatusFailed
		}
		m.refreshSessions()
		return m, nil

	case sessionsMsg:
		m.sessions = msg.Sessions
		return m, nil

	default:
		return m, nil
	}
}

// handleKey routes keys by context (approval modal, prompt input, views).
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.approval != nil {
		switch msg.String() {
		case "y", "Y":
			return m, func() tea.Msg { return approvalAnswerMsg{Req: m.approval, Grant: true} }
		case "n", "N", "esc", "ctrl+c":
			return m, func() tea.Msg { return approvalAnswerMsg{Req: m.approval, Grant: false} }
		}
		return m, nil
	}

	if m.inputFocused {
		switch msg.String() {
		case "enter":
			prompt := strings.TrimSpace(m.input.Value())
			if prompt != "" {
				m.input.SetValue("")
				m.inputFocused = false
				return m, m.startTask(prompt)
			}
			return m, nil
		case "esc":
			m.inputFocused = false
			return m, nil
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}

	switch msg.String() {
	case "ctrl+c", "q":
		if m.running {
			m.cancelTask()
			return m, nil
		}
		return m, tea.Quit
	case "tab", "right":
		m.view = View((int(m.view) + 1) % int(viewCount))
		return m, nil
	case "shift+tab", "left":
		m.view = View((int(m.view) + int(viewCount) - 1) % int(viewCount))
		return m, nil
	case "up":
		if m.selected > 0 {
			m.selected--
		}
		return m, nil
	case "down":
		if m.selected < len(m.agents)-1 {
			m.selected++
		}
		return m, nil
	case "i":
		m.inputFocused = true
		return m, nil
	case "r":
		m.refreshSessions()
		return m, nil
	case "s":
		m.view = ViewSessions
		return m, nil
	case "c":
		m.cancelTask()
		return m, nil
	case "enter":
		if m.view == ViewSessions {
			return m, m.resumeSession()
		}
	}
	return m, nil
}

// startTask launches the task runner goroutine. State that the Update loop
// owns (running, cancel) is set here synchronously — never from inside the
// returned cmd, which runs on the tea command goroutine and would race with
// Update. Each submitted task gets exactly one fresh session.
func (m *Model) startTask(prompt string) tea.Cmd {
	if m.running {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true
	m.status = task.StatusRunning
	rt := m.rt
	return func() tea.Msg {
		ss, err := rt.NewSession("", truncate(prompt, 60))
		if err != nil {
			return taskDoneMsg{Err: err}
		}
		tsk, err := rt.RunTask(ctx, ss.ID, prompt, runtime.RunOptions{})
		return taskDoneMsg{Task: tsk, Err: err}
	}
}

func (m *Model) cancelTask() {
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *Model) refreshSessions() {
	ss, err := m.rt.ListSessions(20)
	if err == nil {
		m.sessions = ss
	}
}

func (m *Model) resumeSession() tea.Cmd {
	if m.running || m.selected >= len(m.sessions) {
		return nil
	}
	ss := m.sessions[m.selected]
	m.sessionID = ss.ID
	m.status = task.StatusRunning
	m.running = true
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	rt := m.rt
	return func() tea.Msg {
		tasks, err := rt.Store.TasksBySession(ss.ID)
		if err != nil || len(tasks) == 0 {
			return taskDoneMsg{Err: fmt.Errorf("no tasks in session")}
		}
		tsk, err := rt.RunTask(ctx, ss.ID, tasks[0].Spec.Prompt, runtime.RunOptions{TaskID: tasks[0].ID})
		return taskDoneMsg{Task: tsk, Err: err}
	}
}

// applyEvent folds a runtime event into the view state.
func (m *Model) applyEvent(e event.Event) {
	if m.sessionID != "" && e.SessionID != m.sessionID {
		return
	}
	switch e.Type {
	case event.SessionStarted:
		m.sessionID = e.SessionID
	case event.TaskCreated:
		m.taskID = e.TaskID
		var d event.TaskCreatedData
		decode(e, &d)
		m.prompt = d.Prompt
	case event.StrategySelected:
		var d event.StrategySelectedData
		decode(e, &d)
		m.strategy = d.Strategy
		m.strategyReason = d.Reason
		m.steps = d.Steps
	case event.AgentCreated:
		var d event.AgentCreatedData
		decode(e, &d)
		m.agents = append(m.agents, agentRow{ID: shortID(e.AgentID), Role: d.Role, Model: d.Model, State: "created"})
	case event.AgentUpdated, event.AgentCompleted, event.AgentFailed, event.AgentCancelled, event.AgentPaused:
		var d event.AgentStateData
		decode(e, &d)
		m.upsertAgent(e.AgentID, d)
	case event.ModelResponded:
		var d event.ModelRespondedData
		decode(e, &d)
		if m.metrics.ModelCalls == 0 {
			m.metrics = telemetry.SessionMetrics{}
		}
	case event.RepairStarted:
		var d event.RepairData
		decode(e, &d)
		m.repairs++
	case event.TaskCompleted:
		var d event.TaskCompletedData
		decode(e, &d)
		m.status = task.StatusCompleted
		if d.Summary != "" {
			m.prompt = d.Summary
		}
	case event.TaskFailed:
		var d event.TaskFailedData
		decode(e, &d)
		m.status = task.StatusFailed
	case event.TaskCancelled:
		m.status = task.StatusCancelled
	case event.TaskStarted:
		var d event.TaskStateData
		decode(e, &d)
		m.status = d.Status
	}
	m.pushEvent(e)
}

func (m *Model) upsertAgent(id string, d event.AgentStateData) {
	for i := range m.agents {
		if m.agents[i].ID == shortID(id) {
			m.agents[i].State = string(d.Status)
			if d.Action != "" {
				m.agents[i].Action = d.Action
			}
			if d.Model != "" {
				m.agents[i].Model = d.Model
			}
			if d.Tokens > 0 {
				m.agents[i].Tokens = d.Tokens
			}
			if d.CostUSD > 0 {
				m.agents[i].Cost = d.CostUSD
			}
			return
		}
	}
}

func (m *Model) pushEvent(e event.Event) {
	m.events = append(m.events, eventLine{
		Time: e.Time.Local().Format("15:04:05.000"),
		Type: string(e.Type),
		Text: compactEvent(e),
	})
	if len(m.events) > 600 {
		m.events = m.events[len(m.events)-600:]
	}
}

func decode(e event.Event, out any) {
	if p, err := event.Decode(e); err == nil {
		if b, err := json.Marshal(p); err == nil {
			_ = json.Unmarshal(b, out)
		}
	}
}

// approvalApprover bridges the policy engine to the TUI.
type approvalApprover struct {
	model *Model
}

func (a *approvalApprover) RequestApproval(_ context.Context, r policy.Request, reason string) (bool, error) {
	req := &approvalReq{Tool: r.Tool, Risk: string(r.Risk), Reason: reason, Reply: make(chan bool, 1)}
	// Deliver to the model through the tea program.
	prog := a.model.program
	if prog == nil {
		return false, nil
	}
	prog.Send(approvalMsg{Req: req})
	select {
	case grant := <-req.Reply:
		return grant, nil
	case <-time.After(5 * time.Minute):
		return false, nil
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
