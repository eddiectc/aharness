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

// Create creates the sessions directory beneath stateDir and opens a new
// session log at SessionPath(stateDir, sessionID). The directory is created
// with mode 0700 and the file with mode 0600; an existing file is an error.
func Create(stateDir, sessionID string) (*Store, error) {
	id, err := CanonicalID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidID, err)
	}
	dir := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory %s: %w", dir, err)
	}
	path := SessionPath(stateDir, id)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create session log %s: %w", path, err)
	}
	if err := lockFile(file, true); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock session log %s: %w", path, err)
	}
	return &Store{id: id, path: path, file: file}, nil
}

// CreateNew creates a new session log with a generated UUIDv7 identifier.
func CreateNew(stateDir string) (*Store, error) {
	return Create(stateDir, NewID())
}

// Open opens an existing session log for resume. It takes an exclusive
// non-blocking lock, verifies the file mode, validates the log, truncates only
// a torn final line (recording session_repair), terminates a valid final line
// that lacks a newline, and positions the store for appending.
func Open(stateDir, sessionID string) (*Store, error) {
	id, err := CanonicalID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidID, err)
	}
	path := SessionPath(stateDir, id)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return nil, fmt.Errorf("open session log %s: %w", path, err)
	}
	if err := lockFile(file, true); err != nil {
		_ = file.Close()
		if errors.Is(err, errWouldLock) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return nil, fmt.Errorf("lock session log %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		release(file)
		return nil, fmt.Errorf("stat session log %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		release(file)
		return nil, fmt.Errorf("open session log %s: not a regular file", path)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		release(file)
		return nil, fmt.Errorf("%w: session log %s has mode %#o, want 0600", ErrMode, path, perm)
	}

	stats, err := readEvents(file, path)
	if err != nil {
		release(file)
		return nil, err
	}
	if len(stats.events) == 0 {
		release(file)
		return nil, fmt.Errorf("%w: %s has no valid events", ErrCorrupt, path)
	}
	if stats.events[0].Type != TypeSessionStart {
		release(file)
		return nil, fmt.Errorf("%w: %s does not start with session_start", ErrCorrupt, path)
	}

	store := &Store{id: id, path: path, file: file, next: stats.lastSeq, events: stats.events}
	switch {
	case stats.torn:
		if err := file.Truncate(stats.goodBytes); err != nil {
			release(file)
			return nil, fmt.Errorf("truncate torn session log %s: %w", path, err)
		}
		repair, err := NewEvent(TypeSessionRepair, 0, "", RepairData{
			DiscardedBytes: stats.totalBytes - stats.goodBytes,
			LastGoodSeq:    stats.lastSeq,
			Reason:         "torn final line",
		})
		if err != nil {
			release(file)
			return nil, err
		}
		if err := store.Append(repair); err != nil {
			release(file)
			return nil, err
		}
		if err := store.Sync(); err != nil {
			release(file)
			return nil, err
		}
	case stats.missingNewline:
		if _, err := file.Write([]byte("\n")); err != nil {
			release(file)
			return nil, fmt.Errorf("terminate session log %s: %w", path, err)
		}
		if err := file.Sync(); err != nil {
			release(file)
			return nil, fmt.Errorf("sync session log %s: %w", path, err)
		}
	}
	return store, nil
}

// Source is a read-only, shared-locked view of a session log used as the
// parent of a fork. The parent file is never modified.
type Source struct {
	id      string
	file    *os.File
	events  []Event
	lastSeq uint64
}

// OpenShared opens an existing session log for a fork. It takes a shared
// non-blocking lock, verifies the file mode, and validates the log. A torn
// final line is ignored because the parent is never rewritten.
func OpenShared(stateDir, sessionID string) (*Source, error) {
	id, err := CanonicalID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidID, err)
	}
	path := SessionPath(stateDir, id)
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session log %s: %w", path, err)
	}
	if err := lockFile(file, false); err != nil {
		_ = file.Close()
		if errors.Is(err, errWouldLock) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return nil, fmt.Errorf("lock session log %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		release(file)
		return nil, fmt.Errorf("stat session log %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		release(file)
		return nil, fmt.Errorf("open session log %s: not a regular file", path)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		release(file)
		return nil, fmt.Errorf("%w: session log %s has mode %#o, want 0600", ErrMode, path, perm)
	}
	stats, err := readEvents(file, path)
	if err != nil {
		release(file)
		return nil, err
	}
	return &Source{id: id, file: file, events: stats.events, lastSeq: stats.lastSeq}, nil
}

// SessionPath returns the session log path for a canonical session ID.
func SessionPath(stateDir, id string) string {
	return filepath.Join(stateDir, "sessions", id+".jsonl")
}

// ID returns the canonical session identifier.
func (s *Store) ID() string { return s.id }

// Path returns the session log path.
func (s *Store) Path() string { return s.path }

// LastSeq returns the sequence number of the most recently appended event.
func (s *Store) LastSeq() uint64 { return s.next }

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

// Append writes one event as a JSON line. It assigns V, Seq, TS, and Session;
// callers leave those fields zero. The first event has Seq 1 and each later
// event increments it by one. The line is encoded into memory first so a
// marshal failure cannot leave a partial line in the log.
func (s *Store) Append(ev Event) error {
	return s.appendAt(ev, time.Time{})
}

// CopyHistory appends the parent's events to a new fork log, skipping its
// session_start. Sequence numbers and the session ID are assigned for the new
// log; payloads, turns, roles, and timestamps are preserved.
func (s *Store) CopyHistory(events []Event) error {
	for _, ev := range events {
		if ev.Type == TypeSessionStart {
			continue
		}
		if err := s.appendAt(ev, ev.TS); err != nil {
			return err
		}
	}
	return nil
}

// appendAt writes one event, assigning V, Seq, and Session. A zero ts uses the
// current time; a non-zero ts is preserved for fork copies.
func (s *Store) appendAt(ev Event, ts time.Time) error {
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
	if ts.IsZero() {
		ev.TS = time.Now().UTC()
	} else {
		ev.TS = ts
	}
	ev.Session = s.id
	ev.Data = bytes.Clone(ev.Data)

	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)
	if err := jsonv2.MarshalEncode(enc, ev); err != nil {
		return fmt.Errorf("append event to %s: %w", s.path, err)
	}
	if buf.Len() > maxEventLine {
		return fmt.Errorf("append event to %s: line is %d bytes, limit %d: %w", s.path, buf.Len(), maxEventLine, ErrInvalidEvent)
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

// Close syncs and closes the log and releases the session lock. A second Close
// returns nil.
func (s *Store) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	syncErr := s.file.Sync()
	if syncErr != nil {
		syncErr = fmt.Errorf("sync session log %s: %w", s.path, syncErr)
	}
	unlockErr := unlockFile(s.file)
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock session log %s: %w", s.path, unlockErr)
	}
	closeErr := s.file.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("close session log %s: %w", s.path, closeErr)
	}
	return errors.Join(syncErr, unlockErr, closeErr)
}

// Events returns the parent events included in the fork.
func (s *Source) Events() []Event {
	out := make([]Event, len(s.events))
	for i, ev := range s.events {
		ev.Data = bytes.Clone(ev.Data)
		out[i] = ev
	}
	return out
}

// LastSeq returns the sequence number of the last valid parent event.
func (s *Source) LastSeq() uint64 { return s.lastSeq }

// ID returns the canonical parent session identifier.
func (s *Source) ID() string { return s.id }

// Close releases the parent's shared lock and file descriptor.
func (s *Source) Close() error {
	if s.file == nil {
		return nil
	}
	unlockErr := unlockFile(s.file)
	closeErr := s.file.Close()
	s.file = nil
	return errors.Join(unlockErr, closeErr)
}

// release closes a partially initialized file, releasing any lock with it.
func release(file *os.File) {
	_ = unlockFile(file)
	_ = file.Close()
}
