package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

func histEvent(t *testing.T, seq uint64, typ session.EventType, turn int, payload any) session.Event {
	t.Helper()
	ev, err := session.NewEvent(typ, turn, "", payload)
	if err != nil {
		t.Fatalf("NewEvent(%s): %v", typ, err)
	}
	ev.Seq = seq
	ev.TS = time.Unix(0, 0)
	ev.Session = "s"
	ev.V = session.Version
	return ev
}

func TestHistoryMultipleRunsAndToolPairing(t *testing.T) {
	exitCode := 0
	events := []session.Event{
		histEvent(t, 1, session.TypeSessionStart, 0, sessionStartData{
			Workspace: "/w", Provider: "p", PromptVersion: PromptVersion, ToolsetVersion: tools.Version,
			Model: "m", Dialect: "openai-chat", ParentSession: "parent", ParentSeq: 7,
		}),
		histEvent(t, 2, session.TypeRunStart, 0, runStartData{Model: "m", Dialect: "openai-chat"}),
		histEvent(t, 3, session.TypeUserMessage, 0, userMessageData{Text: "do it", Source: "-task"}),
		histEvent(t, 4, session.TypeModelResponse, 1, modelResponseData{
			Text: "first answer", FinishReason: model.FinishToolCalls,
			ToolCalls: []loggedToolCall{
				{ID: "a", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)},
				{ID: "b", Name: "read_file", Arguments: json.RawMessage(`{"path":"b"}`)},
			},
			Usage: model.Usage{InputTokens: 5, OutputTokens: 2},
		}),
		histEvent(t, 5, session.TypeToolResult, 1, toolResultData{
			CallID: "a", Name: "read_file", OK: true, Code: tools.CodeOK, Output: "contents",
			ExitCode: &exitCode, DurationMS: 12,
		}),
		histEvent(t, 6, session.TypeModelResponse, 2, modelResponseData{
			Text: "second answer", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 3, OutputTokens: 1},
		}),
		histEvent(t, 7, session.TypeFinal, 2, finalData{Status: StopCompleted, Text: "second answer"}),
		histEvent(t, 8, session.TypeRunEnd, 2, runEndData{
			Status: StopCompleted, Reason: "done", RunTurns: 2,
			RunUsage: model.Usage{InputTokens: 8, OutputTokens: 3},
			Turns:    2, Usage: model.Usage{InputTokens: 8, OutputTokens: 3}, DurationMS: 1500,
		}),
		histEvent(t, 9, session.TypeRunStart, 2, runStartData{Model: "m2", Dialect: "openai-responses"}),
		histEvent(t, 10, session.TypeUserMessage, 2, userMessageData{Text: "steer text", Source: sourceSteer}),
		histEvent(t, 11, session.TypeUserMessage, 2, userMessageData{Text: "hint text", Source: sourceInterruptHint}),
		histEvent(t, 12, session.TypeInterrupt, 2, interruptData{Operation: operationModel, PartialText: "part", Reason: "interrupted"}),
		histEvent(t, 13, session.TypeError, 2, errorData{Class: errorClassHTTP, Message: "boom", PartialText: "failpart", PartialBytes: 8}),
		histEvent(t, 14, session.TypeLimit, 2, limitData{Kind: limitMaxTurns, Configured: 2, Observed: 2}),
		histEvent(t, 15, session.TypeCompaction, 2, compactionData{BeforeTokens: 10, AfterTokens: 4, ElidedResults: 1, Policy: policyCompaction}),
		histEvent(t, 16, session.TypeModelRetry, 2, modelRetryData{Attempt: 1, Class: retryClassHTTP, Status: 500, DelayMS: 10}),
		histEvent(t, 17, session.TypeRunEnd, 2, runEndData{Status: StopInterrupted, Reason: "interrupted"}),
		histEvent(t, 18, session.TypeRunEnd, 2, runEndData{Status: StopInterrupted}),
		{Seq: 19, Type: "future_event", Data: json.RawMessage(`{"x":1}`)},
	}
	items, err := History(events)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if items[0].Kind != HistoryNotice || !strings.Contains(items[0].Text, "forked from session parent") {
		t.Fatalf("first item = %+v, want fork notice", items[0])
	}
	var runs, assistants, tools, notices int
	for _, item := range items {
		switch item.Kind {
		case HistoryRun:
			runs++
		case HistoryAssistant:
			assistants++
		case HistoryTool:
			tools++
		case HistoryNotice:
			notices++
		}
		if item.Kind == HistoryAssistant && item.Text == "second answer" {
			// The final event must not duplicate the canonical response.
			if assistants > 2 {
				t.Fatalf("assistant text duplicated: %+v", items)
			}
		}
	}
	if runs != 2 || assistants != 2 || tools != 2 {
		t.Fatalf("runs/assistants/tools = %d/%d/%d\n%+v", runs, assistants, tools, items)
	}
	if items[1].Kind != HistoryRun || items[1].Status != StopCompleted || items[1].Turns != 2 ||
		items[1].Usage.Total() != 11 || items[1].Duration != 1500*time.Millisecond {
		t.Fatalf("completed run item = %+v", items[1])
	}
	var paired, pending bool
	for _, item := range items {
		if item.Kind != HistoryTool {
			continue
		}
		switch item.CallID {
		case "a":
			paired = !item.Pending && item.OK && item.Output == "contents" && item.ExitCode == 0
		case "b":
			pending = item.Pending
		}
	}
	if !paired || !pending {
		t.Fatalf("tool pairing paired=%v pending=%v\n%+v", paired, pending, items)
	}
	joined := ""
	for _, item := range items {
		joined += item.Text + "\n"
	}
	for _, want := range []string{"hint text", "partial assistant text (interrupted", "partial assistant text (failed", "limit max_turns", "context compacted", "model retry"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("history missing %q\n%s", want, joined)
		}
	}
}

