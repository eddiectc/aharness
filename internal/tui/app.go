package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/tools"
)

// UIState is the coarse activity state exposed to the status bar and the
// headless protocol.
type UIState string

const (
	StateIdle      UIState = "idle"
	StateRunning   UIState = "running"
	StateStreaming UIState = "streaming"
	StateTool      UIState = "tool"
	StateError     UIState = "error"
)

// OverlayKind identifies the top modal overlay.
type OverlayKind string

const (
	OverlayNone    OverlayKind = "none"
	OverlayHelp    OverlayKind = "help"
	OverlayDetails OverlayKind = "details"
	OverlayTable   OverlayKind = "table"
	OverlayFile    OverlayKind = "file"
	OverlayModel   OverlayKind = "model"
	OverlayEffort  OverlayKind = "effort"
	OverlayImage   OverlayKind = "image"
)

// ModelChoice is one catalog-backed model in the picker. Switchable is false
// only for the offline recorded-model fallback, which cannot be applied as a
// new selection until the catalog loads.
type ModelChoice struct {
	ID               string
	Name             string
	Dialect          model.Dialect
	ContextWindow    int
	InputLimit       int
	OutputLimit      int
	Status           string // "", "alpha", "beta", or "deprecated"
	Translated       bool
	Switchable       bool
	ReasoningEfforts []string
}

// ModelRefresher is an optional Host capability for the model picker's
// explicit catalog refresh. It bypasses the implicit catalog cache policy.
type ModelRefresher interface {
	RefreshModels(ctx context.Context) ([]ModelChoice, error)
}

// ModelSelector is an optional Host capability that resolves and applies a
// model for the next run.
type ModelSelector interface {
	SelectModel(id string) (ModelChoice, error)
}

// ModelWarningProvider is an optional Host capability that consumes the
// warning produced by the last successful SelectModel call, such as a clamped
// explicit context budget.
type ModelWarningProvider interface {
	TakeModelWarning() string
}

// Info is the immutable session header information.
type Info struct {
	SessionID      string
	TracePath      string
	Model          string
	Dialect        string
	Workspace      string
	MaxTurns       int
	MaxTotalTokens int
	Turns          int
	Usage          model.Usage
	ThinkingEffort string
}

// Submission is one message to record and run.
type Submission struct {
	Text   string
	Source string // "tui", "-task", or "-task-file"
}

// RunOptions selects the model and thinking effort for the next run.
type RunOptions struct {
	Model             string // empty inherits the current model
	ThinkingEffort    string // empty means the provider default
	ThinkingEffortSet bool   // ThinkingEffort replaces the inherited value
}

// Run is one in-flight run handle.
type Run interface {
	Interrupt()
	Terminate()
	Done() <-chan agent.Result
}

// Host owns the session, workspace, provider adapters, and runner for the TUI.
type Host interface {
	Info() Info
	Models() []ModelChoice
	History() ([]agent.HistoryItem, error)
	Start(sub Submission, opts RunOptions, steer <-chan string, display func(agent.DisplayEvent) error) (Run, error)
	Close() error
}

// WorkspaceProvider is an optional Host capability used by the file picker and
// image resolver. It exposes the approved workspace root; no other path is
// read.
type WorkspaceProvider interface {
	Workspace() *tools.Workspace
}

// Details is optional host metadata for the details overlay.
type Details struct {
	TracePath        string
	StateDir         string
	BaseURL          string
	Provider         string
	PromptVersion    string
	ToolsetVersion   string
	HarnessVersion   string
	MaxContextTokens int
	MaxOutputTokens  int
	MaxOutputUnset   bool
	SessionTimeout   time.Duration
	ModelTimeout     time.Duration
	ThinkingEffort   string
}

// DetailsProvider is an optional Host capability for the details overlay.
type DetailsProvider interface {
	Details() Details
}

// TerminalInfo describes the terminal capabilities surfaced by the details
// overlay.
type TerminalInfo struct {
	Name    string
	Version string
}

// Options configures the TUI model.
type Options struct {
	Host          Host
	Models        []ModelChoice
	Model         string
	Dialect       model.Dialect
	Seed          *Submission
	Headless      bool
	Events        chan<- any
	Acks          chan<- int
	ColorProfile  string
	NoColor       bool
	ImageProtocol string
	Terminal      TerminalInfo
}

