package tui

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"aharness/internal/agent"
	"aharness/internal/model"
)

func plainRows(lines []string) []string {
	rows := make([]string, len(lines))
	for i, line := range lines {
		rows[i] = ansi.Strip(line)
	}
	return rows
}

func TestRenderFoldedShape(t *testing.T) {
	theme := newTheme(true)
	body := []string{"one", "two", "three", "four", "five"}

	collapsed := renderFolded(theme, "header", body, 3, true)
	want := []string{"header", "  … 2 earlier lines", "three", "four", "five"}
	if got := plainRows(collapsed); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("collapsed = %q, want %q", got, want)
	}

	expanded := renderFolded(theme, "header", body, 3, false)
	if got, want := len(expanded), len(body)+1; got != want {
		t.Fatalf("expanded rows = %d, want %d", got, want)
	}
	if collapsed[0] != expanded[0] {
		t.Fatalf("headers differ: %q vs %q", collapsed[0], expanded[0])
	}

	// A body that already fits the tail is complete and gets no elision row.
	short := renderFolded(theme, "header", body[:3], 3, true)
	if got := plainRows(short); strings.Join(got, "\n") != "header\none\ntwo\nthree" {
		t.Fatalf("short collapsed = %q", got)
	}
}

func TestThinkingCollapsesToTheTail(t *testing.T) {
	app := newTestApp(t, newManualHost())
	it := &transcriptItem{
		kind: itemThinking,
		text: "first\nsecond\nthird\nfourth\nconclusion",
	}
	collapsed := plainRows(app.renderThinking(newTheme(true), it))
	if collapsed[0] != "thinking" {
		t.Fatalf("header = %q", collapsed[0])
	}
	if !strings.Contains(collapsed[1], "2 earlier lines") {
		t.Fatalf("elision row = %q", collapsed[1])
	}
	if got, want := strings.Join(collapsed[2:], "|"), "  third|  fourth|  conclusion"; got != want {
		t.Fatalf("collapsed tail = %q, want %q", got, want)
	}
	if strings.Contains(strings.Join(collapsed, "\n"), "session log") {
		t.Fatal("thinking elision claims a session log")
	}

	app.expandAll = true
	expanded := plainRows(app.renderThinking(newTheme(true), it))
	if len(expanded) != 6 {
		t.Fatalf("expanded rows = %d, want 6: %q", len(expanded), expanded)
	}
	if expanded[0] != collapsed[0] {
		t.Fatalf("thinking header changed between states: %q vs %q", expanded[0], collapsed[0])
	}
	if !strings.Contains(strings.Join(expanded, "\n"), "first") {
		t.Fatalf("expanded body lacks the opening: %q", expanded)
	}
}

func TestThinkingFoldCountsWrappedRows(t *testing.T) {
	app := newTestApp(t, newManualHost())
	app.setSize(40, 24)
	it := &transcriptItem{kind: itemThinking, text: strings.Repeat("word ", 80)}

	collapsed := plainRows(app.renderThinking(newTheme(true), it))
	if got, want := len(collapsed), 1+1+foldedTailRows; got != want {
		t.Fatalf("collapsed rows = %d, want %d: %q", got, want, collapsed)
	}
	if !strings.Contains(collapsed[1], "earlier rows") && !strings.Contains(collapsed[1], "earlier lines") {
		t.Fatalf("elision row = %q", collapsed[1])
	}

	app.expandAll = true
	expanded := app.renderThinking(newTheme(true), it)
	if len(expanded) <= len(collapsed) {
		t.Fatalf("a wrapped line did not expand: collapsed=%d expanded=%d", len(collapsed), len(expanded))
	}

	// A single unbroken token is hard-wrapped too, so no row can exceed the
	// terminal width and the collapsed height still holds.
	app.expandAll = false
	long := &transcriptItem{kind: itemThinking, text: strings.Repeat("x", 200)}
	for i, row := range app.renderThinking(newTheme(true), long) {
		if width := ansi.StringWidth(row); width > app.width {
			t.Fatalf("row %d is %d cells wide, the terminal is %d: %q", i, width, app.width, ansi.Strip(row))
		}
	}
}

