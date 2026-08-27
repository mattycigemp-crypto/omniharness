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

	"omniharness/internal/combo"
	"omniharness/internal/config"
	"omniharness/internal/event"
	"omniharness/internal/gateway"
	"omniharness/internal/policy"
	"omniharness/internal/runtime"
	"omniharness/internal/session"
	"omniharness/internal/task"
	"omniharness/internal/telemetry"
	"omniharness/internal/version"
)

// View identifies the active TUI view.
type View int

const (
	ViewBoot View = iota
	ViewHome
	ViewMain
	ViewAgents
	ViewGraph
	ViewRouting
	ViewSessions
	ViewCombo
	ViewHelp
	viewCount
)

func (v View) String() string {
	switch v {
	case ViewBoot:
		return "boot"
	case ViewHome:
		return "home"
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
	case ViewCombo:
		return "combo"
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

// chatKind identifies a conversation bubble.
type chatKind int

const (
	chatUser chatKind = iota
	chatHarness
	chatResult
	chatError
)

// chatLine is one bubble in the chat thread.
type chatLine struct {
	Kind chatKind
	Text string
	Time string
}

// modelStat aggregates one model's usage in the current session.
type modelStat struct {
	ID        string
	Calls     int
	TokensIn  int64
	TokensOut int64
	Cost      float64
	Failures  int
	LastState string // "ok" | "failed" | "requested"
	Reason    string // last selection reason (explainability)
}

// combosMsg carries the fetched model combo list.
type combosMsg struct {
	Options      []combo.Option
	Live         bool // catalog came from the server, not the fallback
	Providers    []providerInfo // connected providers from the account
	AccountCombos []accountCombo // user's configured combos from OmniRoute
}

// providerInfo describes a connected provider from the OmniRoute account.
type providerInfo struct {
	ID      string
	Name    string
	Status  string // "active", "inactive", "test_passed", etc.
	Models  []string // available model IDs
}

// accountCombo is a user-configured combo from their OmniRoute account.
type accountCombo struct {
	Name     string
	Strategy string
	Models   []string // provider/model refs in the chain
	Default  bool
}

// approvalReq is a pending human approval.
type approvalReq struct {
	Tool   string
	Risk   string
	Reason string
	Reply  chan bool
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
	Req   *approvalReq
	Grant bool
}

// tickMsg is a periodic refresh pulse.
type tickMsg time.Time

// bootMsg is a boot sequence step.
type bootMsg struct {
	phase int
	msg   string
}

// bootCompleteMsg signals boot is done.
type bootCompleteMsg struct{}

// sessionsMsg carries the session list.
type sessionsMsg struct{ Sessions []*session.Session }

// taskStartedMsg fires when a task begins running.
type taskStartedMsg struct {
	SessionID string
	TaskID    string
}

// Model is the TUI state.
type Model struct {
	cfg        config.Config
	configPath string // where `stack set` persists (empty = don't persist)
	rt         *runtime.Runtime
	program    *tea.Program
	view       View

	width, height int

	// Live task state.
	sessionID      string
	taskID         string
	status         task.Status
	strategy       string
	strategyReason string
	steps          []string
	prompt         string
	agents         []agentRow
	events         []eventLine // ring buffer
	metrics        telemetry.SessionMetrics
	repairs        int // Conversation (chat thread) + animation state.
	conversation   []chatLine
	frame          int    // animation frame (tick-driven)
	stream         string // typewriter buffer (result text revealed so far)
	streamFull     string // full result text to reveal
	streamIdx      int

	// Model combo picker.
	combos        []combo.Option
	combosLoading bool
	comboSel      int
	modelInput    bool // input bar is in "type a provider/model id" mode

	// Connected providers from the OmniRoute account.
	providers     []providerInfo
	providersLoading bool

	// User's configured combos from the OmniRoute account.
	accountCombos []accountCombo

	// Per-model usage (actual models used this session).
	modelStats  []modelStat
	modelStatID map[string]int
	lastModel   string // most recently requested model (header badge)

	// Control.
	running      bool
	cancel       context.CancelFunc
	approval     *approvalReq
	input        textinput.Model
	inputFocused bool
	selected     int // selected row in agent view

	// API key input mode.
	keyInput bool // input bar is in "paste your API key" mode

	// Endpoint input mode.
	endpointInput bool // input bar is in "type endpoint URL" mode

	// Sessions view.
	sessions []*session.Session

	// Boot sequence.
	bootPhase  int    // 0-4: current boot step
	bootMsgs   []string // messages to display
	bootDone   bool    // boot complete

	// Style.
	styles Styles
}

// Styles holds the (deliberately restrained) visual language.
type Styles struct {
	base   lipgloss.Style
	header lipgloss.Style
	muted  lipgloss.Style
	accent lipgloss.Style
	ok     lipgloss.Style
	err    lipgloss.Style
	warn   lipgloss.Style
	border lipgloss.Style
	footer lipgloss.Style
	title  lipgloss.Style
}

// New builds the TUI model. configPath is where a picked stack is persisted
// (the config file the CLI loaded); pass "" to keep the picker in-memory.
func New(cfg config.Config, rt *runtime.Runtime, configPath string) *Model {
	in := textinput.New()
	in.Placeholder = "describe a task…"
	in.Prompt = "> "
	in.CharLimit = 1000

	// Build the welcome conversation on startup.
	convo := []chatLine{
		{Kind: chatHarness, Text: "Welcome to OmniHarness! Agent orchestration for OmniRoute.", Time: time.Now().Format("15:04:05")},
		{Kind: chatHarness, Text: "Start by describing a task or choose a model with 'p'.", Time: time.Now().Format("15:04:05")},
	}

	return &Model{
		cfg:           cfg,
		configPath:    configPath,
		rt:            rt,
		view:          ViewBoot,
		input:         in,
		inputFocused:  false, // don't focus during boot
		status:        task.StatusPending,
		styles:        makeStyles(cfg.TUI.Color),
		modelStatID:   map[string]int{},
		combos:        combo.List(nil), // offline fallback until the catalog loads
		combosLoading: true,
		conversation:  convo,
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
	return tea.Batch(m.tick(), m.startBoot())
}

// startBoot kicks off the boot sequence with animated steps.
func (m *Model) startBoot() tea.Cmd {
	return func() tea.Msg {
		return bootMsg{phase: 0, msg: "initializing OmniHarness v" + version.Version}
	}
}

// fetchCombosCmd returns a command that fetches combos on startup.
func fetchCombosCmd(m *Model) tea.Cmd {
	rt := m.rt
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		ids, err := rt.Gateway.ListCatalog(ctx)
		if err != nil {
			return combosMsg{Options: combo.List(nil), Live: false}
		}
		var providers []providerInfo
		if conns, err := rt.Gateway.ListProviders(ctx); err == nil {
			for _, c := range conns {
				providers = append(providers, providerInfo{
					ID:     c.ID,
					Name:   c.Name,
					Status: c.Status,
				})
			}
		}
		var accountCombos []accountCombo
		for _, gc := range rt.Gateway.ListCombos(ctx) {
			var models []string
			for _, m := range gc.Models {
				if m.Model != "" {
					models = append(models, m.Model)
				}
			}
			accountCombos = append(accountCombos, accountCombo{
				Name:     gc.Name,
				Strategy: gc.Strategy,
				Models:   models,
				Default:  gc.IsDefault,
			})
		}
		return combosMsg{Options: combo.List(ids), Live: true, Providers: providers, AccountCombos: accountCombos}
	}
}

// refreshInterval returns the animation cadence from config (default 100ms).
func (m *Model) refreshInterval() time.Duration {
	ms := m.cfg.TUI.RefreshMS
	if ms <= 0 {
		ms = 100
	}
	if ms < 30 {
		ms = 30
	}
	return time.Duration(ms) * time.Millisecond
}

func (m *Model) tick() tea.Cmd {
	return tea.Tick(m.refreshInterval(), func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case bootMsg:
		m.bootPhase = msg.phase
		m.bootMsgs = append(m.bootMsgs, msg.msg)
		// Progress through boot phases.
		switch msg.phase {
		case 0:
			// Connect to OmniRoute.
			return m, tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				err := m.rt.Gateway.Ping(ctx)
				if err != nil {
					return bootMsg{phase: 1, msg: "⚠ gateway unreachable at " + m.cfg.OmniRoute.Endpoint}
				}
				return bootMsg{phase: 1, msg: "✓ connected to " + m.cfg.OmniRoute.Endpoint}
			})
		case 1:
			// Check auth.
			return m, tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
				if m.cfg.OmniRoute.APIKey != "" {
					return bootMsg{phase: 2, msg: "✓ authenticated (key_" + last4(m.cfg.OmniRoute.APIKey) + ")"}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				diag := m.rt.Gateway.Diagnose(ctx)
				switch diag.State {
				case gateway.AuthNotRequired:
					return bootMsg{phase: 2, msg: "✓ anonymous mode (no API key needed)"}
				case gateway.AuthUnreachable:
					return bootMsg{phase: 2, msg: "⚠ gateway unreachable — key can be set later with 'k'"}
				default:
					return bootMsg{phase: 2, msg: "⚠ no API key — press 'k' to set one"}
				}
			})
		case 2:
			// Fetch combos.
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				var providers []providerInfo
				if conns, err := m.rt.Gateway.ListProviders(ctx); err == nil {
					for _, c := range conns {
						providers = append(providers, providerInfo{
							ID:     c.ID,
							Name:   c.Name,
							Status: c.Status,
						})
					}
				}
				var accountCombos []accountCombo
				for _, gc := range m.rt.Gateway.ListCombos(ctx) {
					var models []string
					for _, md := range gc.Models {
						if md.Model != "" {
							models = append(models, md.Model)
						}
					}
					accountCombos = append(accountCombos, accountCombo{
						Name:     gc.Name,
						Strategy: gc.Strategy,
						Models:   models,
						Default:  gc.IsDefault,
					})
				}
				m.providers = providers
				m.accountCombos = accountCombos
				count := len(accountCombos)
				if count > 0 {
					return bootMsg{phase: 3, msg: fmt.Sprintf("✓ loaded %d account combos, %d providers", count, len(providers))}
				}
				return bootMsg{phase: 3, msg: fmt.Sprintf("✓ %d providers connected", len(providers))}
			})
		case 3:
			// Finalize.
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return bootCompleteMsg{}
			})
		}
		return m, nil

	case bootCompleteMsg:
		m.bootDone = true
		m.view = ViewHome
		m.inputFocused = true
		return m, m.input.Focus()

	case tickMsg:
		m.frame++
		// Typewriter reveal: surface more of the final result each tick.
		if m.streamIdx < len(m.streamFull) {
			m.streamIdx += 5
			if m.streamIdx > len(m.streamFull) {
				m.streamIdx = len(m.streamFull)
			}
			m.stream = m.streamFull[:m.streamIdx]
		}
		if m.sessionID != "" {
			if mm, err := telemetry.ForSession(m.rt.Store, m.sessionID); err == nil {
				m.metrics = mm
			}
		}
		return m, m.tick()

	case eventMsg:
		m.applyEvent(msg.E)
		return m, nil

	case combosMsg:
		m.combos = msg.Options
		m.providers = msg.Providers
		m.accountCombos = msg.AccountCombos
		m.combosLoading = false
		m.comboSel = 0
		// If the fetch happened after the user pressed p, land in the picker.
		if m.view == ViewCombo {
			return m, nil
		}
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
		m.repairs = 0
		m.lastModel = ""
		m.modelStats = nil
		m.modelStatID = map[string]int{}
		// Reset the typewriter so a fresh task never replays a stale result.
		m.streamFull = ""
		m.stream = ""
		m.streamIdx = 0
		return m, nil

	case taskDoneMsg:
		m.running = false
		if msg.Task != nil {
			m.status = msg.Task.Status
		} else if msg.Err != nil {
			m.status = task.StatusFailed
		}
		m.refreshSessions()
		// Prime the typewriter with the final result (real content, revealed
		// progressively for effect).
		if msg.Task != nil {
			if full := resultText(msg.Task); full != "" {
				m.streamFull = full
				m.streamIdx = 0
				m.stream = ""
			}
		}
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
			val := strings.TrimSpace(m.input.Value())
			if val == "" {
				return m, nil
			}
			m.input.SetValue("")
			m.inputFocused = false
			if m.keyInput {
				// API key entry mode: store the key.
				return m, m.applyKey(val)
			}
			if m.endpointInput {
				// Endpoint entry mode: store the URL.
				return m, m.applyEndpoint(val)
			}
			if strings.HasPrefix(val, "/") {
				return m, m.handleCommand(val)
			}
			if m.modelInput {
				// "type a provider/model id" mode: commit the combo.
				m.modelInput = false
				return m, m.applyCombo(val)
			}
			return m, m.startTask(val)
		case "esc":
			m.inputFocused = false
			m.modelInput = false
			m.keyInput = false
			m.endpointInput = false
			m.input.Placeholder = "describe a task…"
			return m, nil
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}

	// Combo picker navigation.
	if m.view == ViewCombo {
		totalAccount := len(m.accountCombos)
		totalCatalog := len(m.combos)
		total := totalAccount + totalCatalog + 1 // +1 for custom entry

		switch msg.String() {
		case "up":
			if m.comboSel > 0 {
				m.comboSel--
			}
			return m, nil
		case "down":
			if m.comboSel < total-1 {
				m.comboSel++
			}
			return m, nil
		case "enter":
			return m, m.pickCombo()
		case "esc":
			m.view = ViewHome
			return m, nil
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c", "q":
		if m.running {
			m.cancelTask()
			return m, nil
		}
		return m, tea.Quit
	case "tab", "right":
		if m.view == ViewHome {
			m.view = ViewMain
		} else {
			m.view = View((int(m.view) + 1) % int(viewCount))
		}
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
	case "k":
		return m, m.startKeyInput()
	case "e":
		return m, m.startEndpointInput()
	case "p":
		if m.combosLoading {
			return m, m.fetchCombos()
		}
		m.comboSel = 0
		m.view = ViewCombo
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
		if m.view == ViewHome && !m.running {
			m.inputFocused = true
			return m, m.input.Focus()
		}
	}
	return m, nil
}

