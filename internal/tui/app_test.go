package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/tools"
)

type manualHost struct {
	info     Info
	history  []agent.HistoryItem
	models   []ModelChoice
	ws       *tools.Workspace
	startErr error

	mu    sync.Mutex
	run   *manualRun
	calls []startCall
}

type startCall struct {
	sub     Submission
	opts    RunOptions
	steer   <-chan string
	display func(agent.DisplayEvent) error
}

type manualRun struct {
	done      chan agent.Result
	interrupt atomic.Int32
	terminate atomic.Int32
}

func (r *manualRun) Interrupt()                { r.interrupt.Add(1) }
func (r *manualRun) Terminate()                { r.terminate.Add(1) }
func (r *manualRun) Done() <-chan agent.Result { return r.done }

func (h *manualHost) Info() Info                            { return h.info }
func (h *manualHost) Models() []ModelChoice                 { return h.models }
func (h *manualHost) History() ([]agent.HistoryItem, error) { return h.history, nil }
func (h *manualHost) Close() error                          { return nil }
func (h *manualHost) Workspace() *tools.Workspace           { return h.ws }

func (h *manualHost) Start(sub Submission, opts RunOptions, steer <-chan string, display func(agent.DisplayEvent) error) (Run, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.startErr != nil {
		return nil, h.startErr
	}
	run := &manualRun{done: make(chan agent.Result, 1)}
	h.run = run
	h.calls = append(h.calls, startCall{sub: sub, opts: opts, steer: steer, display: display})
	return run, nil
}

func (h *manualHost) last() (*manualRun, startCall, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run == nil || len(h.calls) == 0 {
		return nil, startCall{}, false
	}
	return h.run, h.calls[len(h.calls)-1], true
}

func newManualHost() *manualHost {
	return &manualHost{
		info: Info{
			SessionID: "0190a0c4-0000-7000-8000-0000000000aa",
			TracePath: "/tmp/trace", Model: "deepseek-v4.1-flash", Dialect: string(model.DialectChat),
			Workspace: "/tmp/ws", MaxTurns: 30, MaxTotalTokens: 2_000_000,
		},
		models: []ModelChoice{
			{
				ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash",
				Dialect: model.DialectChat, ContextWindow: 200_000, InputLimit: 160_000,
				OutputLimit: 8_192, Switchable: true,
			},
			{
				ID: "crouton-alpha-free", Name: "Crouton Alpha Free",
				Dialect: model.DialectResponses, ContextWindow: 1_000_000, InputLimit: 900_000,
				Status: "alpha", Translated: true, Switchable: true,
			},
		},
	}
}

func newTestApp(t *testing.T, host Host) *App {
	t.Helper()
	app, _ := newTestAppWithEvents(t, host)
	return app
}

func newTestAppWithEvents(t *testing.T, host Host) (*App, chan any) {
	t.Helper()
	info := host.Info()
	events := make(chan any, 64)
	app := New(Options{
		Host: host, Models: host.Models(), Model: info.Model, Dialect: model.Dialect(info.Dialect),
		Headless: true, Events: events, Acks: make(chan int, 4), ImageProtocol: "glyph",
	})
	app.setSize(80, 24)
	return app, events
}

func updateApp(t *testing.T, app *App, msg tea.Msg) tea.Cmd {
	t.Helper()
	model, cmd := app.Update(msg)
	if model != app {
		t.Fatalf("Update returned a different model")
	}
	return cmd
}

func TestThinkingStreamsIntoTheTranscript(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "use thinking")

	deliver(app, agent.ModelStarted{Turn: 1, Attempt: 1})
	deliver(app, agent.ReasoningDelta{Text: "look at "})
	deliver(app, agent.ReasoningDelta{Text: "the file"})
	deliver(app, agent.TextDelta{Text: "done"})
	deliver(app, agent.ResponseCompleted{
		Turn: 1, Text: "done", Reasoning: "look at the file",
		FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 1, OutputTokens: 2},
	})

	thinking := app.items[len(app.items)-2]
	if thinking.kind != itemThinking {
		t.Fatalf("item kinds = %v", itemKinds(app.items))
	}
	if thinking.text != "look at the file" {
		t.Fatalf("thinking text = %q", thinking.text)
	}
	if thinking.thinking {
		t.Fatal("settled thinking block is still marked streaming")
	}
	if app.run.thinking != nil {
		t.Fatal("thinking pointer was not cleared on response completion")
	}
	rendered := strings.Join(app.transcriptLines(), "\n")
	if !strings.Contains(rendered, "thinking") || !strings.Contains(rendered, "look at the file") {
		t.Fatalf("transcript lacks the thinking block:\n%s", rendered)
	}
	if !strings.Contains(rendered, "assistant") || !strings.Contains(rendered, "done") {
		t.Fatalf("transcript lacks the assistant text:\n%s", rendered)
	}

	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
}

