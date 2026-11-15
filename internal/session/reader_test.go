package session

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validData = `{}`

func makeEvent(seq uint64, typ EventType, data string) Event {
	return Event{
		V:       Version,
		Seq:     seq,
		TS:      time.Unix(0, 0).UTC(),
		Session: testSession,
		Type:    typ,
		Data:    json.RawMessage(data),
	}
}

func encodeEventLine(t *testing.T, ev Event) string {
	t.Helper()
	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)
	if err := jsonv2.MarshalEncode(enc, ev); err != nil {
		t.Fatalf("encode event: %v", err)
	}
	return buf.String()
}

func writeLogFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}
	return path
}

func trimLine(s string) []byte {
	return []byte(strings.TrimSuffix(s, "\n"))
}

func TestReadRoundTrip(t *testing.T) {
	first := makeEvent(1, TypeSessionStart, `{"model":"m"}`)
	second := makeEvent(2, TypeUserMessage, `{"text":"hi"}`)
	content := encodeEventLine(t, first) + encodeEventLine(t, second)
	path := writeLogFile(t, []byte(content))

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEventsEqual(t, got, []Event{first, second})
}

func TestReadHeadStopsAtLimit(t *testing.T) {
	first := makeEvent(1, TypeSessionStart, `{"model":"m"}`)
	second := makeEvent(2, TypeUserMessage, `{"text":"hi"}`)
	third := makeEvent(3, TypeModelResponse, `{}`)
	content := encodeEventLine(t, first) + encodeEventLine(t, second) + encodeEventLine(t, third)
	path := writeLogFile(t, []byte(content))

	got, err := ReadHead(path, 2)
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	assertEventsEqual(t, got, []Event{first, second})

	if _, err := ReadHead(path, 0); err == nil {
		t.Fatal("ReadHead accepted a zero limit")
	}
}

func TestReadHeadRejectsCorruptPrefix(t *testing.T) {
	first := encodeEventLine(t, makeEvent(1, TypeSessionStart, validData))
	bad := `{"v":2,"seq":3,"session":"` + testSession + `","type":"user_message","data":{}}`
	path := writeLogFile(t, []byte(first+bad+"\n"))
	if _, err := ReadHead(path, 4); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("ReadHead: got %v, want ErrCorrupt", err)
	}
}

func TestReadAcceptsFinalLineWithoutNewline(t *testing.T) {
	first := makeEvent(1, TypeSessionStart, validData)
	second := makeEvent(2, TypeUserMessage, `{"text":"hi"}`)
	content := encodeEventLine(t, first) + strings.TrimSuffix(encodeEventLine(t, second), "\n")
	path := writeLogFile(t, []byte(content))

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEventsEqual(t, got, []Event{first, second})
}

func TestReadToleratesTornFinalLine(t *testing.T) {
	first := makeEvent(1, TypeSessionStart, validData)
	firstLine := encodeEventLine(t, first)
	badUTF8 := append([]byte(`{"v":2,"seq":2,"session":"`+testSession+`","type":"user_message","data":"`), 0xff)
	badUTF8 = append(badUTF8, []byte(`"}`)...)
	cases := []struct {
		name string
		tail []byte
	}{
		{name: "partial object", tail: []byte(`{"v":2,"seq":2`)},
		{name: "partial data", tail: []byte(`{"v":2,"seq":2,"session":"` + testSession + `","type":"user_message","data":`)},
		{name: "invalid utf8", tail: badUTF8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := append([]byte(firstLine), tc.tail...)
			path := writeLogFile(t, content)
			got, err := Read(path)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			assertEventsEqual(t, got, []Event{first})
		})
	}
}