func TestCollapsedThinkingKeepsTheCapTextMarker(t *testing.T) {
	app := newTestApp(t, newManualHost())
	it := &transcriptItem{
		kind: itemThinking,
		text: CapText(strings.Repeat("reasoning ", 7_000), maxThinkingItemText),
	}
	rendered := plainRows(app.renderThinking(newTheme(true), it))
	if !strings.Contains(strings.Join(rendered, "\n"), "[truncated ") {
		t.Fatalf("collapsed thinking hid the CapText marker: %q", rendered)
	}
}

func TestToolCollapsesToTheTailAndKeepsItsHeader(t *testing.T) {
	app := newTestApp(t, newManualHost())
	var output strings.Builder
	for i := range 100 {
		fmt.Fprintf(&output, "line %d\n", i)
	}
	it := &transcriptItem{
		kind: itemTool, name: "run_command", args: "go test ./...",
		ok: false, code: "exit_1", truncated: true, timedOut: true,
		toolTime: 3 * time.Second,
		output:   output.String(),
	}

	collapsed := plainRows(app.renderTool(newTheme(true), it))
	header := collapsed[0]
	for _, want := range []string{"tool run_command", "go test ./...", "error exit_1", "truncated", "timeout"} {
		if !strings.Contains(header, want) {
			t.Fatalf("collapsed tool header lacks %q: %q", want, header)
		}
	}
	if !strings.Contains(collapsed[1], "97 earlier lines") {
		t.Fatalf("tool elision row = %q", collapsed[1])
	}
	if got, want := strings.Join(collapsed[2:], "|"), "  line 97|  line 98|  line 99"; got != want {
		t.Fatalf("collapsed tool tail = %q, want %q", got, want)
	}

	app.expandAll = true
	expanded := plainRows(app.renderTool(newTheme(true), it))
	if expanded[0] != header {
		t.Fatalf("tool header changed between states: %q vs %q", expanded[0], header)
	}
	if got := len(expanded); got != 1+toolBodyLines+1 {
		t.Fatalf("expanded tool rows = %d, want %d", got, 1+toolBodyLines+1)
	}
	if !strings.Contains(expanded[len(expanded)-1], "session log") {
		t.Fatalf("expanded tool lacks its overflow sentence: %q", expanded[len(expanded)-1])
	}
	if strings.Contains(strings.Join(expanded, "\n"), "line 99") {
		t.Fatal("expanded tool showed a row beyond the inline cap")
	}
}

func TestExpandedToolKeepsTheCapTextMarker(t *testing.T) {
	app := newTestApp(t, newManualHost())
	it := &transcriptItem{
		kind:   itemTool,
		name:   "run_command",
		ok:     true,
		output: CapText(strings.Repeat("x\n", 40_000), maxTranscriptItemText),
	}
	app.expandAll = true
	rendered := plainRows(app.renderTool(newTheme(true), it))
	text := strings.Join(rendered, "\n")
	if !strings.Contains(text, "[truncated ") {
		t.Fatalf("expanded tool hid the CapText marker:\n%s", text)
	}
	if !strings.Contains(text, "session log") {
		t.Fatalf("expanded tool lacks its overflow sentence:\n%s", text)
	}
}

func TestF3TogglesEveryBlockAndNotices(t *testing.T) {
	app := newTestApp(t, newManualHost())
	if app.expandAll {
		t.Fatal("blocks must start folded")
	}
	press(t, app, "f3")
	if !app.expandAll || app.notice != "expanded blocks" {
		t.Fatalf("after f3: expandAll=%v notice=%q", app.expandAll, app.notice)
	}
	press(t, app, "f3")
	if app.expandAll || app.notice != "collapsed blocks" {
		t.Fatalf("after second f3: expandAll=%v notice=%q", app.expandAll, app.notice)
	}
}

