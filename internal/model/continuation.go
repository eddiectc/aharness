package model

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"sync"
	"unicode/utf8"
)

// Continuation limits. The display copy of thinking text stays at
// MaxReasoningText; the adapter-owned copy is larger because a provider that
// requires a tool-round continuation may reject a truncated echo.
const (
	maxContinuationBytes = 4 << 20  // one response
	maxContinuationTotal = 16 << 20 // all retained responses
)

// continuationCache retains provider-specific material for one adapter. The
// durable transcript deliberately omits thinking, so an adapter that must
// continue a tool round keeps a bounded in-memory copy and attaches it to the
// matching replayed assistant message. A new dialect adapter with reasoning
// models MUST use this cache (or an equivalent bounded cache) and add the
// two-round wire test described in docs/architecture.md §2.4.
//
// The cache is keyed by the canonical assistant message (content plus tool-call
// IDs), which is exactly what the runner projects from the recorded response.
// Each dialect chooses its own value type: Chat stores thinking text, Responses
// stores raw reasoning items, and a future native adapter stores its signed
// blocks. The material is never logged and dies with the adapter.
type continuationCache[T any] struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]T
	order   [][sha256.Size]byte
	bytes   int
	size    func(T) int
}

func newContinuationCache[T any](size func(T) int) continuationCache[T] {
	return continuationCache[T]{size: size}
}

// remember retains one completed response's continuation material. A value
// larger than the per-response bound is dropped rather than truncated, because
// a partial echo is not a faithful continuation.
func (c *continuationCache[T]) remember(key [sha256.Size]byte, value T) {
	if c.size == nil {
		return
	}
	n := c.size(value)
	if n <= 0 || n > maxContinuationBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[[sha256.Size]byte]T)
	}
	if previous, ok := c.entries[key]; ok {
		c.bytes -= c.size(previous)
	} else {
		c.order = append(c.order, key)
	}
	c.entries[key] = value
	c.bytes += n
	// Drop the oldest entries until the cache fits. The newest entry is always
	// retained even when it alone exceeds the total budget.
	for c.bytes > maxContinuationTotal && len(c.order) > 1 {
		oldest := c.order[0]
		c.order = c.order[1:]
		if previous, ok := c.entries[oldest]; ok {
			delete(c.entries, oldest)
			c.bytes -= c.size(previous)
		}
	}
}

// lookup returns the retained material for an assistant message.
func (c *continuationCache[T]) lookup(key [sha256.Size]byte) (T, bool) {
	var zero T
	if c.size == nil {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) == 0 {
		return zero, false
	}
	value, ok := c.entries[key]
	return value, ok
}

// continuationKey fingerprints the canonical assistant message. A message
// with tool calls is identified by its call IDs, which are unique per
// response; a message without calls is identified by its content. Components
// are length-prefixed and domain-separated so no combination is ambiguous.
func continuationKey(content string, calls []ToolCall) [sha256.Size]byte {
	h := sha256.New()
	var size [8]byte
	writeContinuationString := func(s string) {
		binary.LittleEndian.PutUint64(size[:], uint64(len(s)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(s))
	}
	if len(calls) == 0 {
		_, _ = h.Write([]byte{0})
		writeContinuationString(strings.ToValidUTF8(content, "\uFFFD"))
	} else {
		_, _ = h.Write([]byte{1})
		for _, call := range calls {
			writeContinuationString(strings.ToValidUTF8(call.ID, "\uFFFD"))
		}
	}
	var key [sha256.Size]byte
	copy(key[:], h.Sum(nil))
	return key
}

// capContinuationReasoning bounds the adapter-owned copy at a rune boundary.
// The display copy is capped separately by capReasoning.
func capContinuationReasoning(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= maxContinuationBytes {
		return s
	}
	cut := maxContinuationBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