// handleMouse processes mouse events for click-to-focus and view switching.
func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.MouseLeft:
		// Click on the tab bar to switch views.
		if msg.Y == 0 {
			// Estimate tab positions (each tab ~8 chars + 2 space).
			tabWidth := 10
			x := msg.X
			// Skip brand area (~20 chars).
			if x > 20 {
				tabIdx := (x - 20) / tabWidth
				if tabIdx >= 0 && tabIdx < int(viewCount) {
					m.view = View(tabIdx)
				}
			}
		}
		// Click on input area to focus.
		if msg.Y >= m.height-2 {
			m.inputFocused = true
			return m, m.input.Focus()
		}
	case tea.MouseWheelUp:
		if m.view == ViewCombo {
			if m.comboSel > 0 {
				m.comboSel--
			}
		}
	case tea.MouseWheelDown:
		if m.view == ViewCombo {
			total := len(m.accountCombos) + len(m.combos) + 1
			if m.comboSel < total-1 {
				m.comboSel++
			}
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

// fetchCombos loads the model combo list from the live OmniRoute catalog
// (falling back to the built-in combos when unreachable). It also fetches
// connected providers and the user's configured combos. Only auto/* combos
// are shown in the picker (individual models are hidden).
func (m *Model) fetchCombos() tea.Cmd {
	rt := m.rt
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		ids, err := rt.Gateway.ListCatalog(ctx)
		if err != nil {
			return combosMsg{Options: combo.List(nil), Live: false}
		}
		// Filter to only auto/* combos (hide individual models).
		var autoOnly []string
		for _, id := range ids {
			if combo.IsAuto(id) {
				autoOnly = append(autoOnly, id)
			}
		}
		// Fetch connected providers.
		var providers []providerInfo
		if conns, err := rt.Gateway.ListProviders(ctx); err == nil {
			for _, c := range conns {
				providers = append(providers, providerInfo{
					ID:     c.ID,
					Name:   c.Name,
					Status: c.Status,
				})
			}
		}
		// Fetch user's configured combos.
		var accountCombos []accountCombo
		for _, gc := range rt.Gateway.ListCombos(ctx) {
			var models []string
			for _, m := range gc.Models {
				if m.Model != "" {
					models = append(models, m.Model)
				}
			}
			accountCombos = append(accountCombos, accountCombo{
				Name:     gc.Name,
				Strategy: gc.Strategy,
				Models:   models,
				Default:  gc.IsDefault,
			})
		}
		return combosMsg{Options: combo.List(autoOnly), Live: true, Providers: providers, AccountCombos: accountCombos}
	}
}

