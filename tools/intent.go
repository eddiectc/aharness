package tools

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"
	"unicode/utf8"
)

// IntentArgument is the shared tool-argument name for the display-only
// summary that titles a host tool block. It never affects execution, and
// semantic comparisons of tool arguments ignore it.
const IntentArgument = "intent"

// MaxIntentBytes bounds the display-only intent title. A longer value is cut
// on a rune boundary for display instead of failing the call: intent is
// presentation metadata, not execution input.
const MaxIntentBytes = 200

// Intent extracts the display-only intent summary from one tool call's raw
// arguments. It returns "" when the model omitted the argument, the arguments
// are malformed, or the intent is not a string. The result is trimmed and
// capped at MaxIntentBytes on a rune boundary.
//
// Every current tool schema declares intent as a required argument, but tools
// never require it while executing: a replayed toolset-v0/v1 call or a model
// that omits the argument must still run, and a missing title is cosmetic.
func Intent(args json.RawMessage) string {
	var probe struct {
		Intent string `json:"intent"`
	}
	if err := jsonv2.Unmarshal(args, &probe); err != nil {
		return ""
	}
	return normalizeIntent(probe.Intent)
}

// normalizeIntent trims surrounding space and caps the value on a rune
// boundary.
func normalizeIntent(intent string) string {
	intent = strings.TrimSpace(intent)
	if len(intent) <= MaxIntentBytes {
		return intent
	}
	cut := MaxIntentBytes
	for cut > 0 && !utf8.RuneStart(intent[cut]) {
		cut--
	}
	return strings.TrimSpace(intent[:cut])
}

// IntentLine derives a one-line display intent from a task prompt: the first
// non-empty line, control-stripped, whitespace-collapsed, and capped on a rune
// boundary with an ellipsis. It is the same derivation the spawner uses for
// SubagentStarted.Intent, so a host can render a pre-start roster without
// reimplementing it.
func IntentLine(task string) string {
	for line := range strings.SplitSeq(task, "\n") {
		collapsed := strings.Join(strings.Fields(intentSafe(line)), " ")
		if collapsed == "" {
			continue
		}
		if len(collapsed) <= MaxIntentBytes {
			return collapsed
		}
		cut := MaxIntentBytes
		for cut > 0 && !utf8.RuneStart(collapsed[cut]) {
			cut--
		}
		return collapsed[:cut] + "…"
	}
	return ""
}

// intentSafe is the display sanitization IntentLine applies. It mirrors the
// agent's display-safe policy: valid UTF-8, control characters made visible or
// removed instead of reaching a host terminal.
func intentSafe(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20:
			b.WriteRune(0x2400 + r)
		case r == 0x7f:
			b.WriteRune(0x2421)
		case r >= 0x80 && r <= 0x9f:
			b.WriteRune('\uFFFD')
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f || r == 0x061c:
			// bidi controls are dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
