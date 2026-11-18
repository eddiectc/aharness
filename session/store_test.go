package session

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
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
	st, err := Create(t.TempDir(), testSession)
	if err != nil {
		t.Fatalf("Create: %v", err)
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

func testEvent(seq uint64, typ EventType, data string) Event {
	return Event{
		V:       Version,
		Seq:     seq,
		TS:      time.Unix(int64(seq), 0).UTC(),
		Session: testSession,
		Type:    typ,
		Data:    json.RawMessage(data),
	}
}

func encodeLines(t *testing.T, events ...Event) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, ev := range events {
		data, err := jsonv2.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func writeSessionFile(t *testing.T, stateDir, content string) string {
	t.Helper()
	dir := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := SessionPath(stateDir, testSession)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	return path
}

func TestCreateNewGeneratesCanonicalID(t *testing.T) {
	stateDir := t.TempDir()
	store, err := CreateNew(stateDir)
	if err != nil {
		t.Fatalf("CreateNew: %v", err)
	}
	id := store.ID()
	canonical, err := CanonicalID(id)
	if err != nil || canonical != id {
		t.Fatalf("CreateNew ID = %q (canonical %q, %v)", id, canonical, err)
	}
	if want := SessionPath(stateDir, id); store.Path() != want {
		t.Fatalf("Path = %q, want %q", store.Path(), want)
	}
	if _, err := os.Stat(store.Path()); err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := Create(stateDir, ""); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Create with an empty ID = %v, want ErrInvalidID", err)
	}
}

func TestCreateCreatesDirectoryAndFile(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	st, err := Create(stateDir, testSession)
	if err != nil {
		t.Fatalf("Create: %v", err)
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

func TestCreateRejectsExistingAndInvalidID(t *testing.T) {
	stateDir := t.TempDir()
	st, err := Create(stateDir, testSession)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Store.Close: %v", err)
		}
	}()
	if _, err := Create(stateDir, testSession); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second Create: got %v, want os.ErrExist", err)
	}
	if _, err := Create(stateDir, "../../escape"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Create traversal id: got %v, want ErrInvalidID", err)
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

func TestStoreSyncAndClose(t *testing.T) {
	stateDir := t.TempDir()
	st, err := Create(stateDir, testSession)
	if err != nil {
		t.Fatalf("Create: %v", err)
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
	if err := st.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(`{}`)}); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("Append after Close: got %v, want ErrStoreClosed", err)
	}

	got, err := Read(st.Path())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEventsEqual(t, got, want)
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

func TestOpenRepairsTornFinalLine(t *testing.T) {
	stateDir := t.TempDir()
	first := encodeLines(t, testEvent(1, TypeSessionStart, `{"model":"m"}`))
	content := append(append([]byte(nil), first...), []byte(`{"v":2,"seq":2,"sess`)...)
	path := writeSessionFile(t, stateDir, string(content))

	st, err := Open(stateDir, testSession)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	events := st.Events()
	if len(events) != 2 {
		t.Fatalf("events after repair = %d, want 2: %+v", len(events), events)
	}
	if events[0].Type != TypeSessionStart || events[1].Type != TypeSessionRepair {
		t.Fatalf("event types = %v, want session_start, session_repair", []EventType{events[0].Type, events[1].Type})
	}
	var repair RepairData
	if err := jsonv2.Unmarshal(events[1].Data, &repair); err != nil {
		t.Fatalf("decode repair: %v", err)
	}
	if repair.DiscardedBytes <= 0 || repair.LastGoodSeq != 1 || repair.Reason == "" {
		t.Fatalf("repair = %+v", repair)
	}
	if err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append after repair: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 3 || got[2].Seq != 3 {
		t.Fatalf("log after repair = %+v", got)
	}
}

func TestOpenTerminatesFinalLineWithoutNewline(t *testing.T) {
	stateDir := t.TempDir()
	line := encodeLines(t, testEvent(1, TypeSessionStart, `{}`))
	path := writeSessionFile(t, stateDir, string(bytes.TrimSuffix(line, []byte("\n"))))

	st, err := Open(stateDir, testSession)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := len(st.Events()); got != 1 {
		t.Fatalf("events = %d, want 1", got)
	}
	if err := st.Append(Event{Type: TypeUserMessage, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(data, []byte("\n")) != 2 {
		t.Fatalf("newline count = %d, want 2: %q", bytes.Count(data, []byte("\n")), data)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 || got[0].Type != TypeSessionStart || got[1].Type != TypeUserMessage {
		t.Fatalf("events = %+v", got)
	}
}

func TestOpenRejectsCorruptFinalLine(t *testing.T) {
	stateDir := t.TempDir()
	content := string(encodeLines(t,
		testEvent(1, TypeSessionStart, `{}`),
		testEvent(3, TypeUserMessage, `{}`),
	))
	path := writeSessionFile(t, stateDir, content)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(stateDir, testSession); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open: got %v, want ErrCorrupt", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("Open changed a corrupt log: before %q, after %q", before, after)
	}
}

func TestOpenRejectsUnsupportedVersionAndMode(t *testing.T) {
	t.Run("v0 version", func(t *testing.T) {
		stateDir := t.TempDir()
		path := writeSessionFile(t, stateDir, `{"v":1,"seq":1,"ts":"1970-01-01T00:00:00Z","session":"`+testSession+`","type":"session_start","data":{}}`+"\n")
		if _, err := Open(stateDir, testSession); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("Open: got %v, want ErrUnsupportedVersion", err)
		}
		if _, err := Read(path); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("Read: got %v, want ErrUnsupportedVersion", err)
		}
	})
	t.Run("broad mode", func(t *testing.T) {
		stateDir := t.TempDir()
		path := writeSessionFile(t, stateDir, string(encodeLines(t, testEvent(1, TypeSessionStart, `{}`))))
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(stateDir, testSession); !errors.Is(err, ErrMode) {
			t.Fatalf("Open: got %v, want ErrMode", err)
		}
	})
}

func TestSessionLocks(t *testing.T) {
	stateDir := t.TempDir()
	st, err := Create(stateDir, testSession)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.Append(Event{Type: TypeSessionStart, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(stateDir, testSession); !errors.Is(err, ErrLocked) {
		t.Fatalf("Open while exclusive: got %v, want ErrLocked", err)
	}
	if _, err := OpenShared(stateDir, testSession); !errors.Is(err, ErrLocked) {
		t.Fatalf("OpenShared while exclusive: got %v, want ErrLocked", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	src, err := OpenShared(stateDir, testSession)
	if err != nil {
		t.Fatalf("OpenShared: %v", err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			t.Errorf("Source.Close: %v", err)
		}
	}()
	if _, err := Open(stateDir, testSession); !errors.Is(err, ErrLocked) {
		t.Fatalf("Open while shared: got %v, want ErrLocked", err)
	}
	other, err := OpenShared(stateDir, testSession)
	if err != nil {
		t.Fatalf("second OpenShared: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("second Source.Close: %v", err)
	}
}

func TestCopyHistoryRenumbersAndPreserves(t *testing.T) {
	stateDir := t.TempDir()
	parent := []Event{
		testEvent(1, TypeSessionStart, `{"workspace":"/w"}`),
		testEvent(2, TypeUserMessage, `{"text":"hi"}`),
		testEvent(3, TypeModelResponse, `{"text":"there"}`),
		testEvent(4, TypeRunEnd, `{"status":"completed"}`),
	}
	newID := "0190a0c4-0000-7000-8000-000000000002"
	child, err := Create(stateDir, newID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() {
		if err := child.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	if err := child.Append(Event{Type: TypeSessionStart, Data: json.RawMessage(`{"workspace":"/w","parent_session":"` + testSession + `"}`)}); err != nil {
		t.Fatalf("Append session_start: %v", err)
	}
	if err := child.CopyHistory(parent); err != nil {
		t.Fatalf("CopyHistory: %v", err)
	}
	got := child.Events()
	if len(got) != 4 {
		t.Fatalf("child events = %d, want 4: %+v", len(got), got)
	}
	if got[0].Type != TypeSessionStart || got[0].Session != newID {
		t.Fatalf("child session_start = %+v", got[0])
	}
	if got[1].Type != TypeUserMessage || got[2].Type != TypeModelResponse || got[3].Type != TypeRunEnd {
		t.Fatalf("copied types = %v", []EventType{got[1].Type, got[2].Type, got[3].Type})
	}
	for i, ev := range got {
		if ev.Seq != uint64(i+1) {
			t.Errorf("event %d seq = %d, want %d", i, ev.Seq, i+1)
		}
		if ev.Session != newID {
			t.Errorf("event %d session = %q, want %q", i, ev.Session, newID)
		}
	}
	for i := 1; i < len(got); i++ {
		if !got[i].TS.Equal(parent[i].TS) {
			t.Errorf("copied event %d TS = %v, want %v", i, got[i].TS, parent[i].TS)
		}
		if !bytes.Equal(got[i].Data, parent[i].Data) {
			t.Errorf("copied event %d data = %s, want %s", i, got[i].Data, parent[i].Data)
		}
	}
}

func TestCanonicalIDAndNewID(t *testing.T) {
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
	canonical, err := CanonicalID("{" + id + "}")
	if err != nil {
		t.Fatalf("CanonicalID: %v", err)
	}
	if canonical != id {
		t.Errorf("CanonicalID = %q, want %q", canonical, id)
	}
	if _, err := CanonicalID("../" + id); err == nil {
		t.Fatal("CanonicalID accepted a traversal path")
	}
	if _, err := CanonicalID("not-a-uuid"); err == nil {
		t.Fatal("CanonicalID accepted a non-UUID")
	}
}