// StateSnapshot is the reply to a headless state query.
type StateSnapshot struct {
	State       UIState
	Overlay     OverlayKind
	RunActive   bool
	RunStarted  uint64
	RunFinished uint64
	Composer    string
}

// Protocol semantic event payloads. They are display-only and never durable.
type (
	EventRunStarted struct {
		Run            int
		Model          string
		Dialect        string
		ThinkingEffort string
	}
	EventRunFinished struct {
		Status   agent.StopReason
		Reason   string
		RunTurns int
		RunUsage model.Usage
		Turns    int
		Usage    model.Usage
	}
	EventToolStarted struct {
		CallID    string
		Name      string
		Arguments string
	}
	EventToolFinished struct {
		CallID    string
		Name      string
		OK        bool
		Code      string
		ExitCode  int
		Truncated bool
		TimedOut  bool
	}
	EventModelChanged struct {
		Model          string
		Dialect        string
		ThinkingEffort string
	}
	EventNotice struct {
		Kind string
		Text string
	}
	EventError struct {
		Code    string
		Message string
	}
)

// Messages used by the headless driver.
type (
	inputTextMsg         struct{ text string }
	inputPasteMsg        struct{ text string }
	syncMsg              struct{ id int }
	stateQueryMsg        struct{ reply chan StateSnapshot }
	frameQueryMsg        struct{ reply chan frameQueryReply }
	shutdownMsg          struct{ code int }
	terminalInterruptMsg struct{}
	seedMsg              struct{}
	displayEventMsg      struct {
		run int
		ev  agent.DisplayEvent
	}
	displayClosedMsg struct{}
	modelRefreshMsg  struct {
		seq     int
		choices []ModelChoice
		err     error
	}
	runDoneMsg struct {
		run    int
		result agent.Result
	}
	coalesceMsg struct{}
	tickMsg     struct{}
)

const (
	headlessSettle = 30 * time.Millisecond
	coalesceDelay  = 16 * time.Millisecond
)

// Init starts the optional seeded-task timer. Headless drivers submit the seed
// after their first frame instead.
func (m *App) Init() tea.Cmd {
	if m.headless || m.seed == nil {
		return nil
	}
	return tea.Tick(20*time.Millisecond, func(time.Time) tea.Msg { return seedMsg{} })
}

// Update implements tea.Model.
func (m *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ok, cmd := m.forwardPictureMsg(msg); ok {
		return m, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		cmd := m.setSize(msg.Width, msg.Height)
		return m, cmd
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	case tea.PasteMsg:
		m.insertInput(Sanitize(msg.Content))
		return m, nil
	case inputTextMsg:
		m.insertInput(Sanitize(msg.text))
		return m, nil
	case imageRenderMsg:
		return m, m.handleImageRender(msg)
	case inputPasteMsg:
		m.insertInput(Sanitize(msg.text))
		return m, nil
	case syncMsg:
		id := msg.id
		return m, func() tea.Msg {
			time.Sleep(headlessSettle)
			if m.acks != nil {
				select {
				case m.acks <- id:
				default:
				}
			}
			return coalesceMsg{}
		}
	case stateQueryMsg:
		snap := m.snapshot()
		select {
		case msg.reply <- snap:
		default:
		}
		return m, nil
	case frameQueryMsg:
		m.ensureContent()
		reply := frameQueryReply{state: m.snapshot(), images: m.Images()}
		select {
		case msg.reply <- reply:
		default:
		}
		return m, nil
	case seedMsg:
		if m.seed == nil || m.seedSent {
			return m, nil
		}
		m.seedSent = true
		return m, m.submit(m.seed.Text, m.seed.Source)
	case displayEventMsg:
		if msg.run != m.run.number || m.run.closed {
			m.run.readPending = false
			return m, nil
		}
		m.run.readPending = false
		cmd := m.handleDisplay(msg.ev)
		if m.run.draining {
			return m, m.waitDisplayCmd()
		}
		return m, cmd
	case displayClosedMsg:
		m.run.readPending = false
		if m.run.draining {
			return m, m.completeRun()
		}
		return m, nil
	case modelRefreshMsg:
		m.handleModelRefresh(msg)
		return m, nil
	case runDoneMsg:
		if msg.run != m.run.number {
			return m, nil
		}
		return m, m.handleRunDone(msg.result)
	case coalesceMsg:
		m.coalesceScheduled = false
		m.ensureContent()
		return m, nil
	case tickMsg:
		if m.headless || !m.run.active {
			return m, nil
		}
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
	case shutdownMsg:
		return m, m.requestQuit(msg.code, true)
	case terminalInterruptMsg:
		return m, m.handleCtrlC()
	case tea.InterruptMsg:
		return m, m.handleCtrlC()
	case tea.QuitMsg:
		m.cancelModelRefresh()
		return m, tea.Quit
	}
	return m, nil
}