// pickCombo commits the selected picker entry: either an account combo,
// a catalog combo, or the "type a provider/model id" entry.
func (m *Model) pickCombo() tea.Cmd {
	totalAccount := len(m.accountCombos)
	totalCatalog := len(m.combos)
	total := totalAccount + totalCatalog + 1 // +1 for custom entry

	if m.comboSel < 0 || m.comboSel >= total {
		return nil
	}

	// Account combos are listed first.
	if m.comboSel < totalAccount {
		ac := m.accountCombos[m.comboSel]
		return m.applyCombo(ac.Name)
	}

	// Custom id entry (last row).
	if m.comboSel == total-1 {
		m.modelInput = true
		m.view = ViewMain
		m.input.SetValue("")
		m.input.Placeholder = "provider/model id… (e.g. openai/gpt-5.4)"
		m.inputFocused = true
		return m.input.Focus()
	}

	// Catalog combos.
	catalogIdx := m.comboSel - totalAccount
	if catalogIdx >= 0 && catalogIdx < totalCatalog {
		return m.applyCombo(m.combos[catalogIdx].ID)
	}
	return nil
}

// applyCombo sets the model combo (cfg.Models.Default), persists it when a
// config path is known, and confirms in the chat thread. Validation is
// structural only; OmniRoute surfaces routing failures at request time.
func (m *Model) applyCombo(id string) tea.Cmd {
	if id == "" {
		return m.noteError(fmt.Errorf("empty model combo"))
	}
	if !combo.IsAuto(id) && !strings.Contains(id, "/") {
		return m.noteError(fmt.Errorf("%s", combo.FormatError(id)))
	}
	m.cfg.Models.Default = id
	m.comboSel = 0
	m.modelInput = false
	m.view = ViewMain
	m.input.Placeholder = "describe a task…"
	m.chat(chatHarness, "combo → "+id+" — "+combo.Describe(id))
	if m.configPath != "" {
		cfg, err := config.Load(m.configPath)
		if err != nil {
			return m.noteError(err)
		}
		cfg.Models.Default = id
		if err := cfg.Save(m.configPath); err != nil {
			return m.noteError(err)
		}
	}
	return nil
}

