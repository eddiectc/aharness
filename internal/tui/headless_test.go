package tui

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/tools"
)

type fakeHost struct {
	info    Info
	history []agent.HistoryItem
	ws      *tools.Workspace

	mu       sync.Mutex
	run      *fakeRun
	last     []byte
	lastOpts RunOptions
}

type fakeRun struct {
	done chan agent.Result
}

func (r *fakeRun) Interrupt()                {}
func (r *fakeRun) Terminate()                {}
func (r *fakeRun) Done() <-chan agent.Result { return r.done }

func (h *fakeHost) Info() Info { return h.info }
func (h *fakeHost) Models() []ModelChoice {
	return []ModelChoice{
		{
			ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash",
			Dialect: model.DialectChat, ContextWindow: 200_000, InputLimit: 160_000,
			OutputLimit: 8_192, Switchable: true, ReasoningEfforts: []string{"low", "high", "max"},
		},
		{
			ID: "crouton-alpha-free", Name: "Crouton Alpha Free",
			Dialect: model.DialectResponses, ContextWindow: 1_000_000, InputLimit: 900_000,
			Status: "alpha", Translated: true, Switchable: true,
		},
	}
}
func (h *fakeHost) History() ([]agent.HistoryItem, error) { return h.history, nil }
func (h *fakeHost) Workspace() *tools.Workspace           { return h.ws }
func (h *fakeHost) Close() error                          { return nil }

func (h *fakeHost) Start(sub Submission, opts RunOptions, steer <-chan string, display func(agent.DisplayEvent) error) (Run, error) {
	h.mu.Lock()
	h.last = []byte(sub.Text)
	h.lastOpts = opts
	h.mu.Unlock()
	done := make(chan agent.Result, 1)
	modelName := opts.Model
	if modelName == "" {
		modelName = "deepseek-v4.1-flash"
	}
	go func() {
		defer close(done)
		emit := func(ev agent.DisplayEvent) {
			if display != nil {
				_ = display(ev)
			}
		}
		emit(agent.RunStarted{Turn: 0, Model: modelName, Dialect: "openai-chat", ThinkingEffort: opts.ThinkingEffort})
		emit(agent.ModelStarted{Turn: 1, Attempt: 1})
		emit(agent.TextDelta{Text: "hello "})
		emit(agent.TextDelta{Text: "world"})
		emit(agent.ResponseCompleted{
			Turn: 1, Text: "hello world", FinishReason: model.FinishStop,
			Usage: model.Usage{InputTokens: 10, OutputTokens: 2},
		})
		emit(agent.MessageRecorded{Turn: 1, Source: "steer", Text: "more"})
		emit(agent.Notice{Turn: 1, Kind: agent.NoticeRetry, Text: "retrying"})
		result := agent.Result{
			Status: agent.StopCompleted, FinalText: "hello world",
			RunTurns: 1, RunUsage: model.Usage{InputTokens: 10, OutputTokens: 2},
			CumulativeTurns: 1, CumulativeUsage: model.Usage{InputTokens: 10, OutputTokens: 2},
			ContextTokens: 12,
		}
		emit(agent.RunFinished{Result: result, Reason: "model completed the task", Duration: time.Millisecond})
		done <- result
	}()
	h.mu.Lock()
	h.run = &fakeRun{done: done}
	h.mu.Unlock()
	return h.run, nil
}

func newFakeHost() *fakeHost {
	return &fakeHost{info: Info{
		SessionID: "0190a0c4-0000-7000-8000-0000000000aa",
		TracePath: "/tmp/fake/trace.jsonl",
		Model:     "deepseek-v4.1-flash",
		Dialect:   string(model.DialectChat),
		Workspace: "/tmp/fake",
		MaxTurns:  30, MaxTotalTokens: 2_000_000,
		ContextLimit: 200_000,
	}}
}

func (h *fakeHost) lastRunOptions() RunOptions {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastOpts
}

