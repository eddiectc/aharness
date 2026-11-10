package tui

import (
	"strings"
	"unicode/utf8"
)

// Sanitize makes untrusted text safe to place in a terminal cell. It repairs
// invalid UTF-8, strips bidi override/isolate controls, expands tabs, and
// turns C0/C1 controls (including ESC) into visible glyphs. Newlines are
// preserved for multi-line text; use SanitizeLine for one-line contexts.
func Sanitize(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20:
			b.WriteRune(0x2400 + r)
		case r == 0x7F:
			b.WriteRune(0x2421)
		case r >= 0x80 && r <= 0x9F:
			b.WriteRune('\uFFFD')
		case isBidiControl(r):
			// Bidi overrides/isolates can reorder otherwise safe text.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SanitizeLine sanitizes text and collapses newlines and tabs to spaces. It is
// used for headers, file names, status lines, and tool summaries.
func SanitizeLine(s string) string {
	s = Sanitize(s)
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}

// CapText truncates s at a byte limit on a rune boundary and appends a marker.
func CapText(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return s[:cut] + "\n[truncated " + itoa(len(s)-cut) + " bytes]"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func isBidiControl(r rune) bool {
	switch r {
	case 0x061C, 0x200E, 0x200F, 0x202A, 0x202B, 0x202C, 0x202D, 0x202E,
		0x2066, 0x2067, 0x2068, 0x2069:
		return true
	default:
		return false
	}
}
