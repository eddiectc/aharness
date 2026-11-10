package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"uuid"
)

const testSession = "0190a0c4-0000-7000-8000-000000000001"

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewStore(t.TempDir(), testSession)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Store.Close: %v", err)
		}
	})
	return st
}

func checkPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("mode of %s: got %04o, want %04o", path, got, want)
	}
}

func assertEventsEqual(t *testing.T, got, want []Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: got %+v, want %+v", len(got), len(want), got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.V != w.V {
			t.Errorf("event %d: got V %d, want %d", i, g.V, w.V)
		}
		if g.Seq != w.Seq {
			t.Errorf("event %d: got Seq %d, want %d", i, g.Seq, w.Seq)
		}
		if !g.TS.Equal(w.TS) {
			t.Errorf("event %d: got TS %v, want %v", i, g.TS, w.TS)
		}
		if g.Session != w.Session {
			t.Errorf("event %d: got Session %q, want %q", i, g.Session, w.Session)
		}
		if g.Turn != w.Turn {
			t.Errorf("event %d: got Turn %d, want %d", i, g.Turn, w.Turn)
		}
		if g.Type != w.Type {
			t.Errorf("event %d: got Type %q, want %q", i, g.Type, w.Type)
		}
		if g.Role != w.Role {
			t.Errorf("event %d: got Role %q, want %q", i, g.Role, w.Role)
		}
		if !bytes.Equal(g.Data, w.Data) {
			t.Errorf("event %d: got Data %s, want %s", i, string(g.Data), string(w.Data))
		}
	}
}

func TestNewStoreCreatesDirectoryAndFile(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	st, err := NewStore(stateDir, testSession)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Store.Close: %v", err)
		}
	}()

	wantPath := filepath.Join(stateDir, "sessions", testSession+".jsonl")
	if got := st.Path(); got != wantPath {
		t.Errorf("Path: got %q, want %q", got, wantPath)
	}
	if got := SessionPath(stateDir, testSession); got != wantPath {
		t.Errorf("SessionPath: got %q, want %q", got, wantPath)
	}
	checkPerm(t, stateDir, 0o700)
	checkPerm(t, filepath.Join(stateDir, "sessions"), 0o700)
	checkPerm(t, st.Path(), 0o600)
}

func TestNewStoreRejectsExistingSession(t *testing.T) {
	stateDir := t.TempDir()
	st, err := NewStore(stateDir, testSession)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Store.Close: %v", err)
		}
	}()
	if _, err := NewStore(stateDir, testSession); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second NewStore: got %v, want os.ErrExist", err)
	}
}

func TestAppendAssignsEnvelope(t *testing.T) {
	st := openTestStore(t)
	in := Event{
		V:       99,
		Seq:     42,
		TS:      time.Unix(1, 0).UTC(),
		Session: "other",
		Turn:    3,
		Type:    TypeToolResult,
		Role:    "assistant",
		Data:    json.RawMessage(`{"ok":true}`),
	}
	if err := st.Append(in); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if in.V != 99 || in.Seq != 42 || in.Session != "other" {
		t.Errorf("Append mutated caller event: %+v", in)
	}
	events := st.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	got := events[0]
	if got.V != Version {
		t.Errorf("V: got %d, want %d", got.V, Version)
	}
	if got.Seq != 1 {
		t.Errorf("Seq: got %d, want 1", got.Seq)
	}
	if got.TS.IsZero() {
		t.Error("TS was not assigned")
	}
	if got.TS.Location() != time.UTC {
		t.Errorf("TS location: got %v, want UTC", got.TS.Location())
	}
	if got.Session != testSession {
		t.Errorf("Session: got %q, want %q", got.Session, testSession)
	}
	if got.Turn != 3 || got.Type != TypeToolResult || got.Role != "assistant" {
		t.Errorf("producer fields changed: %+v", got)
	}
	if string(got.Data) != `{"ok":true}` {
		t.Errorf("Data: got %s, want %s", string(got.Data), `{"ok":true}`)
	}

	if err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("second Append: %v", err)
	}
	if seq := st.Events()[1].Seq; seq != 2 {
		t.Errorf("second Seq: got %d, want 2", seq)
	}
}

