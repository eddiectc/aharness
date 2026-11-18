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

func TestIntentLineMatchesSpawnerDerivation(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"first line", "Read the data\nThen summarise.", "Read the data"},
		{"skips blank", "\n \n  Solve the model  \n", "Solve the model"},
		{"collapses", "Plan\t the   work", "Plan the work"},
		{"controls", "do\x01it\x7f", "do\u2401it\u2421"},
		{"bidi", "safe\u202eevil", "safeevil"},
		{"empty", " \n\t", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IntentLine(tc.text); got != tc.want {
				t.Fatalf("IntentLine(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
	long := strings.Repeat("é", MaxIntentBytes)
	got := IntentLine(long)
	if len(got) > MaxIntentBytes+len("…") || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("IntentLine long = %q (%d bytes)", got, len(got))
	}
}

func TestIntentLineIsStableOnMultibyteBoundaries(t *testing.T) {
	// A four-byte rune straddling the cap is dropped whole.
	text := strings.Repeat("a", MaxIntentBytes-1) + "😀"
	if got := IntentLine(text); !utf8.ValidString(got) || len(got) > MaxIntentBytes+len("…") {
		t.Fatalf("IntentLine = %q (%d bytes)", got, len(got))
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
