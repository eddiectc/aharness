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

func TestReadToleratesInvalidFinalLine(t *testing.T) {
	first := makeEvent(1, TypeSessionStart, validData)
	firstLine := encodeEventLine(t, first)
	badUTF8 := append([]byte(`{"v":1,"seq":2,"session":"`+testSession+`","type":"user_message","data":"`), 0xff)
	badUTF8 = append(badUTF8, []byte(`"}`)...)
	emptyType := encodeEventLine(t, Event{
		V:       Version,
		Seq:     2,
		TS:      time.Unix(0, 0).UTC(),
		Session: testSession,
		Type:    "",
		Data:    json.RawMessage(validData),
	})
	cases := []struct {
		name string
		tail []byte
	}{
		{name: "partial object", tail: []byte(`{"v":1,"seq":2`)},
		{name: "partial data", tail: []byte(`{"v":1,"seq":2,"session":"` + testSession + `","type":"user_message","data":`)},
		{name: "invalid utf8", tail: badUTF8},
		{name: "wrong sequence", tail: trimLine(encodeEventLine(t, makeEvent(5, TypeUserMessage, validData)))},
		{name: "empty type", tail: trimLine(emptyType)},
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

func TestReadRejectsInvalidNonFinalLine(t *testing.T) {
	valid1 := encodeEventLine(t, makeEvent(1, TypeSessionStart, validData))
	valid2 := encodeEventLine(t, makeEvent(2, TypeUserMessage, validData))
	nonFinal := func(bad []byte) []byte {
		out := append([]byte(valid1), bad...)
		out = append(out, '\n')
		return append(out, valid2...)
	}
	badVersion := encodeEventLine(t, Event{
		V:       2,
		Seq:     2,
		TS:      time.Unix(0, 0).UTC(),
		Session: testSession,
		Type:    TypeUserMessage,
		Data:    json.RawMessage(validData),
	})
	badType := encodeEventLine(t, Event{
		V:       Version,
		Seq:     2,
		TS:      time.Unix(0, 0).UTC(),
		Session: testSession,
		Type:    "",
		Data:    json.RawMessage(validData),
	})
	otherSession := encodeEventLine(t, Event{
		V:       Version,
		Seq:     2,
		TS:      time.Unix(0, 0).UTC(),
		Session: "other",
		Type:    TypeUserMessage,
		Data:    json.RawMessage(validData),
	})
	emptySession := encodeEventLine(t, Event{
		V:       Version,
		Seq:     2,
		TS:      time.Unix(0, 0).UTC(),
		Session: "",
		Type:    TypeUserMessage,
		Data:    json.RawMessage(validData),
	})
	badUTF8 := append([]byte(`{"v":1,"seq":2,"session":"`+testSession+`","type":"user_message","data":"`), 0xff)
	badUTF8 = append(badUTF8, []byte(`"}`)...)
	cases := []struct {
		name    string
		content []byte
	}{
		{name: "malformed json", content: nonFinal([]byte(`{"v":1`))},
		{name: "duplicate envelope names", content: nonFinal([]byte(`{"v":1,"v":1,"seq":2,"session":"` + testSession + `","type":"user_message","data":{}}`))},
		{name: "invalid utf8", content: nonFinal(badUTF8)},
		{name: "wrong version", content: nonFinal(trimLine(badVersion))},
		{name: "empty type", content: nonFinal(trimLine(badType))},
		{name: "sequence starts at two", content: []byte(encodeEventLine(t, makeEvent(2, TypeSessionStart, validData)) + encodeEventLine(t, makeEvent(3, TypeUserMessage, validData)))},
		{name: "sequence gap", content: nonFinal(trimLine(encodeEventLine(t, makeEvent(3, TypeUserMessage, validData))))},
		{name: "sequence repeat", content: nonFinal(trimLine(encodeEventLine(t, makeEvent(1, TypeUserMessage, validData))))},
		{name: "session mismatch", content: nonFinal(trimLine(otherSession))},
		{name: "empty session", content: nonFinal(trimLine(emptySession))},
		{name: "duplicate data names", content: nonFinal([]byte(`{"v":1,"seq":2,"session":"` + testSession + `","type":"user_message","data":{"a":1,"a":2}}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeLogFile(t, tc.content)
			if _, err := Read(path); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Read: got %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestReadOversizedLines(t *testing.T) {
	first := makeEvent(1, TypeSessionStart, validData)
	firstLine := encodeEventLine(t, first)
	third := makeEvent(3, TypeUserMessage, validData)
	huge := strings.Repeat("x", maxEventLine+2)
	hugeLine := `{"v":1,"seq":2,"session":"` + testSession + `","type":"user_message","data":"` + huge + `"}`

	t.Run("non-final", func(t *testing.T) {
		content := []byte(firstLine + hugeLine + "\n" + encodeEventLine(t, third))
		path := writeLogFile(t, content)
		if _, err := Read(path); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("Read: got %v, want ErrInvalidEvent", err)
		}
	})

	t.Run("final tolerated", func(t *testing.T) {
		content := []byte(firstLine + hugeLine)
		path := writeLogFile(t, content)
		got, err := Read(path)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		assertEventsEqual(t, got, []Event{first})
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
	content := []byte(encodeEventLine(t, makeEvent(1, TypeSessionStart, validData)) + `{"v":1,"seq":2`)
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

func TestReadIgnoresUnknownFields(t *testing.T) {
	line := `{"v":1,"seq":1,"ts":"1970-01-01T00:00:00Z","session":"` + testSession +
		`","type":"user_message","data":{},"future":{"x":1}}`
	path := writeLogFile(t, []byte(line))
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEventsEqual(t, got, []Event{makeEvent(1, TypeUserMessage, validData)})
}
