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