func (m *App) snapshot() StateSnapshot {
	return StateSnapshot{
		State:       m.currentState(),
		Overlay:     m.overlay.kind,
		RunActive:   m.run.active,
		RunStarted:  m.runStartedCount,
		RunFinished: m.runFinishedCount,
		Composer:    m.composer.Value(),
	}
}

func (m *App) setSize(width, height int) tea.Cmd {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	m.width = width
	m.height = height
	m.ready = true
	m.layout()
	cmd := m.overlay.resizeCmd(m, width, height)
	m.ensureContent()
	return cmd
}

func (m *App) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	header, composerH, status := 1, m.composerHeight(), 1
	transcript := max(1, m.height-header-composerH-status)
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(transcript)
	m.composer.SetWidth(max(1, m.width-2))
	m.composer.SetHeight(m.editorHeight())
}

// editorHeight is the number of composer rows, excluding the completion menu.
func (m *App) editorHeight() int {
	lines := max(m.composer.LineCount(), 1)
	return min(lines, 6)
}

// visibleSuggestionCount bounds the completion menu so the header, one
// transcript row, the editor, and the status line always fit.
func (m *App) visibleSuggestionCount() int {
	if len(m.suggestions) == 0 {
		return 0
	}
	count := min(len(m.suggestions), maxSlashSuggestions)
	if m.height > 0 {
		available := m.height - m.editorHeight() - 3
		count = min(count, max(0, available))
	}
	return count
}

// composerHeight includes the completion menu because both sit above the
// status line and take space from the transcript.
func (m *App) composerHeight() int {
	return m.editorHeight() + m.visibleSuggestionCount()
}

// insertInput routes text to the focused overlay filter or the composer.
func (m *App) insertInput(text string) {
	if text == "" {
		return
	}
	if m.overlay.kind != OverlayNone {
		if m.overlay.insert(m, text) {
			return
		}
	}
	current := m.composer.Value()
	if len(current)+len(text) > maxSubmissionBytes {
		m.notice = "input cap reached (64 KiB)"
		return
	}
	m.composer.InsertString(text)
	m.updateSuggestions()
	m.layout()
}

