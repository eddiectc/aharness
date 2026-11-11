package tui

import (
	"context"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/tools"
)

// Display caps. The session log is never capped by the UI.
const (
	maxSubmissionBytes    = 64 << 10
	maxTranscriptItems    = 20_000
	maxTranscriptText     = 16 << 20
	maxTranscriptItemText = 64 << 10
	maxInputHistory       = 100
	escapeTimeout         = 50 * time.Millisecond
)

type itemKind string

const (
	itemRun       itemKind = "run"
	itemUser      itemKind = "user"
	itemAssistant itemKind = "assistant"
	itemTool      itemKind = "tool"
	itemNotice    itemKind = "notice"
	itemImage     itemKind = "image"
)

// transcriptItem is one display-only transcript entry. Assistant text is
// markdown source; rendered caches are keyed by width and rich/raw mode.
type transcriptItem struct {
	kind   itemKind
	run    int
	turn   int
	source string
	text   string

	rendered      string
	renderedWidth int
	renderedRich  bool
	renderedSet   bool

	// Run header.
	model    string
	dialect  string
	status   agent.StopReason
	reason   string
	duration time.Duration
	turns    int
	usage    model.Usage

	// User item.
	queued bool
	acked  bool

	// Tool item.
	callID    string
	name      string
	args      string
	pending   bool
	running   bool
	ok        bool
	code      string
	output    string
	errText   string
	exitCode  int
	truncated bool
	timedOut  bool
	toolTime  time.Duration

	// Notice.
	noticeKind agent.NoticeKind

	// Image.
	image *imageBlock
}

type frameQueryReply struct {
	state  StateSnapshot
	images []imageRef
}

type steerEntry struct {
	text  string
	item  *transcriptItem
	acked bool
}

type liveRun struct {
	active       bool
	handle       Run
	number       int
	model        string
	dialect      string
	state        UIState
	turn         int
	started      time.Time
	streamed     *transcriptItem
	pending      map[string]*transcriptItem
	ctrlArmed    bool
	finished     bool
	closed       bool
	draining     bool
	eventsClosed bool
	readPending  bool
	doneResult   *agent.Result
	events       chan agent.DisplayEvent
	stop         chan struct{}
}

// App is the Bubble Tea model for one TUI session.
type App struct {
	opts Options
	host Host
	info Info

	width  int
	height int
	ready  bool

	items     []*transcriptItem
	textSize  int
	imageRefs []imageRef

	viewport          viewport.Model
	composer          textarea.Model
	follow            bool
	dirty             bool
	contentWidth      int
	coalesceScheduled bool

	rich     bool
	headless bool

	run         liveRun
	nextRun     int
	steer       []steerEntry
	steerCh     chan string
	nextModel   string
	nextDialect model.Dialect
	nextEffort  string
	models      []ModelChoice
	acks        chan<- int

	refreshCancel context.CancelFunc
	refreshSeq    int

	inputHistory []string
	historyIndex int
	draft        string

	notice string

	overlay    overlayState
	images     []*imageBlock
	imageCache map[string]*imageBlock

	colorProfile  string
	noColor       bool
	imageProtocol string

	startedAt time.Time
	quitAfter bool
	quitCode  int
	fatal     bool

	seed     *Submission
	seedSent bool

	runStartedCount  uint64
	runFinishedCount uint64

	events chan<- any
}