func parseOutput(t *testing.T, out string) []map[string]any {
	t.Helper()
	var result []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := jsonv2.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode output line %q: %v", line, err)
		}
		result = append(result, m)
	}
	return result
}

func TestHeadlessScriptedSession(t *testing.T) {
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"Fix the failing test"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":5000}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"snapshot","format":"svg"}`,
		`{"v":1,"type":"snapshot","format":"svg"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: newFakeHost(), Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 80, Height: 24,
		Stdin: strings.NewReader(script), Stdout: &out,
		ColorProfile: "truecolor", ImageProtocol: "glyph",
	})
	if err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	lines := parseOutput(t, out.String())
	if len(lines) < 8 {
		t.Fatalf("got %d output lines, want at least 8:\n%s", len(lines), out.String())
	}
	if got := lines[0]["type"]; got != "frame" {
		t.Fatalf("first line type = %v, want frame", got)
	}
	if got := lines[0]["state"]; got != "idle" {
		t.Fatalf("first frame state = %v, want idle", got)
	}
	frameLines, _ := lines[0]["lines"].([]any)
	if len(frameLines) != 24 {
		t.Fatalf("first frame lines = %d, want 24", len(frameLines))
	}
	cursor, _ := lines[0]["cursor"].(map[string]any)
	if cursor["visible"] != true || cursor["shape"] != "block" {
		t.Fatalf("first frame cursor = %v", cursor)
	}
	var events []string
	var snaps []string
	var lastFrame map[string]any
	for _, line := range lines {
		switch line["type"] {
		case "event":
			events = append(events, fmt.Sprint(line["event"]))
		case "snapshot":
			snaps = append(snaps, fmt.Sprint(line["svg"]))
		case "frame":
			lastFrame = line
		}
	}
	if strings.Join(events, ",") != "run_started,notice,run_finished" {
		t.Fatalf("events = %v\n%s", events, out.String())
	}
	if len(snaps) != 2 || snaps[0] != snaps[1] {
		t.Fatalf("snapshots are not deterministic (%d)", len(snaps))
	}
	if !strings.Contains(snaps[0], "<svg") {
		t.Fatalf("snapshot missing svg: %q", snaps[0])
	}
	if lastFrame == nil {
		t.Fatal("no final frame")
	}
	frameText, _ := lastFrame["lines"].([]any)
	text := strings.Join(anyStrings(frameText), "\n")
	if !strings.Contains(text, "hello world") {
		t.Fatalf("final frame does not contain assistant text:\n%s", text)
	}
	// The status line reports context occupancy, not a cumulative token sum.
	statusLine := text[strings.LastIndex(text, "\n")+1:]
	if !strings.Contains(statusLine, "ctx 12/200.0k") {
		t.Fatalf("status line lacks the context occupancy:\n%s", statusLine)
	}
	if strings.Contains(statusLine, "tokens ") {
		t.Fatalf("status line still shows a cumulative token sum:\n%s", statusLine)
	}
	t.Logf("final frame:\n%s", text)
	if strings.ContainsRune(text, '\x1b') {
		t.Fatal("frame contains ESC")
	}
}

func anyStrings(values []any) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i], _ = value.(string)
	}
	return out
}

type headlessSelectingHost struct {
	*fakeHost
	choices []ModelChoice
}

func (h *headlessSelectingHost) Models() []ModelChoice {
	return append([]ModelChoice(nil), h.choices...)
}

func (h *headlessSelectingHost) SelectModel(id string) (ModelChoice, error) {
	for _, choice := range h.choices {
		if choice.ID == id {
			return choice, nil
		}
	}
	return ModelChoice{}, fmt.Errorf("model %q is not in the loaded catalog", id)
}

func TestHeadlessEffortPickerSelectsNextRun(t *testing.T) {
	host := newFakeHost()
	script := strings.Join([]string{
		`{"v":1,"type":"key","key":"ctrl-p"}`,
		`{"v":1,"type":"key","key":"esc"}`,
		`{"v":1,"type":"key","key":"f2"}`,
		`{"v":1,"type":"key","key":"down"}`,
		`{"v":1,"type":"key","key":"down"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"text","text":"use the selected effort"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":5000}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: host, Models: host.Models(), Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 120, Height: 40, Stdin: strings.NewReader(script), Stdout: &out,
		ImageProtocol: "glyph",
	})
	if err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	raw := out.String()
	for _, want := range []string{
		`"overlay":"model"`, "effort low/high/max",
		`"overlay":"effort"`, "provider default",
		"thinking effort for the next run: high",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("output missing %q:\n%s", want, raw)
		}
	}
	opts := host.lastRunOptions()
	if opts.ThinkingEffort != "high" || !opts.ThinkingEffortSet {
		t.Fatalf("Start options = %+v, want high with the set flag", opts)
	}
	var runStarted, effortNotice bool
	for _, line := range parseOutput(t, raw) {
		if line["type"] != "event" {
			continue
		}
		switch line["event"] {
		case "run_started":
			runStarted = true
			if line["thinking_effort"] != "high" {
				t.Fatalf("run_started = %v, want thinking_effort high", line)
			}
		case "notice":
			if line["kind"] == "thinking_effort" {
				effortNotice = true
				if !strings.Contains(fmt.Sprint(line["text"]), "high") {
					t.Fatalf("thinking_effort notice = %v", line)
				}
			}
		}
	}
	if !runStarted || !effortNotice {
		t.Fatalf("missing run_started or thinking_effort notice:\n%s", raw)
	}
	lastFrame := ""
	for _, line := range parseOutput(t, raw) {
		if line["type"] != "frame" {
			continue
		}
		frameLines, _ := line["lines"].([]any)
		lastFrame = strings.Join(anyStrings(frameLines), "\n")
	}
	if !strings.Contains(lastFrame, "effort high") {
		t.Fatalf("TUI header does not show the effective effort:\n%s", lastFrame)
	}
}