func (m *App) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	keystroke := msg.Keystroke()

	// Global keys. Ctrl-C and Ctrl-D remain global even in an overlay.
	switch keystroke {
	case "ctrl+c":
		return m.handleCtrlC()
	case "ctrl+d":
		if m.run.active {
			return m.requestQuit(130, true)
		}
		return m.requestQuit(0, false)
	case "ctrl+l":
		return func() tea.Msg { return tea.ClearScreen() }
	case "ctrl+r":
		if m.overlay.kind == OverlayModel {
			return m.refreshModelCatalog()
		}
		m.setRich(!m.rich)
		return nil
	case "ctrl+t":
		return m.openTableOverlay()
	case "ctrl+g":
		return m.toggleImageViewer()
	case "f1":
		return m.openHelpOverlay()
	case "f2":
		return m.openEffortOverlay()
	case "ctrl+x":
		return m.openDetailsOverlay()
	}

	if m.overlay.kind != OverlayNone {
		if keystroke == "esc" {
			return m.closeOverlayCmd()
		}
		if cmd := m.overlay.update(m, msg); cmd != nil {
			return cmd
		}
		m.overlay.resize(m, m.width, m.height)
		return nil
	}

	m.updateSuggestions()
	if m.suggestionsVisible() {
		switch keystroke {
		case "up":
			m.moveSuggestion(-1)
			return nil
		case "down":
			m.moveSuggestion(1)
			return nil
		case "tab":
			return m.completeSuggestion()
		case "esc":
			m.dismissSuggestions()
			m.layout()
			return nil
		case "enter", "ctrl+m":
			if !msg.Mod.Contains(tea.ModShift) && !msg.Mod.Contains(tea.ModAlt) {
				return m.runSuggestion()
			}
		}
	}

	if m.run.active {
		switch keystroke {
		case "esc":
			m.interruptOperation()
			return nil
		case "ctrl+p":
			m.notice = "the model picker is disabled while a run is active"
			return nil
		case "ctrl+o":
			m.notice = "the file picker is disabled while a run is active"
			return nil
		}
	}

	switch keystroke {
	case "esc":
		if m.composer.Value() != "" {
			m.clearComposer()
			m.layout()
			return nil
		}
		m.interruptOperation()
		return nil
	case "enter", "ctrl+m":
		if msg.Mod.Contains(tea.ModShift) || msg.Mod.Contains(tea.ModAlt) {
			m.insertInput("\n")
			return nil
		}
		text := m.composer.Value()
		m.clearComposer()
		m.layout()
		return m.submit(text, "tui")
	case "tab":
		m.insertInput("    ")
		return nil
	case "up":
		m.recallHistory(-1)
		return nil
	case "down":
		m.recallHistory(1)
		return nil
	case "pgup":
		m.follow = false
		m.viewport.PageUp()
		return nil
	case "pgdown":
		m.viewport.PageDown()
		m.follow = m.viewport.AtBottom()
		return nil
	case "ctrl+u":
		m.clearComposer()
		m.layout()
		return nil
	case "ctrl+p":
		if !m.run.active {
			return m.openModelOverlay()
		}
	case "ctrl+o":
		if !m.run.active {
			return m.openFileOverlay()
		}
	}

	// Printable input is sanitized before it reaches the textarea.
	if text := msg.Text; text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModMeta) {
		m.insertInput(Sanitize(text))
		return nil
	}

	updated, cmd := m.composer.Update(msg)
	m.composer = updated
	m.updateSuggestions()
	if m.composer.Value() != "" {
		m.notice = ""
	}
	m.layout()
	return cmd
}

// handleCtrlC interrupts an operation, then cancels the run on a second press.
func (m *App) handleCtrlC() tea.Cmd {
	if !m.run.active {
		if m.composer.Value() != "" {
			m.clearComposer()
			m.layout()
			m.notice = ""
		} else {
			m.notice = "use Ctrl-D to quit"
		}
		return nil
	}
	if m.run.ctrlArmed {
		m.run.ctrlArmed = false
		m.notice = "canceling the run"
		if m.run.handle != nil {
			m.run.handle.Terminate()
		}
		return nil
	}
	m.run.ctrlArmed = true
	m.interruptOperation()
	return nil
}

// setRich selects the assistant rendering mode used by the transcript.
func (m *App) setRich(rich bool) {
	m.rich = rich
	m.invalidateRenders()
	m.ensureContent()
	if rich {
		m.notice = "rich rendering"
	} else {
		m.notice = "raw rendering"
	}
}

func (m *App) interruptOperation() {
	if m.run.handle == nil {
		return
	}
	m.run.handle.Interrupt()
	m.notice = "interrupt requested"
}

// requestQuit terminates an active run before quitting, or quits immediately
// when idle.
func (m *App) requestQuit(code int, interrupt bool) tea.Cmd {
	m.cancelModelRefresh()
	if m.run.active {
		m.quitAfter = true
		m.quitCode = code
		if interrupt && m.run.handle != nil {
			m.run.handle.Terminate()
		}
		return nil
	}
	if code != 0 {
		m.quitCode = code
	}
	return tea.Batch(m.clearImageViewerCmd(), tea.Quit)
}

// ExitCode is the process exit code selected by the app.
func (m *App) ExitCode() int {
	if m.fatal {
		return 1
	}
	return m.quitCode
}