func TestHistoryNoticeKinds(t *testing.T) {
	events := []session.Event{
		histEvent(t, 1, session.TypeSessionStart, 0, sessionStartData{
			Workspace: "/w", PromptVersion: PromptVersion, ToolsetVersion: tools.Version,
			ParentSession: "parent", ParentSeq: 3,
		}),
		histEvent(t, 2, session.TypeUserMessage, 0, userMessageData{Text: "hint", Source: sourceInterruptHint}),
		histEvent(t, 3, session.TypeInterrupt, 0, interruptData{Operation: operationModel, Reason: "stop", PartialText: "part"}),
		histEvent(t, 4, session.TypeError, 0, errorData{Class: errorClassHTTP, Message: "boom", PartialText: "part"}),
		histEvent(t, 5, session.TypeLimit, 0, limitData{Kind: limitMaxTurns, Configured: 1, Observed: 1}),
		histEvent(t, 6, session.TypeCompaction, 0, compactionData{BeforeTokens: 10, AfterTokens: 5, Policy: policyCompaction}),
		histEvent(t, 7, session.TypeSessionRepair, 0, session.RepairData{DiscardedBytes: 4, Reason: "torn"}),
		histEvent(t, 8, session.TypeModelRetry, 0, modelRetryData{Attempt: 1, Class: retryClassHTTP}),
	}
	items, err := History(events)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	var kinds []NoticeKind
	for _, item := range items {
		if item.Kind == HistoryNotice {
			kinds = append(kinds, item.NoticeKind)
		}
	}
	want := []NoticeKind{
		NoticeWarning,
		NoticeInterrupt,
		NoticeInterrupt, NoticeInterrupt,
		NoticeError, NoticeError,
		NoticeLimit,
		NoticeCompaction,
		NoticeRepair,
		NoticeRetry,
	}
	if !slices.Equal(kinds, want) {
		t.Fatalf("notice kinds = %v, want %v", kinds, want)
	}
}

func TestHistoryMalformedKnownPayload(t *testing.T) {
	events := []session.Event{{
		Seq: 1, Type: session.TypeRunStart, Data: json.RawMessage(`{"model":`),
	}}
	_, err := History(events)
	if err == nil || !strings.Contains(err.Error(), "run_start") || !strings.Contains(err.Error(), "seq 1") {
		t.Fatalf("error = %v", err)
	}
}

func TestHistoryAnomalousResultAndArgumentsCap(t *testing.T) {
	big := `{"path":"` + strings.Repeat("x", 5000) + `"}`
	events := []session.Event{
		histEvent(t, 1, session.TypeRunStart, 0, runStartData{Model: "m"}),
		histEvent(t, 2, session.TypeModelResponse, 1, modelResponseData{
			ToolCalls: []loggedToolCall{{ID: "a", Name: "read_file", Arguments: json.RawMessage(big)}},
		}),
		histEvent(t, 3, session.TypeToolResult, 1, toolResultData{CallID: "orphan", Name: "x", Code: tools.CodeOK}),
	}
	items, err := History(events)
	if err != nil {
		t.Fatal(err)
	}
	var anomaly, capped bool
	for _, item := range items {
		if item.Kind != HistoryTool {
			continue
		}
		if item.CallID == "orphan" && strings.Contains(item.Error, "anomalous") {
			anomaly = true
		}
		if item.CallID == "a" && len(item.Arguments) <= maxHistoryArgsBytes+64 {
			capped = item.Arguments[0] == '"'
		}
	}
	if !anomaly || !capped {
		t.Fatalf("anomaly=%v capped=%v\n%+v", anomaly, capped, items)
	}
}

func TestHistoryCapsItemCount(t *testing.T) {
	events := make([]session.Event, 0, maxHistoryItems+10)
	for i := range maxHistoryItems + 10 {
		events = append(events, histEvent(t, uint64(i+1), session.TypeUserMessage, 0, userMessageData{Text: "x", Source: "-task"}))
	}
	items, err := History(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) > maxHistoryItems+1 {
		t.Fatalf("items = %d, want capped", len(items))
	}
	if !strings.Contains(items[0].Text, "older history omitted") {
		t.Fatalf("missing omitted marker: %+v", items[0])
	}
}
