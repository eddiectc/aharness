package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"github.com/eddiectc/aharness/session"
)

// The reader never decodes a payload, so the tests use the recorded spelling
// and compare it byte for byte with what the event carried.
const (
	exchangeRequestPayload  = `{"model":"test-model","message_count":1}`
	exchangeResponsePayload = `{"text":"done","finish_reason":"stop"}`
)

// exchangeEvent builds one constructed event with an explicit envelope and raw
// payload. It is not proof the reader accepts the data: the empty-payload row
// is constructed-only because session.reader rejects invalid JSON first.
func exchangeEvent(typ session.EventType, turn int, seq uint64, data string) session.Event {
	return session.Event{V: session.Version, Type: typ, Turn: turn, Seq: seq, Data: []byte(data)}
}

func exchangeRequestEvent(turn int, seq uint64) session.Event {
	return exchangeEvent(session.TypeModelRequest, turn, seq, exchangeRequestPayload)
}

func exchangeResponseEvent(turn int, seq uint64) session.Event {
	return exchangeEvent(session.TypeModelResponse, turn, seq, exchangeResponsePayload)
}

func exchangeOtherEvent(typ session.EventType, turn int, seq uint64) session.Event {
	return exchangeEvent(typ, turn, seq, `{"note":"transparent"}`)
}

func wantExchangeRequest(seq uint64, run, turn int) ModelExchange {
	return ModelExchange{
		Request:    json.RawMessage(exchangeRequestPayload),
		RequestSeq: seq,
		Run:        run,
		Turn:       turn,
		HasRequest: true,
	}
}

func wantExchangeResponse(seq uint64, run, turn int) ModelExchange {
	return ModelExchange{
		Response:    json.RawMessage(exchangeResponsePayload),
		ResponseSeq: seq,
		Run:         run,
		Turn:        turn,
		HasResponse: true,
	}
}

func wantExchangePair(requestSeq, responseSeq uint64, run, turn int) ModelExchange {
	return ModelExchange{
		Request:     json.RawMessage(exchangeRequestPayload),
		Response:    json.RawMessage(exchangeResponsePayload),
		RequestSeq:  requestSeq,
		ResponseSeq: responseSeq,
		Run:         run,
		Turn:        turn,
		HasRequest:  true,
		HasResponse: true,
	}
}

func describeExchange(ex ModelExchange) string {
	payload := func(raw json.RawMessage) string {
		if raw == nil {
			return "nil"
		}
		return strconv.Quote(string(raw))
	}
	return fmt.Sprintf("request=%s request_seq=%d response=%s response_seq=%d run=%d turn=%d has_request=%t has_response=%t",
		payload(ex.Request), ex.RequestSeq, payload(ex.Response), ex.ResponseSeq, ex.Run, ex.Turn, ex.HasRequest, ex.HasResponse)
}