func (m *App) startRun(text, source string) tea.Cmd {
	if m.host == nil {
		m.appendNotice(agent.NoticeError, "no session host")
		return nil
	}
	m.nextRun++
	header := &transcriptItem{kind: itemRun, run: m.nextRun, model: m.nextModel, dialect: string(m.nextDialect)}
	userItem := &transcriptItem{kind: itemUser, run: m.nextRun, source: source, text: text, acked: true}
	m.addItem(header)
	m.addItem(userItem)
	m.pushHistory(text)
	m.clearComposer()
	m.layout()
	m.follow = true
	m.notice = ""
	m.historyIndex = len(m.inputHistory)

	events := make(chan agent.DisplayEvent, 256)
	steer := make(chan string, 64)
	stop := make(chan struct{})
	display := func(ev agent.DisplayEvent) error {
		select {
		case events <- ev:
			return nil
		case <-stop:
			return nil
		}
	}
	run, err := m.host.Start(Submission{Text: text, Source: source}, RunOptions{
		Model:             m.nextModel,
		ThinkingEffort:    m.nextEffort,
		ThinkingEffortSet: true,
	}, steer, display)
	if err != nil {
		close(stop)
		m.removeItem(userItem)
		m.removeItem(header)
		if m.composer.Value() == "" {
			m.composer.SetValue(text)
		}
		m.layout()
		m.appendNotice(agent.NoticeError, "start run: "+err.Error())
		m.emit(EventError{Code: "start_failed", Message: SanitizeLine(err.Error())})
		return nil
	}
	m.steerCh = steer
	m.run = liveRun{
		active:  true,
		handle:  run,
		number:  m.nextRun,
		model:   m.nextModel,
		dialect: string(m.nextDialect),
		state:   StateRunning,
		started: time.Now(),
		pending: make(map[string]*transcriptItem),
		events:  events,
		stop:    stop,
	}
	cmds := []tea.Cmd{m.waitDisplayCmd(), m.waitDoneCmd(run)}
	if !m.headless {
		cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} }))
	}
	return tea.Batch(cmds...)
}

func (m *App) waitDisplayCmd() tea.Cmd {
	ch := m.run.events
	if ch == nil {
		return nil
	}
	m.run.readPending = true
	runNo := m.run.number
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return displayClosedMsg{}
		}
		return displayEventMsg{run: runNo, ev: ev}
	}
}

func (m *App) waitDoneCmd(run Run) tea.Cmd {
	if run == nil {
		return nil
	}
	runNo := m.run.number
	return func() tea.Msg {
		return runDoneMsg{run: runNo, result: <-run.Done()}
	}
}

