package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// transcriptView is the vertical window over the rendered transcript. Lines
// are prewrapped to the view width before they reach the window, so View costs
// O(visible rows) and never scans the transcript. This keeps a long session's
// streaming cost independent of its history size.
type transcriptView struct {
	lines  []string
	width  int
	height int
	offset int // index of the first visible line
}

// SetSize resizes the window and clamps the scroll offset.
func (v *transcriptView) SetSize(width, height int) {
	v.width = max(0, width)
	v.height = max(0, height)
	v.clamp()
}

// Height returns the number of visible rows.
func (v *transcriptView) Height() int { return v.height }

// SetLines replaces the rendered content. The caller transfers ownership of
// lines; the window never mutates it.
func (v *transcriptView) SetLines(lines []string) {
	v.lines = lines
	v.clamp()
}

// YOffset returns the index of the first visible line.
func (v *transcriptView) YOffset() int { return v.offset }

// GotoBottom scrolls to the last page.
func (v *transcriptView) GotoBottom() { v.offset = v.maxOffset() }

// AtBottom reports whether the last line is visible.
func (v *transcriptView) AtBottom() bool { return v.offset >= v.maxOffset() }

// ScrollUp moves the window up by n lines.
func (v *transcriptView) ScrollUp(n int) {
	v.offset = max(0, v.offset-n)
}

// ScrollDown moves the window down by n lines.
func (v *transcriptView) ScrollDown(n int) {
	v.offset = min(v.maxOffset(), v.offset+n)
}

// View renders exactly Height rows, padding short content with blank lines.
func (v *transcriptView) View() string {
	if v.width <= 0 || v.height <= 0 {
		return ""
	}
	end := min(v.offset+v.height, len(v.lines))
	rows := make([]string, 0, v.height)
	for _, line := range v.lines[v.offset:end] {
		rows = append(rows, ansi.Truncate(line, v.width, ""))
	}
	for len(rows) < v.height {
		rows = append(rows, "")
	}
	return strings.Join(rows, "\n")
}

func (v *transcriptView) maxOffset() int {
	return max(0, len(v.lines)-v.height)
}

func (v *transcriptView) clamp() {
	v.offset = min(max(0, v.offset), v.maxOffset())
}

// wrapLines wraps every line to width while preserving ANSI styling. The
// transcript window never soft-wraps, so every rendered line must already fit.
func wrapLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if ansi.StringWidth(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(line, width, " "), "\n")...)
	}
	return out
}