func TestHeadlessEffortPickerProviderDefaultOmitsField(t *testing.T) {
	host := newFakeHost()
	script := strings.Join([]string{
		`{"v":1,"type":"key","key":"f2"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"text","text":"run with the provider default"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":5000}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: host, Models: host.Models(), Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 120, Height: 40, Stdin: strings.NewReader(script), Stdout: &out,
		ImageProtocol: "glyph",
	})
	if err != nil || exit != 0 {
		t.Fatalf("RunHeadless exit=%d err=%v", exit, err)
	}
	opts := host.lastRunOptions()
	if opts.ThinkingEffort != "" || !opts.ThinkingEffortSet {
		t.Fatalf("Start options = %+v, want an explicit provider default", opts)
	}
	raw := out.String()
	var runStarted bool
	for _, line := range parseOutput(t, raw) {
		if line["type"] != "event" {
			continue
		}
		if line["event"] == "run_started" {
			runStarted = true
			if _, ok := line["thinking_effort"]; ok {
				t.Fatalf("provider-default run_started exposed the field: %v", line)
			}
		}
	}
	if !runStarted {
		t.Fatalf("missing run_started event:\n%s", raw)
	}
	if !strings.Contains(raw, "thinking effort for the next run: provider default") {
		t.Fatalf("missing provider default notice:\n%s", raw)
	}
	if !strings.Contains(raw, "effort default") {
		t.Fatalf("TUI header does not show the provider default:\n%s", raw)
	}
}

func TestHeadlessEffortPickerNoValues(t *testing.T) {
	models := []ModelChoice{{
		ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", Dialect: model.DialectChat,
		ContextWindow: 200_000, InputLimit: 160_000, OutputLimit: 8_192, Switchable: true,
	}}
	host := newFakeHost()
	script := strings.Join([]string{
		`{"v":1,"type":"key","key":"f2"}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: host, Models: models, Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 120, Height: 40, Stdin: strings.NewReader(script), Stdout: &out,
		ImageProtocol: "glyph",
	})
	if err != nil || exit != 0 {
		t.Fatalf("RunHeadless exit=%d err=%v", exit, err)
	}
	raw := out.String()
	if !strings.Contains(raw, "advertises no effort levels") {
		t.Fatalf("missing no-effort notice:\n%s", raw)
	}
	if strings.Contains(raw, `"overlay":"effort"`) {
		t.Fatalf("effort overlay opened without advertised values:\n%s", raw)
	}
	if strings.Contains(raw, `"event":"run_started"`) {
		t.Fatalf("a run started unexpectedly:\n%s", raw)
	}
}

