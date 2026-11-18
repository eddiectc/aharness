package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
)

// Display history caps. They bound the UI projection only; the log is
// unchanged.
const (
	maxHistoryItems        = 20_000
	maxHistoryTextBytes    = 16 << 20
	maxHistoryItemBytes    = 64 << 10
	maxHistoryArgsBytes    = 2 << 10
	maxHistoryDetailsBytes = 4 << 10
)

// HistoryKind classifies one display-only transcript item.
type HistoryKind string

const (
	HistoryRun       HistoryKind = "run"
	HistoryUser      HistoryKind = "user"
	HistoryAssistant HistoryKind = "assistant"
	HistoryTool      HistoryKind = "tool"
	HistoryNotice    HistoryKind = "notice"
)

// HistoryItem is one display-only transcript entry reconstructed from the
// durable log. Text is capped and sanitized for invalid UTF-8; it is markdown
// source for assistant items and must be rendered by the view.
type HistoryItem struct {
	Kind      HistoryKind
	Run       int
	Turn      int
	Source    string
	Text      string
	CallID    string
	Name      string
	Arguments json.RawMessage
	Pending   bool
	OK        bool
	Code      string
	Output    string
	Error     string
	ExitCode  int
	Truncated bool
	TimedOut  bool
	// Details is the structured tool result payload recorded in the log. The
	// host decodes a delegate result from it to render the recorded per-task
	// table after a resume.
	Details json.RawMessage
	// NoticeKind classifies a HistoryNotice item; empty for other kinds.
	NoticeKind NoticeKind
	Status     StopReason
	Reason     string
	Model      string
	Dialect    string
	Duration   time.Duration
	Turns      int
	Usage      model.Usage
}

// History reconstructs a capped, display-only transcript from v2 events. It
// never reprints final text: the assistant item already comes from
// model_response. Unknown event types and fields are ignored; a malformed
// known payload is an error.
func History(events []session.Event) ([]HistoryItem, error) {
	b := historyBuilder{}
	for _, ev := range events {
		if err := b.add(ev); err != nil {
			return nil, fmt.Errorf("history: event %s seq %d: %w", ev.Type, ev.Seq, err)
		}
	}
	return b.finish(), nil
}

type historyBuilder struct {
	items    []HistoryItem
	run      int
	lastRun  int // index of the current run header, -1 when none
	textSize int
	omitted  bool
}

