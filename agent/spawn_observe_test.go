package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"unicode/utf8"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// eventOfType returns the first event of typ from a replayed log.
func eventOfType(t *testing.T, events []session.Event, typ session.EventType) session.Event {
	t.Helper()
	for _, ev := range events {
		if ev.Type == typ {
			return ev
		}
	}
	t.Fatalf("no %s event in the replayed log", typ)
	return session.Event{}
}

// displayKind names one display event for ordering assertions.
func displayKind(ev DisplayEvent) string {
	switch ev.(type) {
	case RunStarted:
		return "run_started"
	case ModelStarted:
		return "model_started"
	case TextDelta:
		return "text_delta"
	case ReasoningDelta:
		return "reasoning_delta"
	case ResponseCompleted:
		return "response_completed"
	case ToolStarted:
		return "tool_started"
	case ToolCompleted:
		return "tool_completed"
	case MessageRecorded:
		return "message_recorded"
	case SubagentMessageRecorded:
		return "subagent_message_recorded"
	case Notice:
		return "notice"
	case SubagentStarted:
		return "subagent_started"
	case SubagentProgress:
		return "subagent_progress"
	case SubagentFinished:
		return "subagent_finished"
	case RunFinished:
		return "run_finished"
	default:
		return fmt.Sprintf("%T", ev)
	}
}

// observationRecorder collects one child's display events per session. Sinks
// for different children share it, so every access is locked.
type observationRecorder struct {
	mu       sync.Mutex
	observed []ChildObservation
	events   map[string][]DisplayEvent
}

func newObservationRecorder() *observationRecorder {
	return &observationRecorder{events: make(map[string][]DisplayEvent)}
}

// observe is the SpawnerOptions.Observe hook: it records the registration and
// returns that child's sink.
func (r *observationRecorder) observe(observation ChildObservation) ChildSink {
	r.mu.Lock()
	r.observed = append(r.observed, observation)
	r.mu.Unlock()
	return func(ev DisplayEvent) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events[observation.Session] = append(r.events[observation.Session], ev)
		return nil
	}
}

func (r *observationRecorder) registrations() []ChildObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ChildObservation(nil), r.observed...)
}

func (r *observationRecorder) childEvents(session string) []DisplayEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]DisplayEvent(nil), r.events[session]...)
}

// kinds lists one child's received event kinds in arrival order.
func (r *observationRecorder) kinds(session string) []string {
	var kinds []string
	for _, ev := range r.childEvents(session) {
		kinds = append(kinds, displayKind(ev))
	}
	return kinds
}

// observationCompleter serves every call once and records whether the loop
// took the streaming path.
type observationCompleter struct {
	mu        sync.Mutex
	text      string
	reasoning string
	didStream bool
}

func (c *observationCompleter) Complete(context.Context, model.Request) (model.Response, error) {
	c.mu.Lock()
	c.didStream = false
	c.mu.Unlock()
	return model.Response{
		Text:         c.text,
		Reasoning:    c.reasoning,
		FinishReason: model.FinishStop,
		Usage:        model.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
	}, nil
}

func (c *observationCompleter) CompleteStream(_ context.Context, _ model.Request, stream model.StreamHandler) (model.Response, error) {
	c.mu.Lock()
	c.didStream = true
	c.mu.Unlock()
	if c.reasoning != "" {
		if err := stream.Reasoning(c.reasoning); err != nil {
			return model.Response{}, err
		}
	}
	if c.text != "" {
		if err := stream.Text(c.text); err != nil {
			return model.Response{}, err
		}
	}
	return model.Response{
		Text:         c.text,
		Reasoning:    c.reasoning,
		FinishReason: model.FinishStop,
		Usage:        model.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		Streamed:     true,
	}, nil
}

func (c *observationCompleter) streamedCall() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.didStream
}

func TestSpawnerChildObservationDeliversChildEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		recorder := newObservationRecorder()
		f.spawner.opts.Observe = recorder.observe
		// A retried first attempt supplies the notice, the tool round supplies the
		// call and its result, and the buffered second response supplies the
		// reasoning and text deltas.
		f.completer.steps = []scriptStep{
			{err: &model.TransportError{Err: errors.New("connection reset")}},
			{resp: model.Response{
				ToolCalls:    []model.ToolCall{toolCall("c1", "read_file", `{"intent":"r","path":"x"}`)},
				FinishReason: model.FinishToolCalls,
				Usage:        model.Usage{InputTokens: 10, OutputTokens: 2, Reported: true},
			}},
			{resp: model.Response{
				Text:         "the report",
				Reasoning:    "private thought",
				FinishReason: model.FinishStop,
				Usage:        model.Usage{InputTokens: 12, OutputTokens: 3, Reported: true},
			}},
		}
		report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1", Turn: 2},
			tools.SubagentSpec{Agent: "explore", Task: "Look at the parser."}))
		if report.Status != tools.SubagentCompleted || report.Text != "the report" {
			t.Fatalf("report = %+v", report)
		}

		registrations := recorder.registrations()
		if len(registrations) != 1 {
			t.Fatalf("registrations = %d, want 1", len(registrations))
		}
		observation := registrations[0]
		if observation.CallID != "call-1" || observation.Index != 0 || observation.Agent != "explore" {
			t.Fatalf("observation = %+v", observation)
		}
		if observation.Session != report.Session || observation.Session == "" {
			t.Fatalf("observation session = %q, report session = %q", observation.Session, report.Session)
		}
		if observation.Intent != "Look at the parser." || observation.Model != "test-model" {
			t.Fatalf("observation = %+v", observation)
		}
		if len(observation.Tools) != 2 || observation.Tools[0] != "read_file" {
			t.Fatalf("observation grant = %v", observation.Tools)
		}

		want := []string{
			"run_started", "message_recorded", "model_started", "notice",
			"model_started", "response_completed", "tool_started", "tool_completed",
			"model_started", "reasoning_delta", "text_delta", "response_completed",
			"run_finished",
		}
		if got := recorder.kinds(report.Session); !slices.Equal(got, want) {
			t.Fatalf("child sink kinds = %v, want %v", got, want)
		}
	})
}

func TestSpawnerChildObservationOrderPerChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		rec := &concurrencyRecorder{}
		f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
			return &recorderCompleter{rec: rec, text: "report"}
		}
		var (
			mu      sync.Mutex
			events  = make(map[string][]string, 2)
			arrived int
			release = make(chan struct{})
		)
		f.spawner.opts.Observe = func(observation ChildObservation) ChildSink {
			return func(ev DisplayEvent) error {
				mu.Lock()
				events[observation.Session] = append(events[observation.Session], displayKind(ev))
				mu.Unlock()
				if _, ok := ev.(RunStarted); !ok {
					return nil
				}
				// Two sinks must be in flight at once. A spawner-wide mutex
				// around the sinks would deadlock here instead of passing.
				mu.Lock()
				arrived++
				if arrived == 2 {
					close(release)
				}
				mu.Unlock()
				<-release
				return nil
			}
		}
		outcome := f.spawnOutcome(t, tools.Call{ID: "call-1"},
			tools.SubagentSpec{Agent: "explore", Task: "one"},
			tools.SubagentSpec{Agent: "explore", Task: "two"})
		if outcome.Execution != tools.ExecutionConcurrent {
			t.Fatalf("execution = %q, want concurrent", outcome.Execution)
		}
		want := []string{"run_started", "message_recorded", "model_started", "text_delta", "response_completed", "run_finished"}
		mu.Lock()
		defer mu.Unlock()
		if len(events) != 2 {
			t.Fatalf("observed sessions = %d, want 2", len(events))
		}
		for session, kinds := range events {
			if !slices.Equal(kinds, want) {
				t.Fatalf("child %s kinds = %v, want %v", session, kinds, want)
			}
		}
	})
}

func TestSpawnerChildObservationNilKeepsCounterOnly(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	f.completer.steps = []scriptStep{completedStep("the report", model.Usage{InputTokens: 4, OutputTokens: 2, Reported: true})}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "t"}))
	if report.Status != tools.SubagentCompleted {
		t.Fatalf("report = %+v", report)
	}
	events := f.readLog(t, report.Session)
	start := decodePayload[runStartData](t, eventOfType(t, events, session.TypeRunStart))
	if start.Stream {
		t.Fatal("run_start.stream = true for an unobserved child")
	}
	response := decodePayload[modelResponseData](t, eventOfType(t, events, session.TypeModelResponse))
	if response.Streamed {
		t.Fatal("model_response.streamed = true for an unobserved child")
	}
	var kinds []string
	for _, ev := range f.display {
		if kind := displayKind(ev); kind == "subagent_started" || kind == "subagent_progress" || kind == "subagent_finished" {
			kinds = append(kinds, kind)
		}
	}
	if want := []string{"subagent_started", "subagent_progress", "subagent_finished"}; !slices.Equal(kinds, want) {
		t.Fatalf("parent roster events = %v, want %v", kinds, want)
	}
}

