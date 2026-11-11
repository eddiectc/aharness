package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// fileOutputCap bounds read_file and search results.
	fileOutputCap = 60 << 10
	// commandOutputCap bounds combined run_command stdout and stderr.
	commandOutputCap = 64 << 10
	// panicStackCap bounds the stack attached to a recovered tool panic.
	panicStackCap = 4 << 10
)

// capHeadOutput keeps only the head of s when it exceeds max bytes and
// appends an explicit truncation marker. The returned string is at most max
// bytes when max can hold the marker. Rune boundaries are honored so a valid
// UTF-8 input stays valid.
func capHeadOutput(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	removed := len(s)
	var head int
	var marker string
	for {
		marker = fmt.Sprintf("\n[truncated %d bytes]\n", removed)
		budget := max - len(marker)
		if budget < 0 {
			budget = 0
		}
		head = safeHeadLen(s, budget)
		next := len(s) - head
		if next == removed {
			break
		}
		removed = next
	}
	out := s[:head] + marker
	if len(out) > max {
		// Rune-boundary adjustment can push the result a few bytes over a
		// tight budget; trim the head without splitting a rune.
		head = safeHeadLen(s[:head], head-(len(out)-max))
		out = s[:head] + marker
	}
	return out, true
}

// capOutput keeps the head and tail of s when it exceeds max bytes, inserting
// an explicit truncation marker. The returned string is at most max bytes when
// max can hold the marker. Rune boundaries are honored so a valid UTF-8 input
// stays valid.
func capOutput(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	removed := len(s)
	var head, tail int
	var marker string
	for {
		marker = fmt.Sprintf("\n[truncated %d bytes]\n", removed)
		budget := max - len(marker)
		if budget < 0 {
			budget = 0
		}
		head = safeHeadLen(s, budget/2)
		start := safeTailStart(s, len(s)-(budget-head))
		tail = len(s) - start
		next := len(s) - head - tail
		if next == removed {
			break
		}
		removed = next
	}
	out := s[:head] + marker + s[len(s)-tail:]
	if len(out) > max {
		// Rune-boundary adjustment can push the result a few bytes over a
		// tight budget; trim the head without splitting a rune.
		over := len(out) - max
		head = safeHeadLen(s[:head], head-over)
		out = s[:head] + marker + s[len(s)-tail:]
	}
	return out, true
}

// safeHeadLen returns the largest rune boundary at or below n.
func safeHeadLen(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	if n < 0 {
		return 0
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// safeTailStart moves start forward to the next rune boundary.
func safeTailStart(s string, start int) int {
	if start <= 0 {
		return 0
	}
	if start >= len(s) {
		return len(s)
	}
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return start
}

// cappedBuffer captures at most max bytes of a stream, keeping the head and
// tail and counting everything written. Memory stays bounded regardless of how
// much a child process emits.
type cappedBuffer struct {
	max   int
	head  []byte
	tail  []byte
	total int64
}

// Write implements io.Writer.
func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	headCap := b.max / 2
	tailCap := b.max - headCap
	if headCap > 0 && len(b.head) < headCap {
		take := min(headCap-len(b.head), len(p))
		b.head = append(b.head, p[:take]...)
	}
	if len(p) >= tailCap {
		b.tail = append(b.tail[:0], p[len(p)-tailCap:]...)
	} else {
		if over := len(b.tail) + len(p) - tailCap; over > 0 {
			b.tail = b.tail[over:]
		}
		b.tail = append(b.tail, p...)
	}
	return n, nil
}

// render returns the captured stream content and whether bytes were dropped.
func (b *cappedBuffer) render() (string, bool) {
	if b.total <= int64(b.max) {
		total := int(b.total)
		if len(b.head) >= total {
			return string(b.head[:total]), false
		}
		// tail holds the last min(total, tailCap) bytes; skip the part that
		// overlaps the head.
		offset := len(b.head) - (total - len(b.tail))
		return string(b.head) + string(b.tail[offset:]), false
	}
	dropped := b.total - int64(len(b.head)) - int64(len(b.tail))
	marker := fmt.Sprintf("\n[truncated %d bytes]\n", dropped)
	return string(b.head) + marker + string(b.tail), true
}

// sanitizeUTF8 replaces invalid byte sequences so a string can cross the JSON
// and transcript boundaries safely.
func sanitizeUTF8(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}
