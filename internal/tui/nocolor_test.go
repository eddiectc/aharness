package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestNoColorWriterStripsColorsKeepsAttributes(t *testing.T) {
	var out bytes.Buffer
	w := &noColorWriter{w: &out}
	input := "\x1b[1;31mbold red\x1b[0m \x1b[38;5;196m256\x1b[0m \x1b[38;2;1;2;3mtrue\x1b[0m \x1b[4;42munder\x1b[0m"
	n, err := w.Write([]byte(input))
	if err != nil || n != len(input) {
		t.Fatalf("Write = %d,%v", n, err)
	}
	got := out.String()
	for _, forbidden := range []string{"31", "38;5", "38;2", "42"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("color %q survived: %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "\x1b[1m") || !strings.Contains(got, "\x1b[4m") {
		t.Fatalf("attributes were stripped: %q", got)
	}
	if !strings.Contains(got, "bold red") || !strings.Contains(got, "true") {
		t.Fatalf("text was lost: %q", got)
	}
}

func TestNoColorWriterBuffersSplitSequence(t *testing.T) {
	var out bytes.Buffer
	w := &noColorWriter{w: &out}
	if _, err := w.Write([]byte("a\x1b[3")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("1mb")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "31") {
		t.Fatalf("split color sequence survived: %q", out.String())
	}
	if !strings.Contains(out.String(), "ab") {
		t.Fatalf("text lost: %q", out.String())
	}
}