func TestSpawnerChildObservationEnablesStreaming(t *testing.T) {
	observed := newSpawnFixture(t, testConfig())
	streamer := &observationCompleter{text: "streamed report", reasoning: "thought"}
	observed.childCompleter = streamer
	recorder := newObservationRecorder()
	observed.spawner.opts.Observe = recorder.observe
	report := onlyReport(t, observed.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "t"}))
	if report.Status != tools.SubagentCompleted || report.Text != "streamed report" {
		t.Fatalf("report = %+v", report)
	}
	if !streamer.streamedCall() {
		t.Fatal("observed child used Complete, want CompleteStream")
	}
	events := observed.readLog(t, report.Session)
	if start := decodePayload[runStartData](t, eventOfType(t, events, session.TypeRunStart)); !start.Stream {
		t.Fatal("run_start.stream = false for an observed child")
	}
	if response := decodePayload[modelResponseData](t, eventOfType(t, events, session.TypeModelResponse)); !response.Streamed {
		t.Fatal("model_response.streamed = false for an observed child")
	}
	kinds := recorder.kinds(report.Session)
	if !slices.Contains(kinds, "reasoning_delta") || !slices.Contains(kinds, "text_delta") {
		t.Fatalf("sink kinds = %v, want the streamed deltas", kinds)
	}

	unobserved := newSpawnFixture(t, testConfig())
	plain := &observationCompleter{text: "buffered report"}
	unobserved.childCompleter = plain
	report = onlyReport(t, unobserved.spawn(t, tools.Call{ID: "call-2"}, tools.SubagentSpec{Agent: "explore", Task: "t"}))
	if plain.streamedCall() {
		t.Fatal("unobserved child streamed, want the buffered path")
	}
	plainEvents := unobserved.readLog(t, report.Session)
	if response := decodePayload[modelResponseData](t, eventOfType(t, plainEvents, session.TypeModelResponse)); response.Streamed {
		t.Fatal("model_response.streamed = true for an unobserved child")
	}
}

func TestSpawnerChildObservationBufferedFallback(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	recorder := newObservationRecorder()
	f.spawner.opts.Observe = recorder.observe
	f.completer.steps = []scriptStep{completedStep("buffered report", model.Usage{InputTokens: 3, OutputTokens: 2, Reported: true})}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "t"}))
	deltas := 0
	text := ""
	for _, ev := range recorder.childEvents(report.Session) {
		if delta, ok := ev.(TextDelta); ok {
			deltas++
			text = delta.Text
		}
	}
	if deltas != 1 || text != "buffered report" {
		t.Fatalf("text deltas = %d (%q), want exactly the buffered response", deltas, text)
	}
}

func TestSpawnerChildObservationErrorEndsThatChildOnly(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	rec := &concurrencyRecorder{}
	f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
		return &recorderCompleter{rec: rec, text: "report"}
	}
	f.spawner.opts.Observe = func(observation ChildObservation) ChildSink {
		return func(DisplayEvent) error {
			if observation.Index == 0 {
				return errors.New("the renderer is broken")
			}
			return nil
		}
	}
	outcome := f.spawnOutcome(t, tools.Call{ID: "call-1"},
		tools.SubagentSpec{Agent: "explore", Task: "one"},
		tools.SubagentSpec{Agent: "explore", Task: "two"})
	if outcome.Execution != tools.ExecutionConcurrent {
		t.Fatalf("execution = %q, want concurrent", outcome.Execution)
	}
	failed, ok := outcome.Reports[0], outcome.Reports[1]
	if failed.Status != tools.SubagentInternalError || failed.Code != tools.CodeInternal {
		t.Fatalf("failed child = %+v", failed)
	}
	if !strings.Contains(failed.Text, "the renderer is broken") {
		t.Fatalf("failed child text = %q, want the sink error", failed.Text)
	}
	if failed.Text != "the renderer is broken" {
		t.Fatalf("failed child text = %q, want the sink error alone", failed.Text)
	}
	if ok.Status != tools.SubagentCompleted || ok.Text != "report" {
		t.Fatalf("sibling = %+v", ok)
	}
	var finished []SubagentFinished
	for _, ev := range f.display {
		if fin, isFin := ev.(SubagentFinished); isFin {
			finished = append(finished, fin)
		}
	}
	if len(finished) != 2 {
		t.Fatalf("SubagentFinished events = %d, want 2", len(finished))
	}
	for _, fin := range finished {
		switch fin.Index {
		case 0:
			if fin.Status != tools.SubagentInternalError || !strings.Contains(fin.Error, "the renderer is broken") {
				t.Fatalf("finished event = %+v", fin)
			}
		case 1:
			if fin.Status != tools.SubagentCompleted {
				t.Fatalf("finished event = %+v", fin)
			}
		default:
			t.Fatalf("unexpected finished index %d", fin.Index)
		}
	}
}

