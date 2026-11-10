package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeTerminalControls(t *testing.T) {
	input := "a\x1b]52;c;SGVsbG8=\x07b\x1b[31mc\x00d\x7fe\u0085f\u202eg\u2069h\ttab\nnext"
	got := Sanitize(input)
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("sanitized text still contains ESC: %q", got)
	}
	if strings.ContainsRune(got, 0x07) || strings.ContainsRune(got, 0x00) || strings.ContainsRune(got, 0x7f) {
		t.Fatalf("sanitized text contains a C0 control: %q", got)
	}
	if strings.ContainsAny(got, "\u202e\u2069") {
		t.Fatalf("sanitized text contains bidi controls: %q", got)
	}
	if !strings.Contains(got, "␛]52;c;SGVsbG8=␇b") {
		t.Fatalf("OSC was not rendered as visible glyphs: %q", got)
	}
	if !strings.Contains(got, "    tab") || !strings.Contains(got, "\nnext") {
		t.Fatalf("tab/newline handling wrong: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("sanitized text is not valid UTF-8")
	}
}

func TestSanitizeInvalidUTF8AndC1(t *testing.T) {
	got := Sanitize(string([]byte{'a', 0xff, 'b', 0xc2, 0x85, 'c'}))
	if !strings.HasPrefix(got, "a\uFFFDb") {
		t.Fatalf("invalid UTF-8 not repaired: %q", got)
	}
	if strings.ContainsRune(got, 0x85) {
		t.Fatalf("C1 control passed through: %q", got)
	}
}

func TestSanitizeLineCollapsesNewlines(t *testing.T) {
	if got := SanitizeLine("a\nb\tc"); got != "a b    c" {
		t.Fatalf("SanitizeLine = %q", got)
	}
}

func TestCapTextRuneBoundary(t *testing.T) {
	text := strings.Repeat("é", 20)
	got := CapText(text, 5)
	if !utf8.ValidString(got) {
		t.Fatalf("capped text is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("missing truncation marker: %q", got)
	}
}

func FuzzSanitize(f *testing.F) {
	f.Add("plain")
	f.Add("a\x1b]52;c;x\x07b")
	f.Add("a\x00b\x7fc")
	f.Add("\u202e\u2066x")
	f.Add(string([]byte{0xff, 0xfe}))
	f.Fuzz(func(t *testing.T, input string) {
		got := Sanitize(input)
		if !utf8.ValidString(got) {
			t.Fatalf("not valid UTF-8: %q", got)
		}
		for _, r := range got {
			if r == 0x1b || r == 0x07 || (r < 0x20 && r != '\n') || (r >= 0x80 && r <= 0x9f) {
				t.Fatalf("unsafe control %U in %q", r, got)
			}
			if r == 0x202e || r == 0x2069 {
				t.Fatalf("bidi control %U in %q", r, got)
			}
		}
	})
}