func (m *App) handleDisplay(ev agent.DisplayEvent) tea.Cmd {
	if ev == nil {
		return m.waitDisplayCmd()
	}
	switch ev := ev.(type) {
	case agent.RunStarted:
		m.run.model = ev.Model
		m.run.dialect = ev.Dialect
		m.nextEffort = ev.ThinkingEffort
		m.run.state = StateRunning
		m.dirty = true
		m.runStartedCount++
		m.emit(EventRunStarted{
			Run:            m.run.number,
			Model:          ev.Model,
			Dialect:        ev.Dialect,
			ThinkingEffort: ev.ThinkingEffort,
		})
		if header := m.runItem(); header != nil {
			header.model = ev.Model
			header.dialect = ev.Dialect
		}
	case agent.ModelStarted:
		m.run.state = StateRunning
		m.run.turn = ev.Turn
	case agent.TextDelta:
		if ev.Text == "" {
			return m.waitDisplayCmd()
		}
		m.run.state = StateStreaming
		if m.run.streamed == nil {
			m.run.streamed = m.appendAssistant(m.run.turn, "")
		}
		m.run.streamed.text = CapText(m.run.streamed.text+Sanitize(ev.Text), maxTranscriptItemText)
		m.dirty = true
		m.run.streamed.renderedSet = false
		m.follow = true
		if m.coalesceScheduled {
			return m.waitDisplayCmd()
		}
		m.coalesceScheduled = true
		return tea.Batch(m.waitDisplayCmd(), tea.Tick(coalesceDelay, func(time.Time) tea.Msg {
			return coalesceMsg{}
		}))
	case agent.ResponseCompleted:
		m.run.state = StateRunning
		m.dirty = true
		text := CapText(Sanitize(ev.Text), maxTranscriptItemText)
		if m.run.streamed == nil {
			if text != "" {
				m.run.streamed = m.appendAssistant(ev.Turn, text)
			}
		} else {
			m.run.streamed.text = text
			m.run.streamed.renderedSet = false
		}
		m.run.streamed = nil
		for _, call := range ev.ToolCalls {
			it := &transcriptItem{
				kind:    itemTool,
				run:     m.run.number,
				turn:    ev.Turn,
				callID:  SanitizeLine(call.ID),
				name:    SanitizeLine(call.Name),
				args:    CapText(SanitizeLine(string(call.Arguments)), 2<<10),
				pending: true,
			}
			m.addItem(it)
			m.run.pending[it.callID] = it
		}
		m.ensureContent()
	case agent.ToolStarted:
		m.run.state = StateTool
		it := m.run.pending[ev.Call.ID]
		if it == nil {
			it = &transcriptItem{
				kind:   itemTool,
				run:    m.run.number,
				turn:   ev.Turn,
				callID: SanitizeLine(ev.Call.ID),
				name:   SanitizeLine(ev.Call.Name),
			}
			m.addItem(it)
			m.run.pending[it.callID] = it
		}
		it.running = true
		m.dirty = true
		it.args = CapText(SanitizeLine(string(ev.Call.Arguments)), 2<<10)
		m.emit(EventToolStarted{CallID: it.callID, Name: it.name, Arguments: it.args})
		m.dirty = true
		m.ensureContent()
	case agent.ToolCompleted:
		m.run.state = StateRunning
		it := m.toolItem(ev.CallID)
		if it == nil {
			it = &transcriptItem{
				kind:   itemTool,
				run:    m.run.number,
				turn:   ev.Turn,
				callID: SanitizeLine(ev.CallID),
				name:   SanitizeLine(ev.Name),
			}
			m.addItem(it)
		}
		it.pending = false
		m.dirty = true
		it.running = false
		it.ok = ev.OK
		it.code = string(ev.Code)
		it.output = CapText(Sanitize(ev.Output), maxTranscriptItemText)
		it.errText = CapText(Sanitize(ev.Error), maxTranscriptItemText)
		it.exitCode = ev.ExitCode
		it.truncated = ev.Truncated
		it.timedOut = ev.TimedOut
		it.toolTime = ev.Duration
		delete(m.run.pending, it.callID)
		m.dirty = true
		m.emit(EventToolFinished{
			CallID:    it.callID,
			Name:      it.name,
			OK:        it.ok,
			Code:      it.code,
			ExitCode:  it.exitCode,
			Truncated: it.truncated,
			TimedOut:  it.timedOut,
		})
		m.ensureContent()
	case agent.MessageRecorded:
		m.ackMessage(ev)
	case agent.Notice:
		m.appendNotice(ev.Kind, ev.Text)
		if ev.Kind == agent.NoticeError {
			m.run.state = StateError
		}
		m.emit(EventNotice{Kind: string(ev.Kind), Text: SanitizeLine(ev.Text)})
		m.dirty = true
		m.ensureContent()
	case agent.RunFinished:
		m.applyResult(ev.Result, ev.Reason, ev.Duration, true)
		return nil
	}
	return m.waitDisplayCmd()
}

func (m *App) handleRunDone(result agent.Result) tea.Cmd {
	m.run.doneResult = &result
	m.run.draining = true
	if m.run.events == nil {
		return m.completeRun()
	}
	if !m.run.eventsClosed {
		// The runner has returned, so closing the channel cannot race a send.
		// One read is kept in flight at all times; buffered events are handled
		// in order before the closed channel completes the run.
		close(m.run.events)
		m.run.eventsClosed = true
	}
	if m.run.readPending {
		return nil
	}
	return m.waitDisplayCmd()
}

// completeRun finalizes a run after all display events and the run result have
// been observed.
func (m *App) completeRun() tea.Cmd {
	if m.run.stop != nil {
		close(m.run.stop)
		m.run.stop = nil
	}
	m.run.draining = false
	m.run.closed = true
	m.run.readPending = false
	if !m.run.finished && m.run.doneResult != nil {
		m.applyResult(*m.run.doneResult, "", 0, false)
	}
	m.run.active = false
	m.run.handle = nil
	m.run.doneResult = nil
	return m.finishQuit()
}