func TestHeadlessModelSwitchClearsUnsupportedEffort(t *testing.T) {
	base := newFakeHost()
	base.info.ThinkingEffort = "high"
	host := &headlessSelectingHost{
		fakeHost: base,
		choices: []ModelChoice{
			{
				ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", Dialect: model.DialectChat,
				ContextWindow: 200_000, InputLimit: 160_000, OutputLimit: 8_192,
				Switchable: true, ReasoningEfforts: []string{"low", "high", "max"},
			},
			{
				ID: "crouton-alpha-free", Name: "Crouton Alpha Free", Dialect: model.DialectResponses,
				ContextWindow: 1_000_000, InputLimit: 900_000, Switchable: true,
			},
		},
	}
	script := strings.Join([]string{
		`{"v":1,"type":"key","key":"ctrl-p"}`,
		`{"v":1,"type":"text","text":"crouton"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"text","text":"run after the switch"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":5000}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: host, Models: host.Models(), Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 120, Height: 40, Stdin: strings.NewReader(script), Stdout: &out,
		ImageProtocol: "glyph",
	})
	if err != nil || exit != 0 {
		t.Fatalf("RunHeadless exit=%d err=%v", exit, err)
	}
	opts := host.lastRunOptions()
	if opts.Model != "crouton-alpha-free" || opts.ThinkingEffort != "" || !opts.ThinkingEffortSet {
		t.Fatalf("Start options = %+v, want the switched model with the cleared effort", opts)
	}
	raw := out.String()
	var warning, changed bool
	for _, line := range parseOutput(t, raw) {
		if line["type"] != "event" {
			continue
		}
		switch line["event"] {
		case "notice":
			if line["kind"] == "warning" && strings.Contains(fmt.Sprint(line["text"]), `cleared`) {
				warning = true
			}
		case "model_changed":
			changed = true
			if line["model"] != "crouton-alpha-free" {
				t.Fatalf("model_changed = %v", line)
			}
			if _, ok := line["thinking_effort"]; ok {
				t.Fatalf("cleared model_changed exposed the field: %v", line)
			}
		case "run_started":
			if _, ok := line["thinking_effort"]; ok {
				t.Fatalf("cleared run_started exposed the field: %v", line)
			}
		}
	}
	if !warning || !changed {
		t.Fatalf("missing clearing warning or model_changed event:\n%s", raw)
	}
}

func TestHeadlessProtocolError(t *testing.T) {
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: newFakeHost(), Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 40, Height: 10,
		Stdin: strings.NewReader(`{"v":1,"type":"nope"}` + "\n"), Stdout: &out,
		ImageProtocol: "glyph",
	})
	if err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if exit != 2 {
		t.Fatalf("exit = %d, want 2", exit)
	}
	lines := parseOutput(t, out.String())
	if len(lines) == 0 || lines[len(lines)-1]["type"] != "error" {
		t.Fatalf("missing protocol error: %s", out.String())
	}
	if lines[len(lines)-1]["code"] != "protocol_error" {
		t.Fatalf("error code = %v", lines[len(lines)-1]["code"])
	}
}

func TestHeadlessResizeAndOverlayKeys(t *testing.T) {
	script := strings.Join([]string{
		`{"v":1,"type":"resize","width":100,"height":30}`,
		`{"v":1,"type":"key","key":"f1"}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"key","key":"esc"}`,
		`{"v":1,"type":"resize","width":200,"height":60}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: newFakeHost(), Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 80, Height: 24, Stdin: strings.NewReader(script), Stdout: &out,
		ImageProtocol: "glyph",
	})
	if err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	lines := parseOutput(t, out.String())
	var helpFrame map[string]any
	for _, line := range lines {
		if line["type"] == "frame" && line["overlay"] == "help" {
			helpFrame = line
		}
	}
	if helpFrame == nil {
		t.Fatalf("no help overlay frame:\n%s", out.String())
	}
	if helpFrame["width"] != float64(100) || helpFrame["height"] != float64(30) {
		t.Fatalf("resized frame = %vx%v", helpFrame["width"], helpFrame["height"])
	}
	var wide map[string]any
	for _, line := range lines {
		if line["type"] == "frame" && line["width"] == float64(200) {
			wide = line
		}
	}
	if wide == nil || wide["height"] != float64(60) {
		t.Fatalf("200x60 frame missing: %v", wide)
	}
	if got := len(wide["lines"].([]any)); got != 60 {
		t.Fatalf("200x60 lines = %d", got)
	}
}

func TestHeadlessImageMetadataAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := writePNGFile(dir, "chart.png", 12, 8); err != nil {
		t.Fatal(err)
	}
	ws, err := tools.OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	host := newFakeHost()
	host.ws = ws
	host.info.Workspace = dir
	host.history = []agent.HistoryItem{{
		Kind: agent.HistoryAssistant, Text: "![timing](chart.png)",
	}}
	script := strings.Join([]string{
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"snapshot","format":"svg"}`,
		`{"v":1,"type":"key","key":"ctrl-g"}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: host, Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 80, Height: 24, Stdin: strings.NewReader(script), Stdout: &out,
		ImageProtocol: "glyph",
	})
	if err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	lines := parseOutput(t, out.String())
	var imageFrame, viewerFrame map[string]any
	var svg string
	for _, line := range lines {
		switch line["type"] {
		case "snapshot":
			svg, _ = line["svg"].(string)
		case "frame":
			if line["overlay"] == "image" {
				viewerFrame = line
			} else {
				imageFrame = line
			}
		}
	}
	if imageFrame == nil {
		t.Fatal("no transcript frame")
	}
	images, _ := imageFrame["images"].([]any)
	if len(images) != 1 {
		t.Fatalf("images metadata = %v", imageFrame["images"])
	}
	meta := images[0].(map[string]any)
	if meta["path"] != "chart.png" || meta["protocol"] != "glyph" || meta["width"].(float64) <= 0 {
		t.Fatalf("image metadata = %v", meta)
	}
	frameText, _ := imageFrame["lines"].([]any)
	text := strings.Join(anyStrings(frameText), "\n")
	if !strings.Contains(text, "▄") {
		t.Fatalf("glyph thumbnail missing:\n%s", text)
	}
	if !strings.Contains(svg, "▄") {
		t.Fatal("snapshot does not contain the glyph preview")
	}
	if viewerFrame == nil {
		t.Fatal("Ctrl-G did not open the image viewer")
	}
}

func writePNGFile(dir, name string, width, height int) error {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 128, A: 255})
		}
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img)
}

