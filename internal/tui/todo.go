package tui

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"aharness/internal/tools"
)

// maxTodoWidgetRows bounds the persistent checklist widget, including its
// header row. The transcript keeps the remaining rows.
const maxTodoWidgetRows = 8

// todoDetails is the structured payload the todo tool records in a successful
// result. The widget reads it instead of parsing the rendered checklist.
type todoDetails struct {
	Items []tools.TodoItem `json:"items"`
}

// todoFromDetails decodes a todo tool result's details. It reports false when
// the payload is absent or malformed, in which case the widget keeps its
// previous list.
func todoFromDetails(raw json.RawMessage) ([]tools.TodoItem, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var details todoDetails
	if err := jsonv2.Unmarshal(raw, &details); err != nil {
		return nil, false
	}
	return details.Items, true
}

// setTodos installs the checklist shown by the widget. An empty list hides it.
// A changed panel height resizes the transcript viewport immediately.
func (m *App) setTodos(items []tools.TodoItem) {
	items = cloneTodoItems(items)
	if slices.Equal(m.todos, items) {
		return
	}
	previous := m.todoHeight()
	m.todos = items
	m.dirty = true
	if m.todoHeight() != previous {
		m.layout()
		m.ensureContent()
	}
}

func cloneTodoItems(items []tools.TodoItem) []tools.TodoItem {
	if len(items) == 0 {
		return nil
	}
	return append([]tools.TodoItem(nil), items...)
}

// todoHeight is the number of base-frame rows the widget occupies. Zero hides
// it: no checklist, no room, or an open slash-command menu, which keeps its own
// transient layout above the composer.
func (m *App) todoHeight() int {
	if len(m.todos) == 0 || m.height <= 0 || m.suggestionsVisible() {
		return 0
	}
	// Keep the header line, the status line, the composer, and at least one
	// transcript row.
	available := m.height - 1 - m.editorHeight() - 1 - 1
	if available < 2 {
		return 0
	}
	return min(1+len(m.todos), maxTodoWidgetRows, available)
}

// todoView renders the persistent checklist above the composer. It renders
// exactly todoHeight rows; one line per item keeps the layout independent of
// item length, and the full item text remains in the tool transcript.
func (m *App) todoView() string {
	rows := m.todoHeight()
	if rows == 0 || m.width <= 0 {
		return ""
	}
	t := newTheme(m.noColor)
	visible := rows - 1
	start, end := todoWindow(m.todos, visible)
	done := 0
	for _, item := range m.todos {
		if item.Status == tools.TodoCompleted {
			done++
		}
	}
	summary := fmt.Sprintf(" · %d/%d done", done, len(m.todos))
	if hidden := len(m.todos) - (end - start); hidden > 0 {
		summary += fmt.Sprintf(" · %d more", hidden)
	}
	label := "todo"
	summary = ansi.Truncate(summary, max(0, m.width-lipgloss.Width(label)), "…")
	lines := make([]string, 0, rows)
	lines = append(lines, t.label.Render(label)+t.dim.Render(summary))
	for _, item := range m.todos[start:end] {
		marker, style := todoItemStyle(t, item.Status)
		content := SanitizeLine(item.Content)
		prefix := "  " + marker + " "
		content = ansi.Truncate(content, max(1, m.width-lipgloss.Width(prefix)), "…")
		lines = append(lines, "  "+style.Render(marker)+" "+style.Render(content))
	}
	return strings.Join(lines, "\n")
}

// todoItemStyle maps a checklist status to its marker and text style.
func todoItemStyle(t uiTheme, status tools.TodoStatus) (string, lipgloss.Style) {
	switch status {
	case tools.TodoInProgress:
		return "[~]", t.title
	case tools.TodoCompleted:
		return "[x]", t.dim
	default:
		return "[ ]", t.header
	}
}

// todoWindow selects the visible item slice. It prefers the in-progress item,
// then the first pending item, so the active step stays visible when the list
// is longer than the widget.
func todoWindow(items []tools.TodoItem, limit int) (int, int) {
	if limit <= 0 {
		return 0, 0
	}
	if len(items) <= limit {
		return 0, len(items)
	}
	focus, found := 0, false
	for _, status := range []tools.TodoStatus{tools.TodoInProgress, tools.TodoPending} {
		for i, item := range items {
			if item.Status == status {
				focus, found = i, true
				break
			}
		}
		if found {
			break
		}
	}
	start := min(max(0, focus-limit/2), len(items)-limit)
	return start, start + limit
}