func (m *App) finishQuit() tea.Cmd {
	if m.quitAfter || m.fatal {
		return tea.Batch(m.clearImageViewerCmd(), tea.Quit)
	}
	return nil
}

// applyResult records a terminal run status once and updates the session
// counters shown in the header.
func (m *App) applyResult(result agent.Result, reason string, duration time.Duration, fromDisplay bool) {
	_ = fromDisplay
	if m.run.finished {
		return
	}
	status := result.Status
	if status == "" {
		status = agent.StopInternalError
	}
	if header := m.runItem(); header != nil {
		header.status = status
		header.reason = SanitizeLine(reason)
		header.duration = duration
		header.turns = result.RunTurns
		header.usage = result.RunUsage
	}
	m.info.Turns = result.CumulativeTurns
	m.info.Usage = result.CumulativeUsage
	if m.run.model != "" {
		m.info.Model = m.run.model
		m.info.Dialect = m.run.dialect
	}
	// Requeue unacknowledged steering lines rather than losing them.
	var recovered []string
	for _, entry := range m.steer {
		if entry.acked {
			continue
		}
		recovered = append(recovered, entry.text)
		m.removeItem(entry.item)
	}
	if len(recovered) > 0 {
		text := strings.Join(recovered, "\n")
		if m.composer.Value() == "" {
			m.composer.SetValue(text)
		} else {
			m.composer.SetValue(text + "\n" + m.composer.Value())
		}
		m.dismissSuggestions()
		m.appendNotice(agent.NoticeWarning, "unacknowledged steering returned to the composer")
		m.layout()
	}
	m.steer = nil
	m.steerCh = nil
	m.run.pending = map[string]*transcriptItem{}
	m.run.streamed = nil
	m.run.ctrlArmed = false
	alreadyFinished := m.run.finished
	m.run.finished = true
	m.runFinishedCount++
	m.run.state = StateIdle
	m.dirty = true
	if !alreadyFinished {
		m.emit(EventRunFinished{
			Status:   status,
			Reason:   SanitizeLine(reason),
			RunTurns: result.RunTurns,
			RunUsage: result.RunUsage,
			Turns:    result.CumulativeTurns,
			Usage:    result.CumulativeUsage,
		})
	}
	switch status {
	case agent.StopInternalError:
		m.fatal = true
		m.appendNotice(agent.NoticeError, "fatal internal error; the session log cannot be trusted; exiting")
		m.emit(EventError{Code: "internal_error", Message: "fatal internal error"})
	}
	m.notice = ""
	m.follow = true
	m.ensureContent()
}

func (m *App) ackMessage(ev agent.MessageRecorded) {
	text := CapText(Sanitize(ev.Text), maxSubmissionBytes)
	switch ev.Source {
	case "steer":
		for i := range m.steer {
			if !m.steer[i].acked && m.steer[i].text == text {
				m.steer[i].acked = true
				if m.steer[i].item != nil {
					m.steer[i].item.queued = false
					m.steer[i].item.acked = true
				}
				m.notice = "steering sent"
				return
			}
		}
		m.addItem(&transcriptItem{kind: itemUser, run: m.run.number, source: "steer", text: text, acked: true})
		m.dirty = true
	case "interrupt-hint", "continuation-hint", "stuck-hint":
		m.addItem(&transcriptItem{kind: itemNotice, noticeKind: agent.NoticeWarning, text: text, source: ev.Source})
		m.dirty = true
	default:
		// The optimistic user item is already shown.
	}
	m.ensureContent()
}

func (m *App) removeItem(target *transcriptItem) {
	if target == nil {
		return
	}
	for i, it := range m.items {
		if it == target {
			m.textSize -= len(it.text) + len(it.output) + len(it.errText) + len(it.args)
			m.items = append(m.items[:i], m.items[i+1:]...)
			return
		}
	}
}

func (m *App) invalidateRenders() {
	m.dirty = true
	for _, it := range m.items {
		it.renderedSet = false
	}
}