// startKeyInput switches the input bar to API key entry mode.
func (m *Model) startKeyInput() tea.Cmd {
	m.keyInput = true
	m.input.SetValue("")
	m.input.Placeholder = "paste your OmniRoute API key (sk-…)"
	m.inputFocused = true
	return m.input.Focus()
}

// applyKey stores the entered API key and persists it to the config file.
func (m *Model) applyKey(key string) tea.Cmd {
	m.keyInput = false
	m.input.Placeholder = "describe a task…"
	if key == "" {
		return m.noteError(fmt.Errorf("no API key provided"))
	}
	m.cfg.OmniRoute.APIKey = key
	// Persist to config file so the key survives restarts.
	if m.configPath != "" {
		if err := m.cfg.Save(m.configPath); err != nil {
			return m.noteError(fmt.Errorf("saved key but failed to persist: %w", err))
		}
	}
	m.chat(chatHarness, "API key set (key_"+last4(key)+") — saved to config")
	return nil
}

// startEndpointInput switches the input bar to endpoint URL entry mode.
func (m *Model) startEndpointInput() tea.Cmd {
	m.endpointInput = true
	m.input.SetValue(m.cfg.OmniRoute.Endpoint)
	m.input.Placeholder = "OmniRoute endpoint URL (e.g. http://localhost:20128)"
	m.inputFocused = true
	return m.input.Focus()
}

