package tools

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"
	"sync"
	"unicode"
)

const (
	// maxTodoItems bounds one checklist so a model cannot grow the per-run
	// state or the rendered result without limit.
	maxTodoItems = 50
	// maxTodoContentBytes bounds one item. Fifty maximal items plus the header
	// and status markers stay under fileOutputCap.
	maxTodoContentBytes = 500
)

// TodoStatus is the lifecycle state of one checklist item.
type TodoStatus string

const (
	// TodoPending is an item that has not started.
	TodoPending TodoStatus = "pending"
	// TodoInProgress is the single item currently being worked on.
	TodoInProgress TodoStatus = "in_progress"
	// TodoCompleted is a finished item.
	TodoCompleted TodoStatus = "completed"
)

// TodoItem is one checklist entry. It is both the argument shape and the
// durably recorded state inside a todo tool result's details.
type TodoItem struct {
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
}

// todoDetails is the structured form of a todo result. Recording it lets a
// later run in the same session restore the checklist without parsing the
// rendered text; an empty list is encoded as [] so a clear stays
// distinguishable from an absent result.
type todoDetails struct {
	Items []TodoItem `json:"items"`
}

// todoArgs is the typed argument shape for todo. It must agree with the todo
// schema in schemas.go. A nil Items means read the current list; a non-nil
// empty slice clears it.
type todoArgs struct {
	Intent string      `json:"intent,omitempty"`
	Items  *[]TodoItem `json:"items,omitempty"`
}

// todoState owns the checklist for one registry. The runner executes tool
// calls sequentially, but the mutex keeps the state race-free if a future
// caller runs calls concurrently.
type todoState struct {
	mu    sync.Mutex
	items []TodoItem
}

func (s *todoState) snapshot() []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTodoItems(s.items)
}

func (s *todoState) replace(items []TodoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = cloneTodoItems(items)
}

// cloneTodoItems returns a non-nil copy so an empty list encodes as [] in the
// result details.
func cloneTodoItems(items []TodoItem) []TodoItem {
	out := make([]TodoItem, len(items))
	copy(out, items)
	return out
}

// SeedTodo restores the checklist recorded by an earlier run in the same
// session. cmd calls it at run start with the value from agent.RestoreTodo;
// tools never read the session log themselves. Items that fail validation are
// ignored rather than failing the run: the checklist is advisory state, and
// agent.RestoreTodo only returns details this package wrote.
func (r *Registry) SeedTodo(items []TodoItem) {
	valid, err := validateTodoItems(items)
	if err != nil {
		return
	}
	r.todos.replace(valid)
}

// ParseTodoDetails decodes the structured todo result payload. A malformed or
// foreign payload is reported as absent rather than failing replay.
func ParseTodoDetails(raw json.RawMessage) ([]TodoItem, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var details todoDetails
	if err := jsonv2.Unmarshal(raw, &details, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, false
	}
	return details.Items, true
}

// executeTodo validates and runs one todo call.
func (r *Registry) executeTodo(ctx context.Context, args json.RawMessage) Result {
	if err := ctx.Err(); err != nil {
		return errorResult(err)
	}
	var a todoArgs
	if err := decodeArgs("todo", args, &a); err != nil {
		return Failure(CodeInvalidArgs, err.Error())
	}
	if a.Items == nil {
		return todoResult(r.todos.snapshot(), false)
	}
	items, err := validateTodoItems(*a.Items)
	if err != nil {
		return invalidArgs("todo", "%v", err)
	}
	r.todos.replace(items)
	return todoResult(items, true)
}

// todoResult renders the checklist and records the same items in details.
func todoResult(items []TodoItem, updated bool) Result {
	details, err := jsonv2.Marshal(todoDetails{Items: cloneTodoItems(items)})
	if err != nil {
		return Failure(CodeInternal, sanitizeUTF8(fmt.Sprintf("encode todo details: %v", err)))
	}
	return Result{OK: true, Code: CodeOK, Output: renderTodo(items, updated), Details: details}
}

// validateTodoItems normalizes content, enforces the status enum and limits,
// and rejects more than one in-progress item so the checklist has one clear
// focus.
func validateTodoItems(raw []TodoItem) ([]TodoItem, error) {
	if len(raw) > maxTodoItems {
		return nil, fmt.Errorf("%d items exceeds the limit of %d", len(raw), maxTodoItems)
	}
	items := make([]TodoItem, len(raw))
	inProgress := 0
	for i, item := range raw {
		content := normalizeTodoContent(item.Content)
		if content == "" {
			return nil, fmt.Errorf("item %d: content is required", i+1)
		}
		if len(content) > maxTodoContentBytes {
			return nil, fmt.Errorf("item %d: content is %d bytes; the limit is %d", i+1, len(content), maxTodoContentBytes)
		}
		switch item.Status {
		case TodoPending, TodoInProgress, TodoCompleted:
		default:
			return nil, fmt.Errorf("item %d: status must be pending, in_progress, or completed, got %q", i+1, item.Status)
		}
		if item.Status == TodoInProgress {
			inProgress++
			if inProgress > 1 {
				return nil, fmt.Errorf("item %d: at most one item may be in_progress", i+1)
			}
		}
		items[i] = TodoItem{Content: content, Status: item.Status}
	}
	return items, nil
}

// normalizeTodoContent keeps one safe line per item: valid UTF-8, controls
// replaced with spaces, and whitespace collapsed.
func normalizeTodoContent(s string) string {
	s = sanitizeUTF8(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// renderTodo builds the model-visible checklist. Every call returns the full
// list so a read needs no separate tool and the model sees the canonical state
// after a replacement.
func renderTodo(items []TodoItem, updated bool) string {
	if len(items) == 0 {
		if updated {
			return "Todo list cleared.\n"
		}
		return "Todo list is empty.\n"
	}
	var pending, inProgress, completed int
	for _, item := range items {
		switch item.Status {
		case TodoPending:
			pending++
		case TodoInProgress:
			inProgress++
		case TodoCompleted:
			completed++
		}
	}
	heading := "Todo list"
	if updated {
		heading = "Todo list updated"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d %s: %d pending, %d in progress, %d completed)\n",
		heading, len(items), pluralItems(len(items)), pending, inProgress, completed)
	for _, item := range items {
		b.WriteString(todoMarker(item.Status))
		b.WriteByte(' ')
		b.WriteString(item.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func pluralItems(n int) string {
	if n == 1 {
		return "item"
	}
	return "items"
}

func todoMarker(status TodoStatus) string {
	switch status {
	case TodoInProgress:
		return "[~]"
	case TodoCompleted:
		return "[x]"
	default:
		return "[ ]"
	}
}