func TestReadRejectsDecodableInvalidFinalLine(t *testing.T) {
	first := makeEvent(1, TypeSessionStart, validData)
	cases := []struct {
		name string
		ev   Event
	}{
		{name: "wrong sequence", ev: makeEvent(5, TypeUserMessage, validData)},
		{name: "repeat sequence", ev: makeEvent(1, TypeUserMessage, validData)},
		{name: "other session", ev: Event{V: Version, Seq: 2, TS: time.Unix(0, 0).UTC(), Session: "other", Type: TypeUserMessage, Data: json.RawMessage(validData)}},
		{name: "empty session", ev: Event{V: Version, Seq: 2, TS: time.Unix(0, 0).UTC(), Type: TypeUserMessage, Data: json.RawMessage(validData)}},
		{name: "empty type", ev: Event{V: Version, Seq: 2, TS: time.Unix(0, 0).UTC(), Session: testSession, Data: json.RawMessage(validData)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte(encodeEventLine(t, first) + encodeEventLine(t, tc.ev))
			path := writeLogFile(t, content)
			if _, err := Read(path); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Read: got %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestReadRejectsInvalidNonFinalLine(t *testing.T) {
	valid1 := encodeEventLine(t, makeEvent(1, TypeSessionStart, validData))
	valid2 := encodeEventLine(t, makeEvent(2, TypeUserMessage, validData))
	nonFinal := func(bad []byte) []byte {
		out := append([]byte(valid1), bad...)
		out = append(out, '\n')
		return append(out, valid2...)
	}
	badType := encodeEventLine(t, makeEvent(2, TypeUserMessage, validData))
	badType = strings.Replace(badType, `"type":"user_message"`, `"type":""`, 1)
	otherSession := encodeEventLine(t, Event{
		V:       Version,
		Seq:     2,
		TS:      time.Unix(0, 0).UTC(),
		Session: "other",
		Type:    TypeUserMessage,
		Data:    json.RawMessage(validData),
	})
	cases := []struct {
		name    string
		content []byte
	}{
		{name: "malformed json", content: nonFinal([]byte(`{"v":2`))},
		{name: "duplicate envelope names", content: nonFinal([]byte(`{"v":2,"v":2,"seq":2,"session":"` + testSession + `","type":"user_message","data":{}}`))},
		{name: "empty type", content: nonFinal(trimLine(badType))},
		{name: "sequence starts at two", content: []byte(encodeEventLine(t, makeEvent(2, TypeSessionStart, validData)) + encodeEventLine(t, makeEvent(3, TypeUserMessage, validData)))},
		{name: "sequence gap", content: nonFinal(trimLine(encodeEventLine(t, makeEvent(3, TypeUserMessage, validData))))},
		{name: "sequence repeat", content: nonFinal(trimLine(encodeEventLine(t, makeEvent(1, TypeUserMessage, validData))))},
		{name: "session mismatch", content: nonFinal(trimLine(otherSession))},
		{name: "duplicate data names", content: nonFinal([]byte(`{"v":2,"seq":2,"session":"` + testSession + `","type":"user_message","data":{"a":1,"a":2}}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeLogFile(t, tc.content)
			if _, err := Read(path); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Read: got %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestReadRejectsSchemaVersions(t *testing.T) {
	v0 := `{"v":1,"seq":1,"ts":"1970-01-01T00:00:00Z","session":"` + testSession + `","type":"session_start","data":{}}` + "\n"
	unknown := `{"v":3,"seq":1,"ts":"1970-01-01T00:00:00Z","session":"` + testSession + `","type":"session_start","data":{}}` + "\n"
	mixed := encodeEventLine(t, makeEvent(1, TypeSessionStart, validData)) + v0
	for _, content := range []string{v0, unknown, mixed} {
		path := writeLogFile(t, []byte(content))
		if _, err := Read(path); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("Read: got %v, want ErrUnsupportedVersion", err)
		}
	}
}

func TestReadOversizedLines(t *testing.T) {
	first := encodeEventLine(t, makeEvent(1, TypeSessionStart, validData))
	third := encodeEventLine(t, makeEvent(3, TypeUserMessage, validData))
	huge := strings.Repeat("x", maxEventLine+2)
	hugeLine := `{"v":2,"seq":2,"session":"` + testSession + `","type":"user_message","data":"` + huge + `"}`

	t.Run("non-final", func(t *testing.T) {
		content := []byte(first + hugeLine + "\n" + third)
		path := writeLogFile(t, content)
		if _, err := Read(path); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("Read: got %v, want ErrCorrupt", err)
		}
	})

	t.Run("final tolerated", func(t *testing.T) {
		content := []byte(first + hugeLine)
		path := writeLogFile(t, content)
		got, err := Read(path)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		assertEventsEqual(t, got, []Event{makeEvent(1, TypeSessionStart, validData)})
	})
}

func TestReadEmptyLog(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
	}{
		{name: "empty", content: nil},
		{name: "newline only", content: []byte("\n")},
		{name: "invalid final only", content: []byte("not json")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeLogFile(t, tc.content)
			got, err := Read(path)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("got %d events, want 0", len(got))
			}
		})
	}
}

func TestReadDoesNotRewrite(t *testing.T) {
	content := []byte(encodeEventLine(t, makeEvent(1, TypeSessionStart, validData)) + `{"v":2,"seq":2`)
	path := writeLogFile(t, content)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	if _, err := Read(path); err != nil {
		t.Fatalf("Read: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("Read changed the log: before %q, after %q", before, after)
	}
}

func TestReadIgnoresUnknownFieldsAndEvents(t *testing.T) {
	line := `{"v":2,"seq":1,"ts":"1970-01-01T00:00:00Z","session":"` + testSession +
		`","type":"future_event","data":{},"future":{"x":1}}`
	path := writeLogFile(t, []byte(line+"\n"))
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Type != "future_event" {
		t.Fatalf("Read = %+v", got)
	}
}