// applyEndpoint stores the entered endpoint URL and persists it.
func (m *Model) applyEndpoint(url string) tea.Cmd {
	m.endpointInput = false
	m.input.SetValue("")
	m.input.Placeholder = "describe a task…"
	if url == "" {
		return m.noteError(fmt.Errorf("no endpoint provided"))
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return m.noteError(fmt.Errorf("endpoint must start with http:// or https://"))
	}
	m.cfg.OmniRoute.Endpoint = strings.TrimSuffix(url, "/")
	// Persist to config file.
	if m.configPath != "" {
		if err := m.cfg.Save(m.configPath); err != nil {
			return m.noteError(fmt.Errorf("saved endpoint but failed to persist: %w", err))
		}
	}
	m.chat(chatHarness, "endpoint → "+m.cfg.OmniRoute.Endpoint+" — saved")
	// Re-create the gateway client with the new endpoint.
	if m.rt != nil && m.rt.Gateway != nil {
		m.rt.Gateway = gateway.New(m.cfg.OmniRoute.Endpoint, m.cfg.OmniRoute.Timeout, m.cfg.OmniRoute.APIKey)
	}
	return nil
}

// handleCommand processes slash commands entered in the input bar.
func (m *Model) handleCommand(cmd string) tea.Cmd {
	args := strings.Fields(cmd)
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "/init":
		m.chat(chatHarness, "No local project config needed. Use 'p' to pick a model.")
	case "/help":
		m.view = ViewHelp
	case "/settings":
		m.chat(chatHarness, fmt.Sprintf("Endpoint: %s | Model: %s | Budget: $%.2f", m.cfg.OmniRoute.Endpoint, m.cfg.Models.Default, m.cfg.Budgets.MaxCostUSD))
	case "/model":
		if len(args) > 1 {
			return m.applyCombo(args[1])
		}
		m.view = ViewCombo
	case "/status":
		m.chat(chatHarness, fmt.Sprintf("Status: %s | Agents: %d | Combo: %s", m.status, len(m.agents), m.cfg.Models.Default))
	case "/diff":
		m.chat(chatHarness, "No current diff available.")
	case "/release-notes":
		m.chat(chatHarness, "OmniHarness TUI v"+version.Version+" — Gemini-inspired dashboard, improved streaming.")
	case "/key":
		return m.startKeyInput()
	case "/endpoint":
		if len(args) > 1 {
			return m.applyEndpoint(args[1])
		}
		return m.startEndpointInput()
	default:
		m.chat(chatError, "Unknown command: "+args[0])
	}
	return nil
}