func TestF3IsInertWhileAnOverlayIsOpen(t *testing.T) {
	app := newTestApp(t, newManualHost())
	press(t, app, "f1")
	if app.overlay.kind != OverlayHelp {
		t.Fatalf("overlay = %q", app.overlay.kind)
	}
	help := ansi.Strip(strings.Join(app.overlay.help, "\n"))
	if !strings.Contains(help, "F3") || !strings.Contains(help, "expand/collapse") {
		t.Fatalf("help overlay does not list F3:\n%s", help)
	}
	press(t, app, "f3")
	if app.expandAll {
		t.Fatal("f3 toggled the folds through the help overlay")
	}
}

func TestFoldModeSticksAcrossTurnsAndRuns(t *testing.T) {
	app := newTestApp(t, newManualHost())
	press(t, app, "f3")
	run, _ := startTestRun(t, app, "first run")
	deliver(app, agent.ModelStarted{Turn: 1, Attempt: 1})
	deliver(app, agent.ReasoningDelta{Text: "thinking"})
	deliver(app, agent.TextDelta{Text: "answer"})
	deliver(app, agent.ResponseCompleted{
		Turn: 1, Text: "answer", Reasoning: "thinking", FinishReason: model.FinishStop,
		Usage: model.Usage{InputTokens: 1, OutputTokens: 1},
	})
	if !app.expandAll {
		t.Fatal("a response reset the fold mode")
	}
	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})

	startTestRun(t, app, "second run")
	if !app.expandAll {
		t.Fatal("a new run reset the fold mode")
	}
}

func TestNewThinkingBlockInheritsTheFoldMode(t *testing.T) {
	app := newTestApp(t, newManualHost())
	app.expandAll = true
	it := app.appendThinking(1, "one\ntwo\nthree\nfour\nfive")
	app.ensureContent()
	rendered := plainRows(it.lines)
	if len(rendered) != 6 {
		t.Fatalf("new block did not inherit expansion: %q", rendered)
	}
	app.expandAll = false
	app.invalidateFolds()
	app.ensureContent()
	rendered = plainRows(it.lines)
	if len(rendered) != 1+1+foldedTailRows {
		t.Fatalf("new block did not collapse: %q", rendered)
	}
}

func TestFoldRenderingIsDeterministic(t *testing.T) {
	app := newTestApp(t, newManualHost())
	app.addItem(&transcriptItem{kind: itemThinking, text: "alpha\nbeta\ngamma\ndelta"})
	app.addItem(&transcriptItem{kind: itemTool, name: "run_command", ok: true, output: "one\ntwo\nthree\nfour\nfive"})
	app.ensureContent()
	collapsed := strings.Join(app.transcriptLines(), "\n")

	press(t, app, "f3")
	expanded := strings.Join(app.transcriptLines(), "\n")
	if expanded == collapsed {
		t.Fatal("expanding produced identical rows")
	}
	press(t, app, "f3")
	if got := strings.Join(app.transcriptLines(), "\n"); got != collapsed {
		t.Fatalf("re-collapsed rows are not deterministic:\n%s\n---\n%s", got, collapsed)
	}
}

func TestProtocolKeyAcceptsF3(t *testing.T) {
	msg, err := protocolKey("f3")
	if err != nil {
		t.Fatalf("protocolKey(f3): %v", err)
	}
	if msg.Code != tea.KeyF3 {
		t.Fatalf("key code = %v", msg.Code)
	}
}

// reasoningHost emits a multi-line reasoning block so a headless frame can
// prove that the fold changes frame content without changing the protocol.
type reasoningHost struct{ *fakeHost }