func TestLatestModelExchange(t *testing.T) {
	tests := []struct {
		name    string
		events  []session.Event
		want    ModelExchange
		wantErr bool
	}{
		{
			name: "newest response with a matching request",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeRequestEvent(1, 2),
				exchangeResponseEvent(1, 3),
			},
			want: wantExchangePair(2, 3, 1, 1),
		},
		{
			name: "newest response with no request logged in its turn",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeRequestEvent(1, 2),
				exchangeResponseEvent(1, 3),
				exchangeOtherEvent(session.TypeUserMessage, 2, 4),
				exchangeResponseEvent(2, 5),
			},
			want: wantExchangeResponse(5, 1, 2),
		},
		{
			name: "newest event is an in-flight request",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeRequestEvent(1, 2),
				exchangeResponseEvent(1, 3),
				exchangeRequestEvent(2, 4),
			},
			want: wantExchangeRequest(4, 1, 2),
		},
		{
			name: "newest request failed and the run ended",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeRequestEvent(1, 2),
				exchangeOtherEvent(session.TypeError, 1, 3),
				exchangeOtherEvent(session.TypeRunEnd, 1, 4),
			},
			want: wantExchangeRequest(2, 1, 1),
		},
		{
			name: "retry between request and response",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeRequestEvent(1, 2),
				exchangeOtherEvent(session.TypeModelRetry, 1, 3),
				exchangeOtherEvent(session.TypeModelRetry, 1, 4),
				exchangeResponseEvent(1, 5),
			},
			want: wantExchangePair(2, 5, 1, 1),
		},
		{
			name: "compaction between turns",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeRequestEvent(1, 2),
				exchangeResponseEvent(1, 3),
				exchangeOtherEvent(session.TypeCompaction, 1, 4),
				exchangeRequestEvent(2, 5),
				exchangeResponseEvent(2, 6),
			},
			want: wantExchangePair(5, 6, 1, 2),
		},
		{
			name: "pairing does not cross a resumed run start",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeRequestEvent(1, 2),
				exchangeOtherEvent(session.TypeRunStart, 0, 3),
				exchangeResponseEvent(2, 4),
			},
			want: wantExchangeResponse(4, 2, 2),
		},
		{
			name: "earlier run logged a request and the later run did not",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeRequestEvent(1, 2),
				exchangeResponseEvent(1, 3),
				exchangeOtherEvent(session.TypeRunEnd, 1, 4),
				exchangeOtherEvent(session.TypeRunStart, 0, 5),
				exchangeResponseEvent(2, 6),
			},
			want: wantExchangeResponse(6, 2, 2),
		},
		{
			name: "request and response have different turn envelopes",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeRequestEvent(1, 2),
				exchangeResponseEvent(2, 3),
			},
			want: wantExchangeResponse(3, 1, 2),
		},
		{
			name: "two consecutive requests return the later one unpaired",
			events: []session.Event{
				exchangeRequestEvent(1, 1),
				exchangeRequestEvent(2, 2),
			},
			want: wantExchangeRequest(2, 0, 2),
		},
		{
			name:   "empty log",
			events: nil,
			want:   ModelExchange{},
		},
		{
			name: "session start only",
			events: []session.Event{
				exchangeOtherEvent(session.TypeSessionStart, 0, 1),
				exchangeOtherEvent(session.TypeFinal, 0, 2),
			},
			want: ModelExchange{},
		},
		{
			name:    "selected payload is null",
			events:  []session.Event{exchangeEvent(session.TypeModelResponse, 1, 1, `null`)},
			wantErr: true,
		},
		{
			name:    "selected payload is a bare string",
			events:  []session.Event{exchangeEvent(session.TypeModelRequest, 1, 1, `"text"`)},
			wantErr: true,
		},
		{
			name:    "selected payload is an array",
			events:  []session.Event{exchangeEvent(session.TypeModelResponse, 1, 1, `[1,2]`)},
			wantErr: true,
		},
		{
			name:    "selected payload is invalid JSON",
			events:  []session.Event{exchangeEvent(session.TypeModelResponse, 1, 1, `{"text":`)},
			wantErr: true,
		},
		{
			name:    "selected payload is whitespace only",
			events:  []session.Event{exchangeEvent(session.TypeModelResponse, 1, 1, " \n\t")},
			wantErr: true,
		},
		{
			name:    "selected payload is empty (constructed only)",
			events:  []session.Event{exchangeEvent(session.TypeModelRequest, 1, 1, ``)},
			wantErr: true,
		},
		{
			name: "unselected malformed model event is never validated",
			events: []session.Event{
				exchangeEvent(session.TypeModelRequest, 1, 1, `null`),
				exchangeResponseEvent(2, 2),
			},
			want: wantExchangeResponse(2, 0, 2),
		},
		{
			name:   "run is zero without a run start",
			events: []session.Event{exchangeResponseEvent(1, 1)},
			want:   wantExchangeResponse(1, 0, 1),
		},
		{
			name: "run counts one run start",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeResponseEvent(1, 2),
			},
			want: wantExchangeResponse(2, 1, 1),
		},
		{
			name: "run counts two run starts",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeOtherEvent(session.TypeRunStart, 0, 2),
				exchangeResponseEvent(1, 3),
			},
			want: wantExchangeResponse(3, 2, 1),
		},
		{
			name: "run counts three run starts",
			events: []session.Event{
				exchangeOtherEvent(session.TypeRunStart, 0, 1),
				exchangeOtherEvent(session.TypeRunStart, 0, 2),
				exchangeOtherEvent(session.TypeRunStart, 0, 3),
				exchangeResponseEvent(1, 4),
			},
			want: wantExchangeResponse(4, 3, 1),
		},
		{
			name: "run start after the selected event does not count",
			events: []session.Event{
				exchangeResponseEvent(1, 1),
				exchangeOtherEvent(session.TypeRunStart, 0, 2),
			},
			want: wantExchangeResponse(1, 0, 1),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LatestModelExchange(tc.events)
			if tc.wantErr {
				if !errors.Is(err, session.ErrCorrupt) {
					t.Fatalf("LatestModelExchange error = %v, want a session.ErrCorrupt cause", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LatestModelExchange: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("LatestModelExchange =\n got %s\nwant %s", describeExchange(got), describeExchange(tc.want))
			}
		})
	}
}