func (m *Model) noteError(err error) tea.Cmd {
	m.conversation = append(m.conversation, chatLine{
		Kind: chatError,
		Text: err.Error(),
		Time: time.Now().Format("15:04:05"),
	})
	return nil
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
		m.chat(chatUser, truncate(d.Prompt, 500))
	case event.StrategySelected:
		var d event.StrategySelectedData
		decode(e, &d)
		m.strategy = d.Strategy
		m.strategyReason = d.Reason
		m.steps = d.Steps
		reason := d.Reason
		if reason == "" {
			reason = "(no reason)"
		}
		m.chat(chatHarness, "strategy: "+d.Strategy+" — "+reason)
	case event.AgentCreated:
		var d event.AgentCreatedData
		decode(e, &d)
		m.agents = append(m.agents, agentRow{ID: shortID(e.AgentID), Role: d.Role, Model: d.Model, State: "created"})
		m.chat(chatHarness, "agent ready · "+d.Role+" ("+d.Model+")")
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
		m.lastModel = d.Model
		m.recordModelStat(d.Model, func(s *modelStat) {
			s.TokensIn += d.TokensIn
			s.TokensOut += d.TokensOut
			s.Cost += d.CostUSD
			s.LastState = "ok"
		})
		m.chat(chatHarness, fmt.Sprintf("model ← %s (%d+%d tok, $%.4f)", d.Model, d.TokensIn, d.TokensOut, d.CostUSD))
	case event.RepairStarted:
		var d event.RepairData
		decode(e, &d)
		m.repairs++
		m.chat(chatHarness, fmt.Sprintf("repair #%d — %s", d.Attempt, d.Strategy))
	case event.TaskCompleted:
		var d event.TaskCompletedData
		decode(e, &d)
		m.status = task.StatusCompleted
		if d.Summary != "" {
			m.prompt = d.Summary
			m.chat(chatResult, d.Summary)
		}
	case event.TaskFailed:
		var d event.TaskFailedData
		decode(e, &d)
		m.status = task.StatusFailed
		m.chat(chatError, "task failed: "+truncate(d.Error, 300))
	case event.TaskCancelled:
		m.status = task.StatusCancelled
		m.chat(chatHarness, "task cancelled")
	case event.TaskStarted:
		var d event.TaskStateData
		decode(e, &d)
		m.status = d.Status
	case event.ModelRequested:
		var d event.ModelRequestedData
		decode(e, &d)
		m.lastModel = d.Model
		m.recordModelStat(d.Model, func(s *modelStat) {
			s.Calls++
			s.LastState = "requested"
			if d.Reason != "" {
				s.Reason = d.Reason
			}
		})
		if d.Reason != "" {
			m.chat(chatHarness, "model → "+d.Model+" ("+d.Reason+")")
		} else {
			m.chat(chatHarness, "model → "+d.Model)
		}
	case event.ModelFailed:
		var d event.ModelFailedData
		decode(e, &d)
		m.lastModel = d.Model
		m.recordModelStat(d.Model, func(s *modelStat) {
			s.Failures++
			s.LastState = "failed"
		})
		m.chat(chatError, "model ✗ "+d.Model+": "+truncate(d.Error, 200))
	case event.ToolRequested:
		var d event.ToolRequestedData
		decode(e, &d)
		m.chat(chatHarness, "tool "+d.Tool+" ["+d.Risk+"]")
	case event.ToolFailed:
		var d event.ToolFailedData
		decode(e, &d)
		m.chat(chatError, "tool ✗ "+d.Tool+": "+truncate(d.Error, 160))
	case event.EvaluationComplete:
		var d event.EvaluationCompletedData
		decode(e, &d)
		m.chat(chatHarness, "verified · "+d.Evaluator+" → "+d.Outcome)
	}
	m.pushEvent(e)
}

// resultText extracts the final, user-facing result text of a task.
func resultText(t *task.Task) string {
	if t == nil {
		return ""
	}
	if t.Result != nil {
		if t.Result.Summary != "" {
			return t.Result.Summary
		}
		if t.Result.Output != "" {
			return t.Result.Output
		}
	}
	return t.Error
}

// recordModelStat updates (or creates) the per-model usage row for id.
// The mutate fn runs with the row already existing; rows are kept in first-use
// order so the sidebar can show the most recent model last.
func (m *Model) recordModelStat(id string, mutate func(*modelStat)) {
	if id == "" {
		return
	}
	idx, ok := m.modelStatID[id]
	if !ok {
		idx = len(m.modelStats)
		m.modelStatID[id] = idx
		m.modelStats = append(m.modelStats, modelStat{ID: id})
	}
	mutate(&m.modelStats[idx])
}

// chat appends a conversation bubble, keeping the thread bounded.
func (m *Model) chat(k chatKind, text string) {
	if text == "" {
		return
	}
	m.conversation = append(m.conversation, chatLine{Kind: k, Text: text, Time: time.Now().Format("15:04:05")})
	if len(m.conversation) > 400 {
		m.conversation = m.conversation[len(m.conversation)-400:]
	}
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