func TestStreamingViewDefersTranscriptRebuildsToTheCoalesceTick(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "stream fast")

	deliver(app, agent.ModelStarted{Turn: 1, Attempt: 1})
	deliver(app, agent.ReasoningDelta{Text: "first "})
	// Bubble Tea calls View after every message. While a coalesced rebuild is
	// pending, View must paint the previous frame instead of re-rendering the
	// growing thinking block once per token.
	app.View()
	if !app.dirty {
		t.Fatal("View rebuilt the transcript before the coalesce tick")
	}
	deliver(app, agent.ReasoningDelta{Text: "second"})
	app.View()
	if !app.dirty {
		t.Fatal("View rebuilt the transcript before the coalesce tick")
	}

	updateApp(t, app, coalesceMsg{})
	if app.dirty {
		t.Fatal("coalesce tick did not rebuild the transcript")
	}
	rendered := strings.Join(app.transcriptLines(), "\n")
	if !strings.Contains(rendered, "first second") {
		t.Fatalf("transcript lacks the streamed thinking text:\n%s", rendered)
	}

	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
}

func TestThinkingShownForABufferedResponse(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "buffered")

	// A provider that returned a completed response without reasoning deltas
	// still shows its thinking, once, above the answer it explains.
	deliver(app, agent.ResponseCompleted{
		Turn: 1, Text: "answer", Reasoning: "one thought",
		FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 1, OutputTokens: 2},
	})
	var blocks []*transcriptItem
	var assistantIndex, thinkingIndex int
	for i, item := range app.items {
		switch item.kind {
		case itemThinking:
			blocks = append(blocks, item)
			thinkingIndex = i
		case itemAssistant:
			assistantIndex = i
		}
	}
	if len(blocks) != 1 || blocks[0].text != "one thought" || blocks[0].thinking {
		t.Fatalf("thinking blocks = %+v", blocks)
	}
	if thinkingIndex > assistantIndex {
		t.Fatalf("thinking item %d is below the assistant item %d", thinkingIndex, assistantIndex)
	}

	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
}

func TestThinkingAbsentWhenTheProviderSendsNone(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "no thinking")
	deliver(app, agent.ResponseCompleted{
		Turn: 1, Text: "answer", FinishReason: model.FinishStop,
		Usage: model.Usage{InputTokens: 1, OutputTokens: 2},
	})
	for _, item := range app.items {
		if item.kind == itemThinking {
			t.Fatalf("unexpected thinking block: %+v", item)
		}
	}
	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
}

func TestThinkingSettlesAcrossAnInterruptedAttempt(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "interrupt while thinking")

	deliver(app, agent.ModelStarted{Turn: 1, Attempt: 1})
	deliver(app, agent.ReasoningDelta{Text: "partial thought"})
	if app.run.thinking == nil || !app.run.thinking.thinking {
		t.Fatal("thinking block did not start streaming")
	}
	// The caller interrupts before any assistant text; the next attempt must
	// not append into the stale block.
	deliver(app, agent.ModelStarted{Turn: 1, Attempt: 1})
	if app.run.thinking != nil {
		t.Fatal("a new attempt kept the stale thinking pointer")
	}
	deliver(app, agent.ReasoningDelta{Text: "second thought"})
	var blocks []*transcriptItem
	for _, item := range app.items {
		if item.kind == itemThinking {
			blocks = append(blocks, item)
		}
	}
	if len(blocks) != 2 {
		t.Fatalf("thinking blocks = %d, want one per attempt", len(blocks))
	}
	if blocks[0].text != "partial thought" || blocks[0].thinking {
		t.Fatalf("stale block = %+v, want a settled first thought", blocks[0])
	}
	if blocks[1].text != "second thought" || !blocks[1].thinking {
		t.Fatalf("current block = %+v, want the in-progress second thought", blocks[1])
	}

	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
}

