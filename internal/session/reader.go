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
	// maxEventLine bounds one JSONL line, including the envelope and JSON
	// escaping. The v1 limits table raises it to 4 MiB.
	maxEventLine = 4 << 20
	// readBufferSize is the bufio refill size; lines may exceed it and are
	// assembled from multiple chunks.
	readBufferSize = 64 << 10
)

// errTornLine marks a line that is syntactically incomplete or oversized. A
// torn final line is a crash artifact; the same condition on a non-final line
// is corruption.
var errTornLine = errors.New("torn session line")

// readStats is the result of replaying a session log.
type readStats struct {
	events         []Event
	lastSeq        uint64
	goodBytes      int64
	totalBytes     int64
	missingNewline bool
	torn           bool
}

// Read replays a session log. A final line without a trailing newline is
// accepted. A torn final line is ignored; an invalid line followed by more
// data is an error. The file is opened read-only and is never rewritten.
func Read(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session log %s: %w", path, err)
	}
	defer func() {
		// Read-only close; a failure cannot affect the decoded events.
		_ = file.Close()
	}()
	stats, err := readEvents(file, path)
	if err != nil {
		return nil, err
	}
	return stats.events, nil
}

// readEvents replays r, tracking byte offsets so a torn final line can be
// truncated without touching the durable prefix.
func readEvents(r io.Reader, path string) (readStats, error) {
	br := bufio.NewReaderSize(r, readBufferSize)
	var (
		stats     readStats
		wantSeq   uint64 = 1
		sessionID string
		offset    int64
		lineNo    int
	)
	for {
		line, terminated, oversized, consumed, err := readLine(br, maxEventLine)
		if errors.Is(err, io.EOF) {
			return stats, nil
		}
		if err != nil {
			return stats, fmt.Errorf("read session log %s: %w", path, err)
		}
		lineNo++
		start := offset
		offset += consumed
		stats.totalBytes = offset

		final, err := isFinalLine(br)
		if err != nil {
			return stats, fmt.Errorf("read session log %s: %w", path, err)
		}
		ev, err := decodeEventLine(line, oversized, lineNo)
		if err == nil {
			err = checkEvent(ev, wantSeq, sessionID, lineNo)
		}
		if err != nil {
			if errors.Is(err, ErrUnsupportedVersion) {
				return stats, fmt.Errorf("read session log %s: %w", path, err)
			}
			if errors.Is(err, errTornLine) && final {
				// A torn final line is a crash artifact, not corruption of
				// the durable prefix.
				stats.torn = true
				stats.goodBytes = start
				return stats, nil
			}
			if errors.Is(err, errTornLine) {
				return stats, fmt.Errorf("read session log %s: %w: %w", path, ErrCorrupt, err)
			}
			return stats, fmt.Errorf("read session log %s: %w", path, err)
		}
		if sessionID == "" {
			sessionID = ev.Session
		}
		wantSeq = ev.Seq + 1
		stats.lastSeq = ev.Seq
		stats.events = append(stats.events, ev)
		stats.goodBytes = offset
		stats.missingNewline = !terminated
	}
}

// readLine returns the next line without its trailing newline, whether a
// newline terminated it, whether it exceeded limit bytes, and how many raw
// bytes it consumed. io.EOF means the reader held no further bytes.
func readLine(br *bufio.Reader, limit int) (line []byte, terminated, oversized bool, consumed int64, err error) {
	for {
		chunk, readErr := br.ReadSlice('\n')
		consumed += int64(len(chunk))
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
			terminated = true
			if !oversized {
				line = line[:len(line)-1]
			}
			return line, terminated, oversized, consumed, nil
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			if consumed == 0 {
				return nil, false, false, 0, io.EOF
			}
			if !oversized && len(line) > limit {
				oversized = true
				line = nil
			}
			return line, false, oversized, consumed, nil
		default:
			return nil, false, false, consumed, readErr
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
		return Event{}, fmt.Errorf("%w: line %d exceeds %d bytes", errTornLine, lineNo, maxEventLine)
	}
	var ev Event
	if err := jsonv2.Unmarshal(line, &ev); err != nil {
		return Event{}, fmt.Errorf("%w: line %d: %w", errTornLine, lineNo, err)
	}
	if ev.V != Version {
		return Event{}, fmt.Errorf("%w: line %d: schema version %d, want %d", ErrUnsupportedVersion, lineNo, ev.V, Version)
	}
	if ev.Type == "" {
		return Event{}, fmt.Errorf("%w: line %d: empty event type", ErrCorrupt, lineNo)
	}
	if !jsontext.Value(ev.Data).IsValid() {
		return Event{}, fmt.Errorf("%w: line %d: data is not valid json", ErrCorrupt, lineNo)
	}
	return ev, nil
}

func checkEvent(ev Event, wantSeq uint64, sessionID string, lineNo int) error {
	if ev.Seq != wantSeq {
		return fmt.Errorf("%w: line %d: seq %d, want %d", ErrCorrupt, lineNo, ev.Seq, wantSeq)
	}
	if ev.Session == "" {
		return fmt.Errorf("%w: line %d: empty session id", ErrCorrupt, lineNo)
	}
	if sessionID != "" && ev.Session != sessionID {
		return fmt.Errorf("%w: line %d: session %q, want %q", ErrCorrupt, lineNo, ev.Session, sessionID)
	}
	return nil
}
