package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestAssistantMarkdownWrapsInsideLabelWidth covers a reported render: the
// assistant body was word-wrapped to m.width-2, then prefixed with the
// ten-cell "assistant " indent, leaving every row wider than the transcript.
// The second wrap in transcriptLines then stranded the final word on its own
// row, so a sentence could break as "... only in the\nsession JSONL ...".
func TestAssistantMarkdownWrapsInsideLabelWidth(t *testing.T) {
	const source = "1. Re-derivation was not guaranteed. The model could usually recover the list from the visible prior `todo` tool results — but compaction elides older tool results first and replaces them with `[elided tool result \"todo\": 118 bytes]`. Once that happened, the content existed only in the session JSONL, invisible to the model. It would then have had to infer completed-vs-pending state from the conversation, which is lossy."

	app := newTestApp(t, newManualHost())
	app.setSize(80, 24)
	it := app.appendAssistant(1, source)

	for i, row := range app.renderAssistant(newTheme(true), it) {
		if width := ansi.StringWidth(row); width > app.width {
			t.Fatalf("assistant row %d is %d cells wide, terminal is %d: %q", i, width, app.width, ansi.Strip(row))
		}
	}

	rows := plainRows(app.transcriptLines())
	for i, row := range rows {
		if strings.TrimSpace(row) == "the" {
			t.Fatalf("row %d is a stranded word: %q", i, rows[max(0, i-2):min(len(rows), i+3)])
		}
	}
}