func TestThinkingItemsRespectTheTranscriptCap(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "many thoughts")
	// Pushing past the item cap keeps the display bounded and keeps one
	// omitted-history marker at the front.
	for i := range maxTranscriptItems + 50 {
		it := &transcriptItem{kind: itemThinking, run: 1, turn: 1, text: fmt.Sprintf("thought %d", i)}
		app.addItem(it)
	}
	if len(app.items) > maxTranscriptItems {
		t.Fatalf("items = %d, want at most %d", len(app.items), maxTranscriptItems)
	}
	if !app.hasOmittedMarker() {
		t.Fatal("older history marker missing after the cap was hit")
	}
	markers := 0
	for _, item := range app.items {
		if item.kind == itemNotice && strings.Contains(item.text, "older history omitted") {
			markers++
		}
	}
	if markers != 1 {
		t.Fatalf("omitted-history markers = %d, want 1", markers)
	}
	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
}

func TestTranscriptTextCapDropsOldestItems(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "many large thoughts")
	// Two oversized items exceed the text cap on their own; trimming must make
	// progress and leave a bounded transcript instead of spinning.
	big := strings.Repeat("x", maxTranscriptText/2+1)
	app.addItem(&transcriptItem{kind: itemThinking, text: big})
	app.addItem(&transcriptItem{kind: itemThinking, text: big})
	if app.textSize > maxTranscriptText {
		t.Fatalf("textSize = %d, want at most %d", app.textSize, maxTranscriptText)
	}
	if !app.hasOmittedMarker() {
		t.Fatal("older history marker missing after the text cap was hit")
	}
	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
}

func TestThinkingIsSanitizedAndCapped(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "hostile thinking")
	hostile := "a\x1b]52;c;x\x07b\u202e"
	deliver(app, agent.ReasoningDelta{Text: hostile})
	if len(app.items) == 0 {
		t.Fatal("thinking block was not added")
	}
	item := app.items[len(app.items)-1]
	if item.kind != itemThinking {
		t.Fatalf("item kind = %q", item.kind)
	}
	if strings.ContainsRune(item.text, '\x1b') || strings.ContainsRune(item.text, '\x07') {
		t.Fatalf("thinking text was not sanitized: %q", item.text)
	}
	thinkingLines := app.renderThinking(newTheme(true), item)
	plain := ansi.Strip(strings.Join(thinkingLines, "\n"))
	if strings.ContainsRune(plain, '\x1b') {
		t.Fatalf("rendered thinking line contains a raw ESC: %q", plain)
	}
	// Control bytes became visible glyphs instead of terminal commands.
	if !strings.Contains(plain, "␛]52;c;x␇b") {
		t.Fatalf("control bytes were not rendered as glyphs: %q", plain)
	}

	// A settled block over the display cap is truncated with a marker.
	deliver(app, agent.ResponseCompleted{
		Turn: 1, Text: "answer", Reasoning: strings.Repeat("t", maxThinkingItemText+64),
		FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 1, OutputTokens: 2},
	})
	item = app.items[len(app.items)-1]
	if item.kind != itemThinking && app.items[len(app.items)-2].kind == itemThinking {
		item = app.items[len(app.items)-2]
	}
	if !strings.Contains(item.text, "[truncated") {
		t.Fatalf("over-cap thinking was not marked: %d bytes", len(item.text))
	}
	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
}

// itemKinds lists the transcript item kinds for failure messages.
func itemKinds(items []*transcriptItem) []itemKind {
	kinds := make([]itemKind, len(items))
	for i, item := range items {
		kinds[i] = item.kind
	}
	return kinds
}

func TestRunLineRendersLegacyLimitStatuses(t *testing.T) {
	app := newTestApp(t, newManualHost())
	theme := newTheme(true)
	for _, status := range []agent.StopReason{agent.StopMaxTurns, agent.StopTimeLimit} {
		line := app.renderRunLine(theme, &transcriptItem{
			run:    1,
			status: status,
			reason: "legacy reason",
		})
		if !strings.Contains(line, string(status)) || !strings.Contains(line, "legacy reason") {
			t.Fatalf("run line = %q, want the legacy status and reason", line)
		}
	}
}

func TestToolIntentTitlesTheLiveBlock(t *testing.T) {
	app := newTestApp(t, newManualHost())
	startTestRun(t, app, "edit the parser")
	deliver(app, agent.ResponseCompleted{
		Turn: 1, FinishReason: model.FinishToolCalls,
		ToolCalls: []model.ToolCall{{
			ID:   "call-1",
			Name: "edit_file",
			Arguments: json.RawMessage(
				`{"path":"parser.go","old_string":"x","new_string":"y","intent":"Fix the parser"}`),
		}},
	})
	item := app.items[len(app.items)-1]
	if item.kind != itemTool {
		t.Fatalf("last item kind = %q, want tool", item.kind)
	}
	if item.intent != "Fix the parser" {
		t.Fatalf("tool intent = %q", item.intent)
	}
	header := plainRows(app.renderTool(newTheme(true), item))[0]
	if !strings.Contains(header, "Fix the parser") {
		t.Fatalf("tool header lacks the intent: %q", header)
	}
}

