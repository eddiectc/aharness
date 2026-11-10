package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

func eventName(ev DisplayEvent) string {
	switch ev.(type) {
	case RunStarted:
		return "run_started"
	case ModelStarted:
		return "model_started"
	case TextDelta:
		return "text_delta"
	case ResponseCompleted:
		return "response_completed"
	case ToolStarted:
		return "tool_started"
	case ToolCompleted:
		return "tool_completed"
	case MessageRecorded:
		return "message_recorded"
	case Notice:
		return "notice"
	case RunFinished:
		return "run_finished"
	default:
		return "unknown"
	}
}

func TestDisplayEventOrderStreamingAndTools(t *testing.T) {
	cfg := testConfig()
	cfg.Stream = true
	var seen []string
	cfg.Display = func(ev DisplayEvent) error {
		seen = append(seen, eventName(ev))
		if delta, ok := ev.(TextDelta); ok && delta.Text == "" {
			t.Error("empty TextDelta emitted")
		}
		return nil
	}
	comp := &fakeStreamCompleter{steps: []streamStep{
		{
			deltas: []string{"hel", "lo"},
			resp: model.Response{Text: "hello", FinishReason: model.FinishToolCalls,
				ToolCalls: []model.ToolCall{{ID: "call_1", Name: "read_file", Arguments: []byte(`{"path":"a"}`)}},
				Usage:     model.Usage{InputTokens: 3, OutputTokens: 2}},
		},
		{
			deltas: []string{"done"},
			resp:   model.Response{Text: "done", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 2, OutputTokens: 1}},
		},
	}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	want := []string{
		"run_started",
		"message_recorded", // task
		"model_started",
		"text_delta", "text_delta",
		"response_completed",
		"tool_started", "tool_completed",
		"model_started",
		"text_delta",
		"response_completed",
		"run_finished",
	}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v\nwant   %v", seen, want)
	}
	if countEvents(store, session.TypeFinal) != 1 || countEvents(store, session.TypeRunEnd) != 1 {
		t.Fatalf("final/run_end = %d/%d", countEvents(store, session.TypeFinal), countEvents(store, session.TypeRunEnd))
	}
	if countEvents(store, session.TypeModelResponse) != 2 {
		t.Fatalf("model_response = %d", countEvents(store, session.TypeModelResponse))
	}
}

