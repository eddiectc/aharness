package main

import (
	"io"

	"aharness/internal/agent"
)

// textDisplay adapts the runner display stream to the batch stdout sink. It
// handles only assistant text deltas; every other event is ignored.
func textDisplay(sink *textSink) func(agent.DisplayEvent) error {
	return func(ev agent.DisplayEvent) error {
		delta, ok := ev.(agent.TextDelta)
		if !ok {
			return nil
		}
		_, err := sink.Write([]byte(delta.Text))
		return err
	}
}

// textSink writes streamed assistant text and remembers whether a trailing
// newline is needed. Streaming stdout carries text as it arrives; the final
// newline is added once at the end.
type textSink struct {
	w     io.Writer
	wrote bool
	last  byte
}

func (s *textSink) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := s.w.Write(p)
	if n > 0 {
		s.wrote = true
		s.last = p[n-1]
	}
	return n, err
}

func (s *textSink) finish() error {
	if !s.wrote || s.last == '\n' {
		return nil
	}
	_, err := s.w.Write([]byte{'\n'})
	return err
}