func TestToolIntentTitlesAReplayedBlock(t *testing.T) {
	host := newManualHost()
	host.history = []agent.HistoryItem{{
		Kind: agent.HistoryTool, Run: 1, Turn: 1, CallID: "call-1", Name: "read_file",
		Arguments: json.RawMessage(`{"path":"parser.go","intent":"Inspect the parser"}`),
	}}
	app := newTestApp(t, host)
	if len(app.items) != 1 {
		t.Fatalf("history items = %d, want 1", len(app.items))
	}
	if got := app.items[0].intent; got != "Inspect the parser" {
		t.Fatalf("replayed tool intent = %q", got)
	}
}

func finishRun(t *testing.T, app *App, result agent.Result) {
	t.Helper()
	cmd := updateApp(t, app, runDoneMsg{run: app.run.number, result: result})
	for range 10 {
		if cmd == nil {
			return
		}
		cmd = updateApp(t, app, cmd())
	}
	t.Fatal("run completion did not settle")
}

func keyMsg(t *testing.T, name string) tea.KeyPressMsg {
	t.Helper()
	name = strings.ReplaceAll(name, "+", "-")
	msg, err := protocolKey(name)
	if err != nil {
		t.Fatalf("protocolKey(%q): %v", name, err)
	}
	return msg
}

func press(t *testing.T, app *App, name string) tea.Cmd {
	t.Helper()
	return updateApp(t, app, keyMsg(t, name))
}

func deliver(app *App, ev agent.DisplayEvent) {
	_ = app.handleDisplay(ev)
}

func typeText(t *testing.T, app *App, text string) {
	t.Helper()
	updateApp(t, app, inputTextMsg{text: text})
}

func startTestRun(t *testing.T, app *App, text string) (*manualRun, startCall) {
	t.Helper()
	typeText(t, app, text)
	press(t, app, "enter")
	run, call, ok := app.host.(*manualHost).last()
	if !ok || !app.run.active || run == nil {
		t.Fatalf("run did not start: active=%v", app.run.active)
	}
	return run, call
}

func TestComposerEditingHistoryAndSubmit(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	typeText(t, app, "hello world")
	if got := app.composer.Value(); got != "hello world" {
		t.Fatalf("composer = %q", got)
	}
	press(t, app, "backspace")
	if got := app.composer.Value(); got != "hello worl" {
		t.Fatalf("after backspace = %q", got)
	}
	press(t, app, "ctrl+u")
	if got := app.composer.Value(); got != "" {
		t.Fatalf("after ctrl+u = %q", got)
	}
	typeText(t, app, "first message")
	press(t, app, "enter")
	if app.composer.Value() != "" {
		t.Fatalf("composer not cleared: %q", app.composer.Value())
	}
	if !app.run.active || len(app.items) != 2 {
		t.Fatalf("items = %d active = %v", len(app.items), app.run.active)
	}
	run, call, _ := host.last()
	if call.sub.Text != "first message" || call.sub.Source != "tui" {
		t.Fatalf("submission = %+v", call.sub)
	}
	// Complete the run, then recall the message from input history.
	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
	press(t, app, "up")
	if got := app.composer.Value(); got != "first message" {
		t.Fatalf("history recall = %q", got)
	}
	press(t, app, "down")
	if got := app.composer.Value(); got != "" {
		t.Fatalf("history down = %q", got)
	}
}

func TestSteeringAckAndRequeue(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, call := startTestRun(t, app, "first")
	typeText(t, app, "keep going")
	press(t, app, "enter")
	if got := <-call.steer; got != "keep going" {
		t.Fatalf("steer = %q", got)
	}
	if !app.steer[0].item.queued {
		t.Fatal("steering item not marked queued")
	}
	deliver(app, agent.MessageRecorded{Source: "steer", Text: "keep going"})
	if app.steer[0].item.queued || !app.steer[0].acked {
		t.Fatalf("steering not acknowledged: %+v", app.steer[0])
	}
	typeText(t, app, "lost line")
	press(t, app, "enter")
	<-call.steer
	deliver(app, agent.RunFinished{Result: agent.Result{Status: agent.StopInterrupted}})
	finishRun(t, app, agent.Result{Status: agent.StopInterrupted})
	if !strings.Contains(app.composer.Value(), "lost line") {
		t.Fatalf("unacknowledged steering was lost; composer = %q", app.composer.Value())
	}
	run.done <- agent.Result{Status: agent.StopInterrupted}
	close(run.done)
}

