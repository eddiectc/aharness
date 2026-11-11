package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapOutputShort(t *testing.T) {
	const input = "hello\n"
	got, truncated := capOutput(input, 32)
	if truncated {
		t.Errorf("capOutput short = truncated true, want false")
	}
	if got != input {
		t.Errorf("capOutput short = %q, want %q", got, input)
	}
}

func TestCapOutputTruncatesHeadTail(t *testing.T) {
	input := strings.Repeat("a", 2000)
	const max = 200
	got, truncated := capOutput(input, max)
	if !truncated {
		t.Fatalf("capOutput(2000 bytes, max 200) truncated = false, want true")
	}
	if len(got) > max {
		t.Errorf("capOutput length = %d, want <= %d", len(got), max)
	}
	if !strings.Contains(got, "[truncated ") || !strings.Contains(got, " bytes]") {
		t.Errorf("capOutput result missing truncation marker: %q", got)
	}
	if !strings.HasPrefix(got, "aaa") {
		t.Errorf("capOutput lost the head: %q", got)
	}
	if !strings.HasSuffix(got, "aa") {
		t.Errorf("capOutput lost the tail: %q", got)
	}
}

func TestCapOutputUTF8Boundaries(t *testing.T) {
	input := strings.Repeat("é", 1000)
	for _, max := range []int{31, 32, 33, 64, 101} {
		got, truncated := capOutput(input, max)
		if !truncated {
			t.Errorf("capOutput(é*1000, %d) truncated = false", max)
		}
		if len(got) > max {
			t.Errorf("capOutput(é*1000, %d) length = %d, want <= %d", max, len(got), max)
		}
		if !utf8.ValidString(got) {
			t.Errorf("capOutput(é*1000, %d) produced invalid UTF-8", max)
		}
	}
}

func TestCapHeadOutputTruncatesHeadOnly(t *testing.T) {
	input := strings.Repeat("a", 2000)
	const max = 200
	got, truncated := capHeadOutput(input, max)
	if !truncated {
		t.Fatalf("capHeadOutput(2000 bytes, max 200) truncated = false, want true")
	}
	if len(got) > max {
		t.Errorf("capHeadOutput length = %d, want <= %d", len(got), max)
	}
	if !strings.HasPrefix(got, "aaa") {
		t.Errorf("capHeadOutput lost the head: %q", got)
	}
	if !strings.Contains(got, "[truncated ") || !strings.HasSuffix(got, "bytes]\n") {
		t.Errorf("capHeadOutput missing truncation marker: %q", got)
	}
	if strings.Contains(got, strings.Repeat("a", max)) {
		t.Errorf("capHeadOutput kept bytes past the cap")
	}
}

func TestCapHeadOutputShort(t *testing.T) {
	const input = "hello\n"
	got, truncated := capHeadOutput(input, 32)
	if truncated {
		t.Errorf("capHeadOutput short = truncated true, want false")
	}
	if got != input {
		t.Errorf("capHeadOutput short = %q, want %q", got, input)
	}
}

func TestCapHeadOutputUTF8Boundaries(t *testing.T) {
	input := strings.Repeat("é", 1000)
	for _, max := range []int{31, 32, 33, 64, 101} {
		got, truncated := capHeadOutput(input, max)
		if !truncated {
			t.Errorf("capHeadOutput(é*1000, %d) truncated = false", max)
			continue
		}
		if len(got) > max {
			t.Errorf("capHeadOutput(é*1000, %d) length = %d, want <= %d", max, len(got), max)
		}
		if !utf8.ValidString(got) {
			t.Errorf("capHeadOutput(é*1000, %d) produced invalid UTF-8", max)
		}
	}
}

func TestCappedBuffer(t *testing.T) {
	t.Run("small", func(t *testing.T) {
		var b cappedBuffer
		b.max = 64
		if _, err := b.Write([]byte("hello ")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if _, err := b.Write([]byte("world")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, truncated := b.render()
		if truncated {
			t.Errorf("render truncated = true, want false")
		}
		if want := "hello world"; got != want {
			t.Errorf("render = %q, want %q", got, want)
		}
	})
	t.Run("small odd reconstruction", func(t *testing.T) {
		var b cappedBuffer
		b.max = 5
		for _, chunk := range []string{"ab", "cd"} {
			if _, err := b.Write([]byte(chunk)); err != nil {
				t.Fatalf("Write(%q): %v", chunk, err)
			}
		}
		got, truncated := b.render()
		if truncated {
			t.Errorf("render truncated = true, want false")
		}
		if want := "abcd"; got != want {
			t.Errorf("render = %q, want %q", got, want)
		}
	})
	t.Run("small odd exact", func(t *testing.T) {
		var b cappedBuffer
		b.max = 5
		for _, chunk := range []string{"ab", "cde"} {
			if _, err := b.Write([]byte(chunk)); err != nil {
				t.Fatalf("Write(%q): %v", chunk, err)
			}
		}
		got, truncated := b.render()
		if truncated {
			t.Errorf("render truncated = true, want false")
		}
		if want := "abcde"; got != want {
			t.Errorf("render = %q, want %q", got, want)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		var b cappedBuffer
		b.max = 64
		chunk := strings.Repeat("b", 32)
		for range 10 {
			if _, err := b.Write([]byte(chunk)); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		got, truncated := b.render()
		if !truncated {
			t.Fatalf("render truncated = false, want true")
		}
		if !strings.Contains(got, "[truncated ") {
			t.Errorf("render missing marker: %q", got)
		}
		if !strings.HasPrefix(got, "bbbb") || !strings.HasSuffix(got, "bbbb") {
			t.Errorf("render lost head or tail: %q", got)
		}
	})
}
