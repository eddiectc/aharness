package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestIntentExtractsTheDisplayTitle(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{"absent", `{"path":"a.txt"}`, ""},
		{"valid", `{"path":"a.txt","intent":"Read the config"}`, "Read the config"},
		{"trimmed", `{"intent":"  Inspect the parser  "}`, "Inspect the parser"},
		{"empty", `{"intent":"   "}`, ""},
		{"non-string", `{"intent":42}`, ""},
		{"malformed", `{"intent":`, ""},
		{"not-an-object", `["intent"]`, ""},
		{"duplicate", `{"intent":"first","intent":"second"}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Intent([]byte(tc.args)); got != tc.want {
				t.Fatalf("Intent(%s) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestIntentCapsOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("a", MaxIntentBytes+20)
	if got := Intent([]byte(`{"intent":"` + long + `"}`)); len(got) != MaxIntentBytes {
		t.Fatalf("ASCII intent length = %d, want %d", len(got), MaxIntentBytes)
	}

	// A three-byte rune straddling the cap is dropped whole.
	wide := strings.Repeat("€", MaxIntentBytes)
	got := Intent([]byte(`{"intent":"` + wide + `"}`))
	if len(got) > MaxIntentBytes {
		t.Fatalf("wide intent length = %d, want at most %d", len(got), MaxIntentBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("wide intent is not valid UTF-8: %q", got)
	}
	if len(got) != MaxIntentBytes-2 {
		t.Fatalf("wide intent length = %d, want the last whole rune at %d", len(got), MaxIntentBytes-2)
	}
}
