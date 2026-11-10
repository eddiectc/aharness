package session

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrStoreClosed is returned by operations that require an open store after
// Close has run.
var ErrStoreClosed = errors.New("session store closed")

// ErrInvalidEvent reports an event payload that cannot be encoded or decoded
// under the strict json/v2 rules (duplicate object names, invalid UTF-8, or
// syntactically invalid JSON).
var ErrInvalidEvent = errors.New("invalid session event")

// Store is the append-only JSONL session log writer. One goroutine owns a
// Store; Append, Sync, and Close must not be called concurrently.
type Store struct {
	id     string
	path   string
	file   *os.File
	next   uint64
	events []Event
	failed error
	closed bool
}

// NewStore creates the sessions directory beneath stateDir and opens a new
// session log at SessionPath(stateDir, sessionID). The directory is created
// with mode 0700 and the file with mode 0600; an existing file is an error.
func NewStore(stateDir, sessionID string) (*Store, error) {
	dir := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory %s: %w", dir, err)
	}
	path := SessionPath(stateDir, sessionID)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create session log %s: %w", path, err)
	}
	return &Store{id: sessionID, path: path, file: file}, nil
}

// SessionPath returns the session log path for a session ID.
func SessionPath(stateDir, id string) string {
	return filepath.Join(stateDir, "sessions", id+".jsonl")
}

// Append writes one event as a JSON line. It assigns V, Seq, TS, and Session;
// callers leave those fields zero. The first event has Seq 1 and each later
// event increments it by one. The line is encoded into memory first so a
// marshal failure cannot leave a partial line in the log.
func (s *Store) Append(ev Event) error {
	if s.closed {
		return fmt.Errorf("append event to %s: %w", s.path, ErrStoreClosed)
	}
	if s.failed != nil {
		return s.failed
	}
	if !jsontext.Value(ev.Data).IsValid() {
		return fmt.Errorf("append event to %s: data is not valid json: %w", s.path, ErrInvalidEvent)
	}
	ev.V = Version
	ev.Seq = s.next + 1
	ev.TS = time.Now().UTC()
	ev.Session = s.id
	ev.Data = bytes.Clone(ev.Data)

	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)
	if err := jsonv2.MarshalEncode(enc, ev); err != nil {
		return fmt.Errorf("append event to %s: %w", s.path, err)
	}
	if _, err := s.file.Write(buf.Bytes()); err != nil {
		// A failed write may have left a torn line. Stop writing so later
		// events cannot be interleaved with it.
		s.failed = fmt.Errorf("append event to %s: %w", s.path, err)
		return s.failed
	}
	s.next = ev.Seq
	s.events = append(s.events, ev)
	return nil
}

// Events returns a copy of the events appended so far. It remains valid after
// Close.
func (s *Store) Events() []Event {
	out := make([]Event, len(s.events))
	for i, ev := range s.events {
		ev.Data = bytes.Clone(ev.Data)
		out[i] = ev
	}
	return out
}

// Sync flushes previously appended events to stable storage.
func (s *Store) Sync() error {
	if s.closed {
		return fmt.Errorf("sync session log %s: %w", s.path, ErrStoreClosed)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("sync session log %s: %w", s.path, err)
	}
	return nil
}

// Close syncs and closes the log. A second Close returns nil.
func (s *Store) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	syncErr := s.file.Sync()
	if syncErr != nil {
		syncErr = fmt.Errorf("sync session log %s: %w", s.path, syncErr)
	}
	closeErr := s.file.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("close session log %s: %w", s.path, closeErr)
	}
	return errors.Join(syncErr, closeErr)
}

// Path returns the session log path.
func (s *Store) Path() string { return s.path }