// New creates the TUI application model. It does not start a run.
func New(opts Options) *App {
	ta := textarea.New()
	ta.Prompt = "> "
	ta.ShowLineNumbers = false
	ta.SetVirtualCursor(false)
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.Placeholder = "Send a task or steering message (Enter)"
	ta.Focus()

	vp := viewport.New()
	vp.SoftWrap = true
	vp.FillHeight = true
	vp.KeyMap = viewport.KeyMap{}

	app := &App{
		opts:          opts,
		host:          opts.Host,
		composer:      ta,
		viewport:      vp,
		follow:        true,
		rich:          true,
		headless:      opts.Headless,
		nextModel:     opts.Model,
		nextDialect:   opts.Dialect,
		models:        opts.Models,
		colorProfile:  opts.ColorProfile,
		noColor:       opts.NoColor,
		imageProtocol: opts.ImageProtocol,
		imageCache:    make(map[string]*imageBlock),
		seed:          opts.Seed,
		events:        opts.Events,
		acks:          opts.Acks,
		overlay:       overlayState{kind: OverlayNone},
		startedAt:     time.Now(),
	}
	if app.imageProtocol == "" {
		app.imageProtocol = "glyph"
	}
	app.info.Model = opts.Model
	app.info.Dialect = string(opts.Dialect)
	if opts.Host != nil {
		app.info = opts.Host.Info()
		app.nextEffort = app.info.ThinkingEffort
		if app.nextModel == "" {
			app.nextModel = app.info.Model
			app.nextDialect = model.Dialect(app.info.Dialect)
		}
		if history, err := opts.Host.History(); err == nil {
			app.loadHistory(history)
		}
		// The host may have resolved the session with a warning (for example,
		// an inherited effort cleared by a model change). It is consumed here
		// exactly once, before any picker selection can set a new warning.
		if provider, ok := opts.Host.(ModelWarningProvider); ok {
			if warning := provider.TakeModelWarning(); warning != "" {
				app.appendNotice(agent.NoticeWarning, warning)
			}
		}
	}
	app.applyNoColor()
	return app
}

// SetInfo replaces the session header information.
func (m *App) SetInfo(info Info) {
	m.info = info
	if m.nextModel == "" {
		m.nextModel = info.Model
		m.nextDialect = model.Dialect(info.Dialect)
	}
}

// SetModels replaces the accepted-model registry.
func (m *App) SetModels(models []ModelChoice) { m.models = models }

func (m *App) loadHistory(items []agent.HistoryItem) {
	for _, h := range items {
		it := &transcriptItem{
			kind:      itemKind(h.Kind),
			run:       h.Run,
			turn:      h.Turn,
			source:    h.Source,
			text:      h.Text,
			callID:    h.CallID,
			name:      h.Name,
			pending:   h.Pending,
			ok:        h.OK,
			code:      h.Code,
			output:    h.Output,
			errText:   h.Error,
			exitCode:  h.ExitCode,
			truncated: h.Truncated,
			timedOut:  h.TimedOut,
			status:    h.Status,
			reason:    h.Reason,
			model:     h.Model,
			dialect:   h.Dialect,
			duration:  h.Duration,
			turns:     h.Turns,
			usage:     h.Usage,
		}
		if len(h.Arguments) > 0 {
			it.args = SanitizeLine(string(h.Arguments))
		}
		if it.kind == itemNotice {
			it.noticeKind = agent.NoticeWarning
		}
		m.addItem(it)
		if it.kind == itemRun {
			m.nextRun = max(m.nextRun, it.run)
		}
	}
	m.rebuildImages()
}

func (m *App) addItem(it *transcriptItem) {
	m.dirty = true
	m.items = append(m.items, it)
	m.textSize += len(it.text) + len(it.output) + len(it.errText) + len(it.args)
	for (len(m.items) > maxTranscriptItems || m.textSize > maxTranscriptText) && len(m.items) > 1 {
		dropped := m.items[0]
		m.textSize -= len(dropped.text) + len(dropped.output) + len(dropped.errText) + len(dropped.args)
		m.items = m.items[1:]
		if !m.hasOmittedMarker() {
			m.items = append([]*transcriptItem{{kind: itemNotice, text: "older history omitted by the display cap", noticeKind: agent.NoticeWarning}}, m.items...)
		}
	}
}

func (m *App) hasOmittedMarker() bool {
	return len(m.items) > 0 && m.items[0].kind == itemNotice && strings.Contains(m.items[0].text, "older history omitted")
}

func (m *App) appendNotice(kind agent.NoticeKind, text string) {
	m.addItem(&transcriptItem{kind: itemNotice, noticeKind: kind, text: CapText(Sanitize(text), 4<<10)})
}