func TestHeadlessDeterministicFinalFrameAndSnapshot(t *testing.T) {
	run := func() (map[string]any, string) {
		script := strings.Join([]string{
			`{"v":1,"type":"text","text":"Fix the failing test"}`,
			`{"v":1,"type":"key","key":"enter"}`,
			`{"v":1,"type":"wait","until":"idle","timeout_ms":5000}`,
			`{"v":1,"type":"frame"}`,
			`{"v":1,"type":"snapshot","format":"svg"}`,
			`{"v":1,"type":"quit"}`,
		}, "\n") + "\n"
		var out bytes.Buffer
		exit, err := RunHeadless(HeadlessOptions{
			Host: newFakeHost(), Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
			Width: 80, Height: 24, Stdin: strings.NewReader(script), Stdout: &out,
			ColorProfile: "truecolor", ImageProtocol: "glyph",
		})
		if err != nil || exit != 0 {
			t.Fatalf("RunHeadless exit=%d err=%v", exit, err)
		}
		lines := parseOutput(t, out.String())
		var lastFrame map[string]any
		var svg string
		for _, line := range lines {
			if line["type"] == "frame" {
				lastFrame = line
			}
			if line["type"] == "snapshot" {
				svg, _ = line["svg"].(string)
			}
		}
		if lastFrame == nil || svg == "" {
			t.Fatal("missing final frame or snapshot")
		}
		return lastFrame, svg
	}
	frame1, svg1 := run()
	frame2, svg2 := run()
	if svg1 != svg2 {
		i := 0
		for i < len(svg1) && i < len(svg2) && svg1[i] == svg2[i] {
			i++
		}
		start := max(0, i-120)
		end := min(len(svg1), i+240)
		t.Fatalf("document-identical sessions produced different snapshots at %d:\n%s\n---\n%s", i, svg1[start:end], svg2[start:min(len(svg2), i+240)])
	}
	if !reflect.DeepEqual(frame1["lines"], frame2["lines"]) {
		t.Fatalf("final lines differ")
	}
	if !reflect.DeepEqual(frame1["styles"], frame2["styles"]) {
		t.Fatalf("final styles differ")
	}
	if !reflect.DeepEqual(frame1["cursor"], frame2["cursor"]) {
		t.Fatalf("final cursor differs")
	}
}

