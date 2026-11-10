// Package session defines the append-only JSONL session log: the event
// envelope, durable store, locking, repair, replay reader, and fork copy.
package session

import (
	"encoding/json"
	"fmt"
	"time"
	"uuid"

	jsonv2 "encoding/json/v2"
)

// Version is the v1 session schema version. The reader accepts only this
// version; v0 logs (v=1) are rejected without migration.
const Version = 2

// EventType identifies an event payload.
type EventType string

const (
	TypeSessionStart  EventType = "session_start"
	TypeRunStart      EventType = "run_start"
	TypeUserMessage   EventType = "user_message"
	TypeModelRequest  EventType = "model_request"
	TypeModelResponse EventType = "model_response"
	TypeModelRetry    EventType = "model_retry"
	TypeToolResult    EventType = "tool_result"
	TypeCompaction    EventType = "compaction"
	TypeLimit         EventType = "limit"
	TypeInterrupt     EventType = "interrupt"
	TypeError         EventType = "error"
	TypeSessionRepair EventType = "session_repair"
	TypeFinal         EventType = "final"
	TypeRunEnd        EventType = "run_end"
)

// Event is the common JSONL envelope. Store.Append assigns V, Seq, TS, and
// Session; callers leave them zero. Data is owned by the event producer.
type Event struct {
	V       int             `json:"v"`
	Seq     uint64          `json:"seq"`
	TS      time.Time       `json:"ts"`
	Session string          `json:"session"`
	Turn    int             `json:"turn,omitzero"`
	Type    EventType       `json:"type"`
	Role    string          `json:"role,omitzero"`
	Data    json.RawMessage `json:"data"`
}

// RepairData is the session_repair payload written when a torn final line is
// truncated before appending a new run.
type RepairData struct {
	DiscardedBytes int64  `json:"discarded_bytes"`
	LastGoodSeq    uint64 `json:"last_good_seq"`
	Reason         string `json:"reason"`
}

// NewEvent marshals a producer payload into a session event.
func NewEvent(typ EventType, turn int, role string, payload any) (Event, error) {
	data, err := jsonv2.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal %s payload: %w", typ, err)
	}
	return Event{Turn: turn, Type: typ, Role: role, Data: data}, nil
}

// NewID returns a new UUIDv7 session identifier.
func NewID() string { return uuid.NewV7().String() }

// CanonicalID validates a UUID session identifier and returns its canonical
// hyphenated form. Session IDs are validated before any path is built.
func CanonicalID(id string) (string, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return "", fmt.Errorf("invalid session id %q: %w", id, err)
	}
	return u.String(), nil
}
