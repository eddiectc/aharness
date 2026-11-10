// Package session defines the append-only JSONL session log: the event
// envelope, durable store, and replay reader.
package session

import (
	"encoding/json"
	"fmt"
	"time"
	"uuid"

	jsonv2 "encoding/json/v2"
)

// Version is the v0 session schema version.
const Version = 1

// EventType identifies an event payload.
type EventType string

const (
	TypeSessionStart  EventType = "session_start"
	TypeUserMessage   EventType = "user_message"
	TypeModelRequest  EventType = "model_request"
	TypeModelResponse EventType = "model_response"
	TypeModelRetry    EventType = "model_retry"
	TypeToolResult    EventType = "tool_result"
	TypeCompaction    EventType = "compaction"
	TypeLimit         EventType = "limit"
	TypeError         EventType = "error"
	TypeFinal         EventType = "final"
	TypeSessionEnd    EventType = "session_end"
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
