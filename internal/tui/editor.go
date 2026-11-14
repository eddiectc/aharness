package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// errEditedMessageTooLarge matches the composer/submission cap. The editor may
// write any file, but a result over the cap is rejected instead of being
// silently truncated.
var errEditedMessageTooLarge = errors.New("edited message is over the 64 KiB limit")

// openExternalEditor edits the current composer in $VISUAL or $EDITOR. The
// editor runs in the terminal's main screen; Bubble Tea restores the TUI when
// it exits. The feature is idle-only so a blocking editor cannot stall display
// events from an active run.
func (m *App) openExternalEditor() tea.Cmd {
	if m.headless {
		m.notice = "the external editor is unavailable in -ui-json"
		return nil
	}
	if m.run.active {
		m.notice = "the external editor is disabled while a run is active"
		return nil
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		m.notice = "set $VISUAL or $EDITOR to use the external editor"
		return nil
	}
	path, err := writeComposerTemp(m.composer.Value())
	if err != nil {
		m.notice = "editor: " + SanitizeLine(err.Error())
		return nil
	}
	return m.execEditor(editorCommand(editor, path), func(err error) tea.Msg {
		defer func() { _ = os.Remove(path) }()
		if err != nil {
			return editorDoneMsg{err: err}
		}
		text, err := readComposerTemp(path)
		return editorDoneMsg{text: text, err: err}
	})
}

// editorCommand starts the user's editor command with the temp path as a shell
// positional argument, so a path with spaces or metacharacters stays literal.
func editorCommand(editor, path string) *exec.Cmd {
	return exec.CommandContext(context.Background(), "/bin/sh", "-c", `exec `+editor+` "$1"`, "aharness-editor", path)
}

func writeComposerTemp(text string) (string, error) {
	f, err := os.CreateTemp("", "aharness-compose-*.md")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := io.WriteString(f, text); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// readComposerTemp loads an edited draft under the submission cap. Editors
// conventionally terminate the file with one newline; that final newline is
// not part of the composer value. The raw file may be larger than the result
// after CRLF normalization, so it is bounded before the normalized cap check.
func readComposerTemp(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	// A normalized draft of exactly maxSubmissionBytes can occupy at most
	// 2*maxSubmissionBytes CRLF pairs plus a final newline.
	const limit = maxSubmissionBytes*2 + 3
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return "", err
	}
	if len(data) >= limit {
		return "", errEditedMessageTooLarge
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	text = Sanitize(text)
	if len(text) > maxSubmissionBytes {
		return "", errEditedMessageTooLarge
	}
	return text, nil
}