func (b *historyBuilder) add(ev session.Event) error {
	switch ev.Type {
	case session.TypeSessionStart:
		var data sessionStartData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		if data.ParentSession != "" {
			b.append(HistoryItem{
				Kind:       HistoryNotice,
				NoticeKind: NoticeWarning,
				Text:       fmt.Sprintf("forked from session %s at seq %d", data.ParentSession, data.ParentSeq),
			})
		}
	case session.TypeRunStart:
		var data runStartData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		b.run++
		b.lastRun = len(b.items)
		b.append(HistoryItem{
			Kind:    HistoryRun,
			Run:     b.run,
			Turn:    ev.Turn,
			Model:   data.Model,
			Dialect: data.Dialect,
		})
	case session.TypeRunEnd:
		var data runEndData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		if b.lastRun >= 0 && b.lastRun < len(b.items) && b.items[b.lastRun].Kind == HistoryRun {
			item := &b.items[b.lastRun]
			item.Status = data.Status
			item.Reason = capText(cleanText(data.Reason), maxErrorEventBytes)
			item.Duration = time.Duration(data.DurationMS) * time.Millisecond
			item.Turns = data.RunTurns
			item.Usage = data.RunUsage
		}
	case session.TypeUserMessage:
		var data userMessageData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		text := capText(cleanText(data.Text), maxHistoryItemBytes)
		switch data.Source {
		case sourceInterruptHint:
			b.append(HistoryItem{Kind: HistoryNotice, NoticeKind: NoticeInterrupt, Run: b.run, Turn: ev.Turn, Text: text, Source: data.Source})
		case sourceContinuationHint, sourceStuckHint:
			b.append(HistoryItem{Kind: HistoryNotice, NoticeKind: NoticeWarning, Run: b.run, Turn: ev.Turn, Text: text, Source: data.Source})
		default:
			b.append(HistoryItem{Kind: HistoryUser, Run: b.run, Turn: ev.Turn, Source: data.Source, Text: text})
		}
	case session.TypeModelResponse:
		var data modelResponseData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		text := capText(cleanText(data.Text), maxHistoryItemBytes)
		if text != "" {
			b.append(HistoryItem{Kind: HistoryAssistant, Run: b.run, Turn: ev.Turn, Text: text, Usage: data.Usage})
		}
		for _, call := range data.ToolCalls {
			b.append(HistoryItem{
				Kind:      HistoryTool,
				Run:       b.run,
				Turn:      ev.Turn,
				CallID:    call.ID,
				Name:      call.Name,
				Arguments: capHistoryArguments(call.Arguments),
				Pending:   true,
			})
		}
	case session.TypeToolResult:
		var data toolResultData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		item := b.fillToolResult(data, ev.Turn)
		if item == nil {
			anomaly := "anomalous tool_result without a matching model_response tool call"
			b.append(HistoryItem{
				Kind:      HistoryTool,
				Run:       b.run,
				Turn:      ev.Turn,
				CallID:    data.CallID,
				Name:      data.Name,
				OK:        data.OK,
				Code:      string(data.Code),
				Output:    capText(cleanText(data.Output), maxHistoryItemBytes),
				Error:     anomaly,
				ExitCode:  historyExitCode(data.ExitCode),
				Truncated: data.Truncated,
				TimedOut:  data.TimedOut,
			})
		}
	case session.TypeInterrupt:
		var data interruptData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		text := fmt.Sprintf("interrupted %s", data.Operation)
		if data.Name != "" {
			text = fmt.Sprintf("interrupted %s %s", data.Operation, data.Name)
		}
		if data.Reason != "" {
			text += ": " + data.Reason
		}
		b.append(HistoryItem{Kind: HistoryNotice, NoticeKind: NoticeInterrupt, Run: b.run, Turn: ev.Turn, Text: capText(cleanText(text), maxErrorEventBytes)})
		if data.PartialText != "" {
			b.appendPartial(ev.Turn, NoticeInterrupt, "interrupted", data.PartialText)
		}
	case session.TypeError:
		var data errorData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		text := fmt.Sprintf("%s error: %s", data.Class, data.Message)
		b.append(HistoryItem{Kind: HistoryNotice, NoticeKind: NoticeError, Run: b.run, Turn: ev.Turn, Text: capText(cleanText(text), maxErrorEventBytes)})
		if data.PartialText != "" {
			b.appendPartial(ev.Turn, NoticeError, "failed", data.PartialText)
		}
	case session.TypeLimit:
		var data limitData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		b.append(HistoryItem{
			Kind:       HistoryNotice,
			NoticeKind: NoticeLimit,
			Run:        b.run,
			Turn:       ev.Turn,
			Text:       capText(fmt.Sprintf("limit %s: configured %d, observed %d", data.Kind, data.Configured, data.Observed), maxErrorEventBytes),
		})
	case session.TypeCompaction:
		var data compactionData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		b.append(HistoryItem{
			Kind:       HistoryNotice,
			NoticeKind: NoticeCompaction,
			Run:        b.run,
			Turn:       ev.Turn,
			Text: capText(fmt.Sprintf("context compacted (%s): %d -> %d tokens, %d tool results elided",
				data.Policy, data.BeforeTokens, data.AfterTokens, data.ElidedResults), maxErrorEventBytes),
		})
	case session.TypeSessionRepair:
		var data session.RepairData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		b.append(HistoryItem{
			Kind:       HistoryNotice,
			NoticeKind: NoticeRepair,
			Run:        b.run,
			Turn:       ev.Turn,
			Text:       capText(fmt.Sprintf("session log repaired: discarded %d bytes (%s)", data.DiscardedBytes, data.Reason), maxErrorEventBytes),
		})
	case session.TypeModelRetry:
		var data modelRetryData
		if err := decodeEventData(ev, &data); err != nil {
			return err
		}
		text := fmt.Sprintf("model retry %d (%s)", data.Attempt, data.Class)
		if data.Status != 0 {
			text += fmt.Sprintf(" status %d", data.Status)
		}
		if data.DelayMS > 0 {
			text += fmt.Sprintf(" after %dms", data.DelayMS)
		}
		b.append(HistoryItem{Kind: HistoryNotice, NoticeKind: NoticeRetry, Run: b.run, Turn: ev.Turn, Text: text})
	case session.TypeFinal, session.TypeModelRequest:
		// final text duplicates the last model_response; requests are debug
		// payloads and are not part of the display transcript.
	}
	return nil
}

