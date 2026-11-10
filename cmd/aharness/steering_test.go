package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadSteeringLine(t *testing.T) {
	limit := 16
	tests := []struct {
		name      string
		input     string
		want      string
		oversized bool
		wantEOF   bool
	}{
		{name: "line", input: "hello\n", want: "hello"},
		{name: "crlf", input: "hello\r\n", want: "hello"},
		{name: "final without newline", input: "hello", want: "hello"},
		{name: "empty line", input: "\n", want: ""},
		{name: "exact cap", input: strings.Repeat("x", limit) + "\n", want: strings.Repeat("x", limit)},
		{name: "over cap", input: strings.Repeat("x", limit+1) + "\n", oversized: true},
		{name: "over cap final", input: strings.Repeat("x", limit+1), oversized: true},
		{name: "empty input", input: "", wantEOF: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			br := bufio.NewReader(strings.NewReader(tc.input))
			got, oversized, err := readSteeringLine(br, limit)
			if tc.wantEOF {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("error = %v, want io.EOF", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if oversized != tc.oversized || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, oversized, tc.want, tc.oversized)
			}
		})
	}
}

func TestStartSteeringReadsAndRejectsOversized(t *testing.T) {
	input := "first\n" + strings.Repeat("x", maxSteeringLine+1) + "\nsecond\n"
	ch := startSteering(t.Context(), strings.NewReader(input), io.Discard)
	var got []string
	for line := range ch {
		got = append(got, line)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("lines = %q, want [first second]", got)
	}
}

func TestTextSinkFinishesWithNewline(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "adds newline", input: "text", want: "text\n"},
		{name: "keeps newline", input: "text\n", want: "text\n"},
		{name: "empty", input: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			sink := &textSink{w: &buf}
			if tc.input != "" {
				if _, err := sink.Write([]byte(tc.input)); err != nil {
					t.Fatal(err)
				}
			}
			if err := sink.finish(); err != nil {
				t.Fatal(err)
			}
			if buf.String() != tc.want {
				t.Fatalf("output = %q, want %q", buf.String(), tc.want)
			}
		})
	}
}
