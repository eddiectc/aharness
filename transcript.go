package aharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/session"
)

// TranscriptKind classifies one transcript item.
type TranscriptKind string

const (
	// TranscriptRun is a run header with its effective model and terminal status.
	TranscriptRun TranscriptKind = "run"
	// TranscriptUser is a task, steering line, or hint.
	TranscriptUser TranscriptKind = "user"
	// TranscriptAssistant is assistant text, as markdown source.
	TranscriptAssistant TranscriptKind = "assistant"
	// TranscriptTool is a tool call or its paired result.
	TranscriptTool TranscriptKind = "tool"
	// TranscriptNotice is a retry, limit, compaction, interrupt, repair, or
	// warning notice.
	TranscriptNotice TranscriptKind = "notice"
)

// TranscriptItem is one display-only transcript entry reconstructed from a
// session log. It mirrors agent.HistoryItem field for field; the facade copies
// the component type at the boundary, so the component struct stays free to
// change. Text is markdown source for assistant items and must be rendered by
// the host. Pending marks a tool call with no recorded result yet.
type TranscriptItem struct {
	Kind      TranscriptKind
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
	// Details is the structured tool result payload recorded in the log, for
	// example a delegate result carrying the per-task table.
	Details json.RawMessage
	// NoticeKind classifies a TranscriptNotice item; empty for other kinds.
	NoticeKind NoticeKind
	Status     StopReason
	Reason     string
	Model      string
	Dialect    string
	Duration   time.Duration
	Turns      int
	Usage      Usage
}

// Transcript reads the display transcript of any session under stateDir: a
// root session, a delegated child, or a fork. It reads a byte snapshot of the
// log without taking the session lock, so a running session stays readable, and
// it ignores a torn final line. An unusable ID is ErrInvalidSessionID and a
// missing log is ErrSessionNotFound.
func Transcript(ctx context.Context, stateDir, sessionID string) ([]TranscriptItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if stateDir == "" {
		return nil, &ConfigError{Field: "StateDir", Detail: "is required"}
	}
	return readTranscript(stateDir, sessionID)
}

// readTranscript is the lock-free reader behind Transcript and
// Agent.Transcript. It takes no context: it performs one bounded local read and
// never waits on a lock or a network.
func readTranscript(stateDir, sessionID string) ([]TranscriptItem, error) {
	id, err := session.CanonicalID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSessionID, err)
	}
	events, err := session.ReadSnapshot(session.SessionPath(stateDir, id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
		}
		return nil, err
	}
	items, err := agent.History(events)
	if err != nil {
		return nil, err
	}
	out := make([]TranscriptItem, 0, len(items))
	for _, item := range items {
		out = append(out, transcriptItem(item))
	}
	return out, nil
}

// transcriptItem copies one component transcript item across the facade
// boundary. Raw payloads are cloned so the caller cannot mutate the recorded
// bytes.
func transcriptItem(item agent.HistoryItem) TranscriptItem {
	return TranscriptItem{
		Kind:       TranscriptKind(item.Kind),
		Run:        item.Run,
		Turn:       item.Turn,
		Source:     item.Source,
		Text:       item.Text,
		CallID:     item.CallID,
		Name:       item.Name,
		Arguments:  append(json.RawMessage(nil), item.Arguments...),
		Pending:    item.Pending,
		OK:         item.OK,
		Code:       item.Code,
		Output:     item.Output,
		Error:      item.Error,
		ExitCode:   item.ExitCode,
		Truncated:  item.Truncated,
		TimedOut:   item.TimedOut,
		Details:    append(json.RawMessage(nil), item.Details...),
		NoticeKind: NoticeKind(item.NoticeKind),
		Status:     StopReason(item.Status),
		Reason:     item.Reason,
		Model:      item.Model,
		Dialect:    item.Dialect,
		Duration:   item.Duration,
		Turns:      item.Turns,
		Usage:      fromInternalUsage(item.Usage),
	}
}