func (h *reasoningHost) Start(_ Submission, _ RunOptions, _ <-chan string, display func(agent.DisplayEvent) error) (Run, error) {
	done := make(chan agent.Result, 1)
	run := &fakeRun{done: done}
	go func() {
		defer close(done)
		emit := func(ev agent.DisplayEvent) {
			if display != nil {
				_ = display(ev)
			}
		}
		var reasoning strings.Builder
		for i := range 10 {
			fmt.Fprintf(&reasoning, "thought %d\n", i)
		}
		emit(agent.RunStarted{Model: "deepseek-v4.1-flash", Dialect: "openai-chat"})
		emit(agent.ModelStarted{Turn: 1, Attempt: 1})
		emit(agent.ReasoningDelta{Text: reasoning.String()})
		result := agent.Result{
			Status: agent.StopCompleted, FinalText: "final answer",
			RunTurns: 1, RunUsage: model.Usage{InputTokens: 10, OutputTokens: 2},
			CumulativeTurns: 1, CumulativeUsage: model.Usage{InputTokens: 10, OutputTokens: 2},
		}
		emit(agent.ResponseCompleted{
			Turn: 1, Text: "final answer", Reasoning: reasoning.String(),
			FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 10, OutputTokens: 2},
		})
		emit(agent.RunFinished{Result: result, Duration: time.Millisecond})
		done <- result
	}()
	return run, nil
}