func TestSpawnerChildObservationPanicIsRecovered(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	rec := &concurrencyRecorder{}
	f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
		return &recorderCompleter{rec: rec, text: "report"}
	}
	f.spawner.opts.Observe = func(observation ChildObservation) ChildSink {
		return func(DisplayEvent) error {
			if observation.Index == 0 {
				panic("the renderer exploded")
			}
			return nil
		}
	}
	outcome := f.spawnOutcome(t, tools.Call{ID: "call-1"},
		tools.SubagentSpec{Agent: "explore", Task: "one"},
		tools.SubagentSpec{Agent: "explore", Task: "two"})
	failed := outcome.Reports[0]
	if failed.Status != tools.SubagentInternalError || failed.Code != tools.CodeInternal {
		t.Fatalf("panicking child = %+v", failed)
	}
	if !strings.Contains(failed.Text, "panicked") {
		t.Fatalf("panicking child text = %q, want the recovered panic", failed.Text)
	}
	if sibling := outcome.Reports[1]; sibling.Status != tools.SubagentCompleted {
		t.Fatalf("sibling = %+v", sibling)
	}
}

func TestSpawnerChildObservationHookPanicIsRecovered(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	f.completer.steps = []scriptStep{completedStep("report", model.Usage{InputTokens: 1, OutputTokens: 1})}
	f.spawner.opts.Observe = func(ChildObservation) ChildSink {
		panic("the hook exploded")
	}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "t"}))
	if report.Status != tools.SubagentInternalError || report.Code != tools.CodeInternal {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Text, "panicked") {
		t.Fatalf("report text = %q, want the recovered panic", report.Text)
	}
	if len(f.completer.requests) != 0 {
		t.Fatalf("child model calls = %d, want none after a failed registration", len(f.completer.requests))
	}
}

func TestSpawnerChildObservationSanitizes(t *testing.T) {
	raw := "\x1b[31mred\x1b[0m\x07"
	f := newSpawnFixture(t, testConfig())
	recorder := newObservationRecorder()
	f.spawner.opts.Observe = recorder.observe
	f.completer.steps = []scriptStep{completedStep(raw, model.Usage{InputTokens: 1, OutputTokens: 1})}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "t"}))
	var text string
	for _, ev := range recorder.childEvents(report.Session) {
		if delta, ok := ev.(TextDelta); ok {
			text = delta.Text
		}
	}
	if text == raw || strings.ContainsRune(text, 0x1b) || strings.ContainsRune(text, 0x07) {
		t.Fatalf("sink text = %q, want the display-safe form", text)
	}
	if !utf8.ValidString(text) || !strings.Contains(text, "red") {
		t.Fatalf("sink text = %q, want valid UTF-8 carrying the text", text)
	}
	for _, r := range text {
		if r != '\n' && r != '\t' && r < 0x20 {
			t.Fatalf("sink text = %q retains control rune %#x", text, r)
		}
	}
}

func TestSpawnerChildObservationSkipsPreStartFailure(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	recorder := newObservationRecorder()
	f.spawner.opts.Observe = recorder.observe
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "missing", Task: "t"}))
	if report.Code != tools.CodeNotFound {
		t.Fatalf("report = %+v", report)
	}
	if got := recorder.registrations(); len(got) != 0 {
		t.Fatalf("registrations = %d for a task that never started: %+v", len(got), got)
	}
	if len(f.display) != 1 {
		t.Fatalf("parent events = %d, want one SubagentFinished", len(f.display))
	}
	if _, ok := f.display[0].(SubagentFinished); !ok {
		t.Fatalf("parent event = %T, want SubagentFinished", f.display[0])
	}

	// A task rejected by the subagent budget never starts either, while its
	// sibling registers exactly one sink.
	limited := newSpawnFixture(t, testConfig())
	limited.spawner.opts.MaxSubagents = 1
	limitedRecorder := newObservationRecorder()
	limited.spawner.opts.Observe = limitedRecorder.observe
	limited.completer.steps = []scriptStep{completedStep("report", model.Usage{InputTokens: 1, OutputTokens: 1})}
	reports := limited.spawn(t, tools.Call{ID: "call-2"},
		tools.SubagentSpec{Agent: "explore", Task: "one"},
		tools.SubagentSpec{Agent: "explore", Task: "two"})
	if reports[0].Status != tools.SubagentCompleted {
		t.Fatalf("first report = %+v", reports[0])
	}
	if reports[1].Status != tools.SubagentFailed || reports[1].Code != tools.CodeInvalidArgs {
		t.Fatalf("second report = %+v", reports[1])
	}
	if got := limitedRecorder.registrations(); len(got) != 1 {
		t.Fatalf("registrations = %d, want one for the started child", len(got))
	}
}
