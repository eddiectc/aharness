package session

import (
	"bufio"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	// maxEventLine bounds one JSONL line: a 1 MiB payload plus headroom for
	// the envelope fields and JSON punctuation.
	maxEventLine = 1<<20 + 64<<10
	// readBufferSize is the bufio refill size; lines may exceed it and are
	// assembled from multiple chunks.
	readBufferSize = 64 << 10
)

// Read replays a session log. A final line without a trailing newline is
// accepted. Only a torn or otherwise invalid final line is ignored; an invalid
// line followed by more data is an error. The file is opened read-only and is
// never rewritten.
func Read(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session log %s: %w", path, err)
	}
	defer func() {
		// Read-only close; a failure cannot affect the decoded events.
		_ = file.Close()
	}()
	return readEvents(file, path)
}

func readEvents(r io.Reader, path string) ([]Event, error) {
	br := bufio.NewReaderSize(r, readBufferSize)
	var (
		events    []Event
		wantSeq   uint64 = 1
		sessionID string
		lineNo    int
	)
	for {
		line, oversized, err := readLine(br, maxEventLine)
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read session log %s: %w", path, err)
		}
		lineNo++
		final, err := isFinalLine(br)
		if err != nil {
			return nil, fmt.Errorf("read session log %s: %w", path, err)
		}
		ev, err := decodeEventLine(line, oversized, lineNo)
		if err == nil {
			err = checkEvent(ev, wantSeq, sessionID, lineNo)
		}
		if err != nil {
			if final {
				// A torn or invalid final line is a crash artifact, not
				// corruption of the durable prefix.
				return events, nil
			}
			return nil, fmt.Errorf("read session log %s: %w", path, err)
		}
		if sessionID == "" {
			sessionID = ev.Session
		}
		wantSeq = ev.Seq + 1
		events = append(events, ev)
	}
}

// readLine returns the next line without its trailing newline. oversized
// reports that the line exceeded limit bytes and was discarded. io.EOF means
// the reader held no further bytes.
func readLine(br *bufio.Reader, limit int) (line []byte, oversized bool, err error) {
	for {
		chunk, readErr := br.ReadSlice('\n')
		if len(chunk) > 0 && !oversized {
			// Once the line exceeds the cap, discard chunks until it ends.
			if len(line)+len(chunk) > limit+1 {
				oversized = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		switch {
		case readErr == nil:
			if !oversized {
				line = line[:len(line)-1]
			}
			return line, oversized, nil
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			if len(line) == 0 && !oversized {
				return nil, false, io.EOF
			}
			return line, oversized, nil
		default:
			return nil, false, readErr
		}
	}
}

// isFinalLine reports whether the buffered reader has no more data after the
// line just read.
func isFinalLine(br *bufio.Reader) (bool, error) {
	_, err := br.Peek(1)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, io.EOF):
		return true, nil
	default:
		return false, err
	}
}

func decodeEventLine(line []byte, oversized bool, lineNo int) (Event, error) {
	if oversized {
		return Event{}, fmt.Errorf("%w: line %d exceeds %d bytes", ErrInvalidEvent, lineNo, maxEventLine)
	}
	var ev Event
	if err := jsonv2.Unmarshal(line, &ev); err != nil {
		return Event{}, fmt.Errorf("%w: line %d: %w", ErrInvalidEvent, lineNo, err)
	}
	if ev.V != Version {
		return Event{}, fmt.Errorf("%w: line %d: schema version %d, want %d", ErrInvalidEvent, lineNo, ev.V, Version)
	}
	if ev.Type == "" {
		return Event{}, fmt.Errorf("%w: line %d: empty event type", ErrInvalidEvent, lineNo)
	}
	if !jsontext.Value(ev.Data).IsValid() {
		return Event{}, fmt.Errorf("%w: line %d: data is not valid json", ErrInvalidEvent, lineNo)
	}
	return ev, nil
}

func checkEvent(ev Event, wantSeq uint64, sessionID string, lineNo int) error {
	if ev.Seq != wantSeq {
		return fmt.Errorf("%w: line %d: seq %d, want %d", ErrInvalidEvent, lineNo, ev.Seq, wantSeq)
	}
	if ev.Session == "" {
		return fmt.Errorf("%w: line %d: empty session id", ErrInvalidEvent, lineNo)
	}
	if sessionID != "" && ev.Session != sessionID {
		return fmt.Errorf("%w: line %d: session %q, want %q", ErrInvalidEvent, lineNo, ev.Session, sessionID)
	}
	return nil
}
