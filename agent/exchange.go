package agent

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"

	"github.com/eddiectc/aharness/session"
)

// ModelExchange is the latest projected model attempt in one session log: the
// newest recorded model_request or model_response, whichever is later, plus
// its pair when one exists in the same run and turn.
//
// Raw payloads are the recorded event JSON (the SDK's projection), not wire
// bytes: HTTP headers, auth, and exact request/response frames are never
// recorded. They are carried as json.RawMessage to match the package's other
// recorded-JSON fields (HistoryItem.Arguments, customToolData.Parameters);
// nothing here encodes or decodes, and validity is checked with jsontext.
type ModelExchange struct {
	// Request and Response are the recorded payloads of each side. They are
	// nil, not empty, when the side is absent. They are copies: the caller may
	// mutate them without touching the event log.
	Request  json.RawMessage
	Response json.RawMessage
	// RequestSeq and ResponseSeq are the recorded sequence numbers of the
	// events the payloads came from. Zero means that side is absent.
	RequestSeq  uint64
	ResponseSeq uint64
	// Run is the count of run_start events up to and including the selected
	// event. Zero means the log has no run_start at or before it (malformed or
	// very old); hosts render it as unknown. It is never synthesized from
	// ordering.
	Run int
	// Turn is the envelope Turn of the selected event. A returned pair always
	// shares one Turn.
	Turn int
	// HasRequest and HasResponse report which sides were found. Both are false
	// only for the zero value returned by an empty scan.
	HasRequest  bool
	HasResponse bool
}

// LatestModelExchange returns the latest projected model attempt in events: the
// newest model_request or model_response, whichever is later, and its pair when
// one exists in the same turn without an intervening run or session boundary.
//
// Only model_request and model_response participate in a decision. Every other
// type is transparent, so a model_retry between a request and its response does
// not break the pair. A newest request is the in-flight or terminally failed
// case: it is returned alone, never paired with an older exchange and never
// given a synthetic response. A newest response is paired only with the first
// preceding model_request when that request has the same Turn and no
// run_start, session_start, or model_response sits between them.
//
// An empty log, or one with no model events, returns the zero value and a nil
// error. A returned payload that is not a JSON object (empty, invalid, or a
// bare JSON value) is a corrupt-log error wrapping session.ErrCorrupt; a model
// event that is not returned is never validated. Envelope integrity remains
// the session reader's job.
//
// On a fork the newest attempt may be an event copied from the parent, because
// a fork copies every parent event except session_start; Run then counts the
// parent's runs too. Hosts should render Run as recorded rather than assume it
// belongs to the current process.
//
// This is a component-level reader for first-party hosts. The aharness facade
// has no exchange DTO, because handing raw payload bytes across the facade
// boundary would alias a component type; a facade host reads the log with
// session.ReadSnapshot and calls this function directly.
func LatestModelExchange(events []session.Event) (ModelExchange, error) {
	selected := -1
	var (
		runCount    int
		selectedRun int
	)
	for i, ev := range events {
		switch ev.Type {
		case session.TypeRunStart:
			runCount++
		case session.TypeModelRequest, session.TypeModelResponse:
			selected = i
			selectedRun = runCount
		}
	}
	if selected < 0 {
		return ModelExchange{}, nil
	}

	selectedEvent := events[selected]
	exchange := ModelExchange{Run: selectedRun, Turn: selectedEvent.Turn}
	payload, err := exchangePayload(selectedEvent)
	if err != nil {
		return ModelExchange{}, err
	}
	if selectedEvent.Type == session.TypeModelRequest {
		exchange.Request = payload
		exchange.RequestSeq = selectedEvent.Seq
		exchange.HasRequest = true
		return exchange, nil
	}
	exchange.Response = payload
	exchange.ResponseSeq = selectedEvent.Seq
	exchange.HasResponse = true

	// Walk back to the first model_request. A previous response, run_start, or
	// session_start ends the scan without pairing, so a request never pairs
	// across a turn, run, or session boundary.
	for i := selected - 1; i >= 0; i-- {
		previous := events[i]
		switch previous.Type {
		case session.TypeModelRequest:
			if previous.Turn != selectedEvent.Turn {
				return exchange, nil
			}
			request, err := exchangePayload(previous)
			if err != nil {
				return ModelExchange{}, err
			}
			exchange.Request = request
			exchange.RequestSeq = previous.Seq
			exchange.HasRequest = true
			return exchange, nil
		case session.TypeModelResponse, session.TypeRunStart, session.TypeSessionStart:
			return exchange, nil
		}
	}
	return exchange, nil
}

// exchangePayload validates and copies one recorded model payload for return.
func exchangePayload(ev session.Event) (json.RawMessage, error) {
	if isJSONObject(ev.Data) {
		return bytes.Clone(ev.Data), nil
	}
	return nil, fmt.Errorf("%w: %s seq %d: payload is not a JSON object", session.ErrCorrupt, ev.Type, ev.Seq)
}

// isJSONObject reports whether raw is a syntactically valid JSON object under
// the same json/v2 rules the session reader applies.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{' && jsontext.Value(raw).IsValid()
}