func TestAppendRejectsInvalidData(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{name: "invalid utf8", data: []byte{'"', 0xff, '"'}},
		{name: "duplicate names", data: []byte(`{"a":1,"a":2}`)},
		{name: "empty data", data: nil},
		{name: "torn json", data: []byte(`{"a":`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openTestStore(t)
			err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(tc.data)})
			if !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Append: got %v, want ErrInvalidEvent", err)
			}
			if got := len(st.Events()); got != 0 {
				t.Fatalf("got %d events after rejected append, want 0", got)
			}
			info, err := os.Stat(st.Path())
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if size := info.Size(); size != 0 {
				t.Fatalf("log size after rejected append: got %d, want 0", size)
			}

			// A rejected event must not consume a sequence number.
			if err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(`{}`)}); err != nil {
				t.Fatalf("Append after rejection: %v", err)
			}
			if seq := st.Events()[0].Seq; seq != 1 {
				t.Errorf("Seq after recovered append: got %d, want 1", seq)
			}
		})
	}
}

func TestAppendOnly(t *testing.T) {
	st := openTestStore(t)
	if err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(`{"n":1}`)}); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	first, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatalf("read after first append: %v", err)
	}
	if err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(`{"n":2}`)}); err != nil {
		t.Fatalf("second Append: %v", err)
	}
	second, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatalf("read after second append: %v", err)
	}
	if !bytes.HasPrefix(second, first) {
		t.Errorf("existing bytes changed: first %q, second %q", first, second)
	}
	if bytes.Equal(first, second) {
		t.Error("second append did not extend the log")
	}
	if lines := bytes.Count(second, []byte("\n")); lines != 2 {
		t.Errorf("newline count: got %d, want 2", lines)
	}
}

func TestStoreSyncDurability(t *testing.T) {
	stateDir := t.TempDir()
	st, err := NewStore(stateDir, testSession)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for i := range 3 {
		data := json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))
		if err := st.Append(Event{Type: TypeToolResult, Turn: i, Data: data}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	want := st.Events()
	if err := st.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := Read(st.Path())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEventsEqual(t, got, want)
}

func TestStoreCloseIdempotent(t *testing.T) {
	st := openTestStore(t)
	if err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(`{}`)}); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("Append after Close: got %v, want ErrStoreClosed", err)
	}
	if err := st.Sync(); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("Sync after Close: got %v, want ErrStoreClosed", err)
	}
	if got := len(st.Events()); got != 1 {
		t.Errorf("Events after Close: got %d, want 1", got)
	}
}

func TestStoreEventsReturnsCopy(t *testing.T) {
	st := openTestStore(t)
	data := []byte(`{"x":1}`)
	if err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(data)}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	data[0] = '['
	first := st.Events()
	if got := string(first[0].Data); got != `{"x":1}` {
		t.Errorf("store retained caller data: got %s, want %s", got, `{"x":1}`)
	}
	first[0].Seq = 99
	first[0].Data[0] = '['
	second := st.Events()
	if got := second[0].Seq; got != 1 {
		t.Errorf("stored Seq changed through copy: got %d, want 1", got)
	}
	if got := string(second[0].Data); got != `{"x":1}` {
		t.Errorf("stored Data changed through copy: got %s, want %s", got, `{"x":1}`)
	}
}

func TestNewIDIsUUIDv7(t *testing.T) {
	id := NewID()
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("uuid.Parse(%q): %v", id, err)
	}
	if got := parsed[6] >> 4; got != 7 {
		t.Errorf("version: got %d, want 7", got)
	}
	if got := parsed[8] >> 6; got != 2 {
		t.Errorf("variant: got %d, want 2", got)
	}
	if got := parsed.String(); got != id {
		t.Errorf("canonical form: got %q, want %q", got, id)
	}
}