func TestDisplayFallbackTextOnceBeforeResponseCompleted(t *testing.T) {
	cfg := testConfig()
	cfg.Stream = true
	var deltas []string
	var order []string
	cfg.Display = func(ev DisplayEvent) error {
		order = append(order, eventName(ev))
		if delta, ok := ev.(TextDelta); ok {
			deltas = append(deltas, delta.Text)
		}
		return nil
	}
	comp := &fakeStreamCompleter{steps: []streamStep{{
		resp: model.Response{Text: "fallback", FinishReason: model.FinishStop},
	}}}
	if _, _, err := runTask(t, cfg, comp, &fakeToolset{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(deltas, "|") != "fallback" {
		t.Fatalf("deltas = %v, want one fallback", deltas)
	}
	joined := strings.Join(order, ",")
	if strings.Index(joined, "text_delta") > strings.Index(joined, "response_completed") {
		t.Fatalf("fallback delta was not emitted before response_completed: %v", order)
	}
}

func TestDisplayRunStartedCarriesModelAndResumeSeq(t *testing.T) {
	cfg := testConfig()
	cfg.Model = "muse-spark-1.3-contributor"
	cfg.Dialect = string(model.DialectResponses)
	var started RunStarted
	cfg.Display = func(ev DisplayEvent) error {
		if rs, ok := ev.(RunStarted); ok {
			started = rs
		}
		return nil
	}
	store := &fakeStore{}
	runner := newRunner(cfg, &fakeCompleter{steps: []scriptStep{{resp: model.Response{Text: "ok", FinishReason: model.FinishStop}}}}, &fakeToolset{}, store)
	startTestSession(t, runner)
	opts := defaultRunOptions()
	opts.ResumedFromSeq = 42
	if _, err := runner.Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if started.Model != cfg.Model || started.Dialect != cfg.Dialect || started.ResumedFromSeq != 42 {
		t.Fatalf("RunStarted = %+v", started)
	}
}

func TestDisplayErrorIsInternalAndTerminal(t *testing.T) {
	cfg := testConfig()
	cfg.Display = func(DisplayEvent) error { return errors.New("display broken") }
	store, res, err := runTask(t, cfg, &fakeCompleter{steps: []scriptStep{{resp: model.Response{Text: "x", FinishReason: model.FinishStop}}}}, &fakeToolset{})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if res.Status != StopInternalError {
		t.Fatalf("status = %s, want internal_error", res.Status)
	}
	if countEvents(store, session.TypeFinal) != 1 || countEvents(store, session.TypeRunEnd) != 1 {
		t.Fatalf("final/run_end = %d/%d", countEvents(store, session.TypeFinal), countEvents(store, session.TypeRunEnd))
	}
	if countEvents(store, session.TypeError) == 0 {
		t.Fatal("display failure was not recorded as an error event")
	}
}

func TestDisplayToolStartFailurePairsCanceledCalls(t *testing.T) {
	cfg := testConfig()
	cfg.Display = func(ev DisplayEvent) error {
		if _, ok := ev.(ToolStarted); ok {
			return errors.New("display broken")
		}
		return nil
	}
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		FinishReason: model.FinishToolCalls,
		ToolCalls: []model.ToolCall{
			{ID: "a", Name: "read_file", Arguments: []byte(`{"path":"a"}`)},
			{ID: "b", Name: "read_file", Arguments: []byte(`{"path":"b"}`)},
		},
	}}}}
	ts := &fakeToolset{}
	store, res, err := runTask(t, cfg, comp, ts)
	if err == nil || res.Status != StopInternalError {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	if len(ts.calls) != 0 {
		t.Fatalf("executed tools = %+v, want none", ts.calls)
	}
	results := eventsByType(store, session.TypeToolResult)
	if len(results) != 2 {
		t.Fatalf("tool results = %d, want 2", len(results))
	}
	for _, ev := range results {
		if data := decodePayload[toolResultData](t, ev); data.Code != tools.CodeCanceled {
			t.Fatalf("tool result code = %s, want canceled", data.Code)
		}
	}
}

func TestDisplayNilDisablesEvents(t *testing.T) {
	cfg := testConfig()
	cfg.Display = nil
	if _, res, err := runTask(t, cfg, &fakeCompleter{steps: []scriptStep{{resp: model.Response{Text: "ok", FinishReason: model.FinishStop}}}}, &fakeToolset{}); err != nil || res.Status != StopCompleted {
		t.Fatalf("result = %+v err = %v", res, err)
	}
}

func TestDisplaySanitizesTerminalControls(t *testing.T) {
	cfg := testConfig()
	cfg.Stream = true
	var delta, notice string
	cfg.Display = func(ev DisplayEvent) error {
		switch ev := ev.(type) {
		case TextDelta:
			delta += ev.Text
		case Notice:
			notice = ev.Text
		}
		return nil
	}
	comp := &fakeStreamCompleter{steps: []streamStep{{
		deltas: []string{"a\x1b]52;c;x\x07b\u202e"},
		resp:   model.Response{Text: "a\x1b]52;c;x\x07b\u202e", FinishReason: model.FinishStop},
	}}}
	if _, _, err := runTask(t, cfg, comp, &fakeToolset{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.ContainsRune(delta, 0x1b) || strings.ContainsRune(delta, 0x07) || strings.ContainsRune(delta, 0x202e) {
		t.Fatalf("delta contains controls: %q", delta)
	}
	if strings.ContainsRune(notice, 0x1b) {
		t.Fatalf("notice contains ESC: %q", notice)
	}
}