func (m *App) appendAssistant(turn int, text string) *transcriptItem {
	it := &transcriptItem{kind: itemAssistant, run: m.run.number, turn: turn, text: CapText(Sanitize(text), maxTranscriptItemText)}
	m.addItem(it)
	return it
}

func (m *App) runItem() *transcriptItem {
	for _, v := range slices.Backward(m.items) {
		if v.kind == itemRun && v.run == m.run.number {
			return v
		}
	}
	return nil
}

func (m *App) toolItem(callID string) *transcriptItem {
	if it, ok := m.run.pending[callID]; ok {
		return it
	}
	for _, v := range slices.Backward(m.items) {
		if v.kind == itemTool && v.callID == callID {
			return v
		}
	}
	return nil
}

// submit accepts a composer submission. While a run is active it becomes
// steering; otherwise it starts a new run.
func (m *App) submit(text, source string) tea.Cmd {
	text = strings.TrimRight(Sanitize(text), "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if len(text) > maxSubmissionBytes {
		m.notice = "message rejected: over the 64 KiB limit"
		m.appendNotice(agent.NoticeWarning, m.notice)
		return nil
	}
	if m.run.active {
		return m.submitSteering(text)
	}
	return m.startRun(text, source)
}

func (m *App) submitSteering(text string) tea.Cmd {
	if len(text) > maxSubmissionBytes {
		m.notice = "steering rejected: over the 64 KiB limit"
		m.appendNotice(agent.NoticeWarning, m.notice)
		return nil
	}
	ch := m.steerCh
	if ch == nil {
		m.notice = "steering is unavailable"
		return nil
	}
	it := &transcriptItem{kind: itemUser, run: m.run.number, source: "steer", text: text, queued: true}
	m.addItem(it)
	select {
	case ch <- text:
		m.steer = append(m.steer, steerEntry{text: text, item: it})
		m.follow = true
		m.notice = "steering queued"
	default:
		m.removeItem(it)
		m.notice = "steering queue is full"
		m.appendNotice(agent.NoticeWarning, m.notice)
	}
	return nil
}

func (m *App) pushHistory(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if n := len(m.inputHistory); n > 0 && m.inputHistory[n-1] == text {
		return
	}
	m.inputHistory = append(m.inputHistory, text)
	if len(m.inputHistory) > maxInputHistory {
		m.inputHistory = m.inputHistory[len(m.inputHistory)-maxInputHistory:]
	}
}

func (m *App) recallHistory(delta int) {
	if len(m.inputHistory) == 0 {
		return
	}
	if m.historyIndex == len(m.inputHistory) {
		m.draft = m.composer.Value()
	}
	next := m.historyIndex + delta
	switch {
	case next < 0:
		next = 0
	case next > len(m.inputHistory):
		next = len(m.inputHistory)
	}
	m.historyIndex = next
	if next == len(m.inputHistory) {
		m.composer.SetValue(m.draft)
		return
	}
	m.composer.SetValue(m.inputHistory[next])
	m.composer.CursorEnd()
}

func (m *App) clearComposer() {
	m.composer.SetValue("")
	m.historyIndex = len(m.inputHistory)
	m.draft = ""
}

func (m *App) applyNoColor() {
	if !m.noColor {
		return
	}
	styles := textarea.DefaultStyles(true)
	styles.Focused.Text = styles.Focused.Text.Underline(false)
	styles.Blurred.Text = styles.Blurred.Text.Underline(false)
	m.composer.SetStyles(styles)
}

// currentState reports the state label shown in the status bar and protocol.
func (m *App) currentState() UIState {
	if m.run.active {
		if m.run.state != "" {
			return m.run.state
		}
		return StateRunning
	}
	return StateIdle
}

func (m *App) isWorkspace() *tools.Workspace {
	provider, ok := m.host.(WorkspaceProvider)
	if !ok {
		return nil
	}
	return provider.Workspace()
}

func (m *App) emit(ev any) {
	if m.events == nil {
		return
	}
	select {
	case m.events <- ev:
	default:
		// The protocol driver is the only consumer and is expected to keep
		// up; a display event is not worth blocking the update loop.
	}
}