func (b *historyBuilder) appendPartial(turn int, kind NoticeKind, state, text string) {
	capped := capText(cleanText(text), maxHistoryItemBytes)
	b.append(HistoryItem{
		Kind:       HistoryNotice,
		NoticeKind: kind,
		Run:        b.run,
		Turn:       turn,
		Text:       fmt.Sprintf("partial assistant text (%s, %d bytes):\n%s", state, len(text), capped),
	})
}

// fillToolResult pairs a result with the most recent pending call carrying the
// same ID. It returns the filled item or nil when no call matches.
func (b *historyBuilder) fillToolResult(data toolResultData, turn int) *HistoryItem {
	for i := len(b.items) - 1; i >= 0; i-- {
		item := &b.items[i]
		if item.Kind != HistoryTool || !item.Pending || item.CallID != data.CallID {
			continue
		}
		item.Pending = false
		item.OK = data.OK
		item.Code = string(data.Code)
		item.Output = capText(cleanText(data.Output), maxHistoryItemBytes)
		item.Error = capText(cleanText(data.Error), maxHistoryItemBytes)
		item.ExitCode = historyExitCode(data.ExitCode)
		item.Truncated = data.Truncated
		item.TimedOut = data.TimedOut
		item.Details = capHistoryDetails(data.Details)
		item.Duration = time.Duration(data.DurationMS) * time.Millisecond
		item.Turn = turn
		return item
	}
	return nil
}

func historyExitCode(code *int) int {
	if code == nil {
		return 0
	}
	return *code
}

// capHistoryDetails keeps a decode-able delegate summary without letting a
// hostile log grow the display projection. Larger payloads are dropped; the
// durable log is unchanged.
func capHistoryDetails(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || len(raw) > maxHistoryDetailsBytes {
		return nil
	}
	return slices.Clone(raw)
}

// capHistoryArguments keeps tool arguments viewable without letting a hostile
// provider payload grow the history. Invalid JSON is preserved as a JSON
// string so the item still decodes.
func capHistoryArguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxHistoryArgsBytes {
		quoted, err := json.Marshal(capText(cleanText(string(raw)), maxHistoryArgsBytes))
		if err != nil {
			return json.RawMessage(`"<unprintable arguments>"`)
		}
		return quoted
	}
	return slices.Clone(raw)
}

func (b *historyBuilder) append(item HistoryItem) {
	b.items = append(b.items, item)
	b.textSize += historyTextBytes(item)
	for (len(b.items) > maxHistoryItems || b.textSize > maxHistoryTextBytes) && len(b.items) > 1 {
		b.textSize -= historyTextBytes(b.items[0])
		b.items = b.items[1:]
		if b.lastRun == 0 {
			b.lastRun = -1
		} else if b.lastRun > 0 {
			b.lastRun--
		}
		b.omitted = true
	}
}

func historyTextBytes(item HistoryItem) int {
	n := len(item.Text) + len(item.Output) + len(item.Error) + len(item.Reason) + len(item.Arguments)
	return n
}

func (b *historyBuilder) finish() []HistoryItem {
	if !b.omitted {
		return b.items
	}
	items := make([]HistoryItem, 0, len(b.items)+1)
	items = append(items, HistoryItem{Kind: HistoryNotice, NoticeKind: NoticeWarning, Text: "older history omitted by the display cap"})
	return append(items, b.items...)
}