func TestHeadlessF3FoldsFrameRows(t *testing.T) {
	host := &reasoningHost{fakeHost: newFakeHost()}
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"think"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":5000}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"key","key":"f3"}`,
		`{"v":1,"type":"key","key":"f3"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: host, Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 80, Height: 30, Stdin: strings.NewReader(script), Stdout: &out,
		ColorProfile: "truecolor", ImageProtocol: "glyph",
	})
	if err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	var collapsedFrames, expandedFrames []string
	for _, line := range parseOutput(t, out.String()) {
		if line["type"] != "frame" {
			continue
		}
		raw, _ := line["lines"].([]any)
		frame := strings.Join(anyStrings(raw), "\n")
		switch {
		case strings.Contains(frame, "thought 0"):
			expandedFrames = append(expandedFrames, frame)
		case strings.Contains(frame, "earlier lines"):
			collapsedFrames = append(collapsedFrames, frame)
		}
	}
	if len(collapsedFrames) < 2 || len(expandedFrames) == 0 {
		t.Fatalf("collapsed frames = %d, expanded frames = %d\n%s",
			len(collapsedFrames), len(expandedFrames), out.String())
	}
	for _, collapsed := range collapsedFrames {
		if strings.Contains(collapsed, "thought 0") {
			t.Fatalf("collapsed frame contains folded reasoning:\n%s", collapsed)
		}
		if !strings.Contains(collapsed, "thought 9") {
			t.Fatalf("collapsed frame lacks the reasoning tail:\n%s", collapsed)
		}
	}
	if strings.Contains(expandedFrames[0], "earlier lines") {
		t.Fatalf("expanded frame is not expanded:\n%s", expandedFrames[0])
	}
}

func TestToggleKeepsFollowingTheTail(t *testing.T) {
	app := tallFoldApp(t, false)
	if !app.follow || app.viewport.YOffset() == 0 {
		t.Fatalf("test transcript does not follow: follow=%v offset=%d", app.follow, app.viewport.YOffset())
	}
	press(t, app, "f3")
	if !app.follow || !app.viewport.AtBottom() {
		t.Fatalf("expand detached the tail: follow=%v offset=%d", app.follow, app.viewport.YOffset())
	}
	press(t, app, "f3")
	if !app.follow || !app.viewport.AtBottom() {
		t.Fatalf("collapse detached the tail: follow=%v offset=%d", app.follow, app.viewport.YOffset())
	}
}

func TestTogglePreservesTheAnchoredItemWhenDetached(t *testing.T) {
	app := tallFoldApp(t, false)
	app.viewport.ScrollUp(20)
	app.follow = false
	anchor, delta := itemAtRow(app, app.viewport.YOffset())
	if anchor == nil {
		t.Fatal("no anchor item")
	}

	press(t, app, "f3")
	got, gotDelta := itemAtRow(app, app.viewport.YOffset())
	if got != anchor {
		t.Fatalf("expand moved to a different item")
	}
	if gotDelta != delta {
		t.Fatalf("expand intra-item offset = %d, want %d", gotDelta, delta)
	}

	press(t, app, "f3")
	got, gotDelta = itemAtRow(app, app.viewport.YOffset())
	if got != anchor {
		t.Fatalf("collapse moved to a different item")
	}
	if gotDelta != delta {
		t.Fatalf("collapse intra-item offset = %d, want %d", gotDelta, delta)
	}
}

func TestCollapseClampsToTheAnchoredItemsLastRow(t *testing.T) {
	app := newTestApp(t, newManualHost())
	app.expandAll = true
	app.addItem(&transcriptItem{kind: itemThinking, text: strings.Repeat("line\n", 200)})
	for i := range 30 {
		app.addItem(&transcriptItem{kind: itemNotice, text: fmt.Sprintf("later notice %d", i)})
	}
	app.ensureContent()
	if app.viewport.YOffset() == 0 {
		t.Fatal("test transcript is not scrollable")
	}
	app.viewport.offset = 100
	app.follow = false

	press(t, app, "f3")
	if app.follow {
		t.Fatal("collapse unexpectedly re-attached the tail")
	}
	start := app.rowOf[0]
	end := app.rowOf[1]
	last := end - 1
	if app.viewport.YOffset() > last || app.viewport.YOffset()+app.viewport.Height() <= last {
		t.Fatalf("item's last row is not visible: offset=%d last=%d height=%d start=%d",
			app.viewport.YOffset(), last, app.viewport.Height(), start)
	}
}

func TestCollapseThatLandsAtTheBottomReattachesTheTail(t *testing.T) {
	app := newTestApp(t, newManualHost())
	app.expandAll = true
	app.addItem(&transcriptItem{kind: itemThinking, text: strings.Repeat("line\n", 200)})
	app.ensureContent()
	app.viewport.offset = 100
	app.follow = false

	press(t, app, "f3")
	if !app.follow || !app.viewport.AtBottom() {
		t.Fatalf("collapse at the bottom did not re-attach: follow=%v offset=%d", app.follow, app.viewport.YOffset())
	}
}

func TestCollapseDoesNotReattachWhenContentStillFits(t *testing.T) {
	app := newTestApp(t, newManualHost())
	app.expandAll = true
	app.addItem(&transcriptItem{kind: itemThinking, text: "one\ntwo\nthree"})
	app.ensureContent()
	app.follow = false
	app.viewport.offset = 0

	press(t, app, "f3")
	if app.follow {
		t.Fatalf("a fitting transcript re-attached on collapse: offset=%d", app.viewport.YOffset())
	}
	if app.viewport.YOffset() != 0 {
		t.Fatalf("offset = %d, want 0", app.viewport.YOffset())
	}
}

func TestTrimmedAnchorFallsBackToTheBottom(t *testing.T) {
	app := tallFoldApp(t, false)
	app.viewport.ScrollUp(40)
	app.follow = false
	item, delta, ok := app.captureAnchor()
	if !ok {
		t.Fatal("no anchor captured")
	}
	app.removeItem(item)
	app.invalidateFolds()
	app.ensureContent()
	app.restoreAnchor(item, delta, true)
	if !app.viewport.AtBottom() {
		t.Fatalf("trimmed anchor did not fall back to the bottom: offset=%d", app.viewport.YOffset())
	}
}

// tallFoldApp builds a transcript of enough collapsed thinking blocks to
// scroll. expandAll chooses the starting fold state.
func tallFoldApp(t *testing.T, expanded bool) *App {
	t.Helper()
	app := newTestApp(t, newManualHost())
	app.expandAll = expanded
	for i := range 8 {
		var text strings.Builder
		for j := range 12 {
			fmt.Fprintf(&text, "block %d line %d\n", i, j)
		}
		app.addItem(&transcriptItem{kind: itemThinking, text: text.String()})
	}
	app.ensureContent()
	return app
}

func itemAtRow(app *App, row int) (*transcriptItem, int) {
	if len(app.items) == 0 || len(app.rowOf) != len(app.items) {
		return nil, 0
	}
	index := sort.Search(len(app.rowOf), func(i int) bool { return app.rowOf[i] > row }) - 1
	if index < 0 {
		index = 0
	}
	return app.items[index], max(0, row-app.rowOf[index])
}
