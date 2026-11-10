package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
)

// maxSteeringLine is the largest accepted steering line, excluding the
// terminating newline.
const maxSteeringLine = 64 << 10

// startSteering reads stdin lines and delivers them to the runner. The
// goroutine exits on EOF, a stdin error, or run cancellation. It performs no
// session logging and mutates no run state.
func startSteering(ctx context.Context, stdin io.Reader, stderr io.Writer) <-chan string {
	if stdin == nil {
		ch := make(chan string)
		close(ch)
		return ch
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		br := bufio.NewReaderSize(stdin, 64<<10)
		for {
			line, tooLong, err := readSteeringLine(br, maxSteeringLine)
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				return
			}
			if tooLong {
				_, _ = fmt.Fprintf(stderr, "aharness: steering line exceeds %d bytes and was ignored\n", maxSteeringLine)
				continue
			}
			select {
			case ch <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

// readSteeringLine returns one line without its newline. oversized reports a
// line over limit bytes; the rest of that line is discarded.
func readSteeringLine(br *bufio.Reader, limit int) (line string, oversized bool, err error) {
	var buf []byte
	for {
		chunk, readErr := br.ReadSlice('\n')
		if len(chunk) > 0 && !oversized {
			if len(buf)+len(chunk) > limit+1 {
				oversized = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch {
		case readErr == nil:
			if oversized {
				return "", true, nil
			}
			buf = buf[:len(buf)-1]
			if len(buf) > 0 && buf[len(buf)-1] == '\r' {
				buf = buf[:len(buf)-1]
			}
			return string(buf), false, nil
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			if !oversized && len(buf) > limit {
				oversized = true
				buf = nil
			}
			if len(buf) == 0 && !oversized {
				return "", false, io.EOF
			}
			if oversized {
				return "", true, nil
			}
			if len(buf) > 0 && buf[len(buf)-1] == '\r' {
				buf = buf[:len(buf)-1]
			}
			return string(buf), false, nil
		default:
			return "", false, readErr
		}
	}
}