func TestHeadlessNoColorStripsColorStyles(t *testing.T) {
	var out bytes.Buffer
	script := `{"v":1,"type":"quit"}` + "\n"
	_, err := RunHeadless(HeadlessOptions{
		Host: newFakeHost(), Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 60, Height: 12, Stdin: strings.NewReader(script), Stdout: &out,
		NoColor: true, ImageProtocol: "glyph",
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := parseOutput(t, out.String())
	if len(lines) == 0 || lines[0]["no_color"] != true {
		t.Fatalf("frame = %v", lines)
	}
	styles, _ := lines[0]["styles"].([]any)
	for _, raw := range styles {
		span := raw.(map[string]any)
		if span["fg"] != "default" || span["bg"] != "default" {
			t.Fatalf("color survived NO_COLOR: %v", span)
		}
	}
}

func TestHeadlessUntrustedTextAndUnicode(t *testing.T) {
	host := newFakeHost()
	host.history = []agent.HistoryItem{{
		Kind: agent.HistoryAssistant,
		Text: "汉字 café e\u0301 👩‍👩‍👧‍👦\n\n[clip](https://example.com)\n\n\x1b]52;c;SGVsbG8=\x07",
	}}
	script := strings.Join([]string{
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: host, Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Width: 100, Height: 30, Stdin: strings.NewReader(script), Stdout: &out,
		ImageProtocol: "glyph",
	})
	if err != nil || exit != 0 {
		t.Fatalf("exit=%d err=%v", exit, err)
	}
	if strings.ContainsRune(out.String(), 0x1b) {
		t.Fatal("protocol output contains raw ESC")
	}
	if strings.Contains(out.String(), "\\u001b]52") || strings.Contains(out.String(), "\x1b]52") {
		t.Fatal("OSC 52 passed through")
	}
	lines := parseOutput(t, out.String())
	frameText, _ := lines[0]["lines"].([]any)
	text := strings.Join(anyStrings(frameText), "\n")
	for _, want := range []string{"汉字", "café", "👩‍👩‍👧‍👦", "␛]52"} {
		if !strings.Contains(text, want) {
			t.Fatalf("frame missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "[clip](https://example.com)") {
		t.Fatalf("markdown source was not rendered:\n%s", text)
	}
}

// rosterHost starts a run that stays active until the test releases it, and
// reports two delegated children mid-run.
type rosterHost struct {
	*fakeHost
	release  chan struct{}
	finished chan struct{}
	once     sync.Once
}

func newRosterHost() *rosterHost {
	return &rosterHost{fakeHost: newFakeHost(), release: make(chan struct{}), finished: make(chan struct{})}
}

// releaseRun ends the fake child run when the UI is asked to quit.
func (h *rosterHost) releaseRun() { h.once.Do(func() { close(h.release) }) }

// rosterRun models an active run that ends when it is terminated.
type rosterRun struct {
	done    chan agent.Result
	release func()
}

func (r *rosterRun) Interrupt()                {}
func (r *rosterRun) Terminate()                { r.release() }
func (r *rosterRun) Done() <-chan agent.Result { return r.done }

func (h *rosterHost) Start(sub Submission, opts RunOptions, steer <-chan string, display func(agent.DisplayEvent) error) (Run, error) {
	done := make(chan agent.Result, 1)
	emit := func(ev agent.DisplayEvent) {
		if display != nil {
			_ = display(ev)
		}
	}
	go func() {
		defer close(h.finished)
		emit(agent.RunStarted{Turn: 0, Model: "deepseek-v4.1-flash", Dialect: "openai-chat", ThinkingEffort: "high"})
		emit(agent.SubagentStarted{
			Turn: 1, CallID: "call-1", Index: 0, Agent: "explore",
			Intent: "find the projection", Session: "child-1",
			Model: "deepseek-v4.1-flash", Effort: "high", Tools: []string{"read_file", "search"},
		})
		emit(agent.SubagentStarted{
			Turn: 1, CallID: "call-1", Index: 1, Agent: "reviewer",
			Intent: "review the change", Session: "child-2",
			Model: "deepseek-v4.1-flash", Effort: "high", Tools: []string{"read_file", "search"},
		})
		emit(agent.SubagentProgress{
			Turn: 1, CallID: "call-1", Index: 0, Agent: "explore", Session: "child-1",
			ChildTurns: 3, ContextTokens: 18240,
			Usage:        model.Usage{InputTokens: 41230, OutputTokens: 3110},
			CostMicroUSD: 9210, CostComplete: true, LastTool: "read_file",
		})
		<-h.release
		result := agent.Result{Status: agent.StopCompleted, RunTurns: 1, CumulativeTurns: 1}
		emit(agent.RunFinished{Result: result, Reason: "model completed the task", Duration: time.Millisecond})
		done <- result
	}()
	return &rosterRun{done: done, release: h.releaseRun}, nil
}

func (h *rosterHost) Close() error {
	h.releaseRun()
	<-h.finished
	return nil
}

func TestHeadlessAgentHubRoster(t *testing.T) {
	host := newRosterHost()
	script := strings.Join([]string{
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"text","text":"/agent-hub"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: host, Models: host.Models(), Model: "deepseek-v4.1-flash", Dialect: model.DialectChat,
		Seed:   &Submission{Text: "delegate the work", Source: "-task"},
		Width:  120,
		Height: 30,
		Stdin:  strings.NewReader(script), Stdout: &out,
		ImageProtocol: "glyph",
	})
	if err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	found := false
	for _, line := range parseOutput(t, out.String()) {
		if line["type"] != "frame" || line["overlay"] != string(OverlayAgentHub) {
			continue
		}
		rows, _ := line["lines"].([]any)
		var frame strings.Builder
		for _, row := range rows {
			fmt.Fprintf(&frame, "%v\n", row)
		}
		body := frame.String()
		if strings.Contains(body, "explore") && strings.Contains(body, "find the projec") &&
			strings.Contains(body, "reviewer") && strings.Contains(body, "deepseek-v4.1-f") &&
			strings.Contains(body, "high") && strings.Contains(body, "running") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no agent-hub frame listed both children:\n%s", out.String())
	}
}