func TestLatestModelExchangeCopiesPayloads(t *testing.T) {
	events := []session.Event{
		exchangeOtherEvent(session.TypeRunStart, 0, 1),
		exchangeRequestEvent(1, 2),
		exchangeResponseEvent(1, 3),
	}
	exchange, err := LatestModelExchange(events)
	if err != nil {
		t.Fatalf("LatestModelExchange: %v", err)
	}
	exchange.Request[0] = 'X'
	exchange.Response[0] = 'X'
	if string(events[1].Data) != exchangeRequestPayload {
		t.Errorf("mutating the request changed the event: %q", events[1].Data)
	}
	if string(events[2].Data) != exchangeResponsePayload {
		t.Errorf("mutating the response changed the event: %q", events[2].Data)
	}
}

// TestLatestModelExchangeOnFork pins the documented fork behavior: a fork
// copies every parent event except session_start, so a fresh fork reports the
// parent's last exchange and the inherited run count.
func TestLatestModelExchangeOnFork(t *testing.T) {
	stateDir := t.TempDir()
	parent, err := session.CreateNew(stateDir)
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	appendParent := func(ev session.Event) {
		t.Helper()
		if err := parent.Append(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Type, err)
		}
	}
	if err := WriteSessionStart(parent, testConfig(), nil); err != nil {
		t.Fatalf("write parent session_start: %v", err)
	}
	appendParent(exchangeOtherEvent(session.TypeRunStart, 0, 0))
	appendParent(exchangeRequestEvent(1, 0))
	appendParent(exchangeResponseEvent(1, 0))
	appendParent(exchangeOtherEvent(session.TypeRunEnd, 1, 0))
	appendParent(exchangeOtherEvent(session.TypeRunStart, 0, 0))
	appendParent(exchangeRequestEvent(2, 0))
	appendParent(exchangeResponseEvent(2, 0))
	if err := parent.Sync(); err != nil {
		t.Fatalf("sync parent: %v", err)
	}
	parentID := parent.ID()
	if err := parent.Close(); err != nil {
		t.Fatalf("close parent: %v", err)
	}

	source, err := session.OpenShared(stateDir, parentID)
	if err != nil {
		t.Fatalf("open parent for fork: %v", err)
	}
	defer func() { _ = source.Close() }()
	fork, err := session.CreateNew(stateDir)
	if err != nil {
		t.Fatalf("create fork session: %v", err)
	}
	if err := WriteSessionStart(fork, testConfig(), &ParentRef{Session: parentID, Seq: source.LastSeq()}); err != nil {
		t.Fatalf("write fork session_start: %v", err)
	}
	if err := fork.CopyHistory(source.Events()); err != nil {
		t.Fatalf("copy parent history: %v", err)
	}
	if err := fork.Sync(); err != nil {
		t.Fatalf("sync fork: %v", err)
	}
	if err := fork.Close(); err != nil {
		t.Fatalf("close fork: %v", err)
	}

	forkEvents, err := session.Read(session.SessionPath(stateDir, fork.ID()))
	if err != nil {
		t.Fatalf("read fork: %v", err)
	}
	exchange, err := LatestModelExchange(forkEvents)
	if err != nil {
		t.Fatalf("LatestModelExchange on fork: %v", err)
	}
	// The fork records its own session_start first, then copies the parent's
	// seven events: run_start, request, response, run_end, run_start, request,
	// response. The copied payloads are renumbered to seq 3/4 and 7/8.
	want := wantExchangePair(7, 8, 2, 2)
	if !reflect.DeepEqual(exchange, want) {
		t.Fatalf("fork exchange =\n got %s\nwant %s", describeExchange(exchange), describeExchange(want))
	}
}