func TestInterruptKeys(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	typeText(t, app, "task")
	press(t, app, "enter")
	run, _, _ := host.last()

	press(t, app, "esc")
	if run.interrupt.Load() != 1 || run.terminate.Load() != 0 || !app.run.active {
		t.Fatalf("esc: interrupt=%d terminate=%d active=%v", run.interrupt.Load(), run.terminate.Load(), app.run.active)
	}
	// An Option-[ key must insert text, never act as Esc.
	updateApp(t, app, tea.KeyPressMsg{Code: '[', Mod: tea.ModAlt, Text: "["})
	if run.interrupt.Load() != 1 {
		t.Fatalf("Option-[ interrupted the run")
	}
	press(t, app, "ctrl+c")
	if run.interrupt.Load() != 2 || run.terminate.Load() != 0 {
		t.Fatalf("first ctrl-c: interrupt=%d terminate=%d", run.interrupt.Load(), run.terminate.Load())
	}
	press(t, app, "ctrl+c")
	if run.terminate.Load() != 1 || !app.run.active {
		t.Fatalf("second ctrl-c: terminate=%d active=%v", run.terminate.Load(), app.run.active)
	}
	press(t, app, "ctrl+d")
	if app.quitCode != 130 || !app.quitAfter {
		t.Fatalf("ctrl-d active: code=%d quitAfter=%v", app.quitCode, app.quitAfter)
	}
}

func TestIdleKeysDoNotQuitOnEscOrCtrlC(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	typeText(t, app, "draft")
	cmd := press(t, app, "esc")
	if cmd != nil || app.composer.Value() != "" {
		t.Fatalf("esc did not clear composer: %q", app.composer.Value())
	}
	if cmd := press(t, app, "ctrl+c"); cmd != nil {
		t.Fatal("ctrl-c idle returned a quit command")
	}
	if app.notice == "" {
		t.Fatal("ctrl-c idle did not show a quit hint")
	}
	if cmd := press(t, app, "ctrl+d"); cmd == nil {
		t.Fatal("ctrl-d idle did not quit")
	}
}

func TestModelPickerAppliesToNextRunOnly(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	press(t, app, "ctrl+p")
	if app.overlay.kind != OverlayModel {
		t.Fatalf("overlay = %q, want model", app.overlay.kind)
	}
	press(t, app, "down")
	press(t, app, "enter")
	if app.overlay.kind != OverlayNone || app.nextModel != "crouton-alpha-free" {
		t.Fatalf("picker result: overlay=%q next=%q", app.overlay.kind, app.nextModel)
	}
	typeText(t, app, "use the other model")
	press(t, app, "enter")
	_, call, ok := app.host.(*manualHost).last()
	if !ok || call.opts.Model != "crouton-alpha-free" {
		t.Fatalf("Start options = %+v", call.opts)
	}
	if app.info.Model != "deepseek-v4.1-flash" {
		t.Fatalf("effective model changed before run_start: %q", app.info.Model)
	}
	press(t, app, "ctrl+p")
	if app.overlay.kind != OverlayNone {
		t.Fatal("model picker opened during a run")
	}
}

func TestTableOverlayOpensAndCloses(t *testing.T) {
	host := newManualHost()
	host.history = []agent.HistoryItem{{
		Kind: agent.HistoryAssistant, Text: "| package | time |\n|---|---|\n| agent | 1.2s |\n",
	}}
	app := newTestApp(t, host)
	press(t, app, "ctrl+t")
	if app.overlay.kind != OverlayTable {
		t.Fatalf("overlay = %q, want table", app.overlay.kind)
	}
	press(t, app, "right")
	if app.overlay.table.xoff == 0 {
		t.Fatal("table horizontal scroll did not move")
	}
	press(t, app, "esc")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("overlay after esc = %q", app.overlay.kind)
	}
}

func TestRunFinishedUnackedSteeringReturnsOnce(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, call := startTestRun(t, app, "task")
	typeText(t, app, "unacked")
	press(t, app, "enter")
	<-call.steer
	deliver(app, agent.RunFinished{Result: agent.Result{Status: agent.StopInterrupted, CumulativeTurns: 1}})
	finishRun(t, app, agent.Result{Status: agent.StopInterrupted, CumulativeTurns: 1})
	if !strings.Contains(app.composer.Value(), "unacked") {
		t.Fatalf("composer = %q", app.composer.Value())
	}
	run.done <- agent.Result{Status: agent.StopInterrupted}
	close(run.done)
}
