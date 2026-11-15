package tui

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"aharness/internal/agent"
	"aharness/internal/tools"
)

func todoDetailsJSON(t *testing.T, items []tools.TodoItem) json.RawMessage {
	t.Helper()
	data, err := jsonv2.Marshal(todoDetails{Items: items})
	if err != nil {
		t.Fatalf("marshal todo details: %v", err)
	}
	return json.RawMessage(data)
}

func deliverTodo(t *testing.T, app *App, callID string, ok bool, items []tools.TodoItem) {
	t.Helper()
	deliver(app, agent.ToolCompleted{
		Turn:    1,
		CallID:  callID,
		Name:    "todo",
		OK:      ok,
		Code:    tools.CodeOK,
		Output:  "Todo list updated\n",
		Details: todoDetailsJSON(t, items),
	})
}

func finishTodoRun(t *testing.T, app *App, run *manualRun) {
	t.Helper()
	result := agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	run.done <- result
	close(run.done)
	finishRun(t, app, result)
}

func TestTodoWidgetRendersUpdatesAndHides(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "plan the work")

	items := []tools.TodoItem{
		{Content: "read the docs", Status: tools.TodoCompleted},
		{Content: "write the widget", Status: tools.TodoInProgress},
		{Content: "run tests", Status: tools.TodoPending},
	}
	deliverTodo(t, app, "call_todo", true, items)
	if !slices.Equal(app.todos, items) {
		t.Fatalf("todos = %+v, want %+v", app.todos, items)
	}
	frame := ansi.Strip(app.baseFrame())
	for _, want := range []string{
		"todo · 1/3 done",
		"[x] read the docs",
		"[~] write the widget",
		"[ ] run tests",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame lacks %q:\n%s", want, frame)
		}
	}
	if got, want := strings.Count(frame, "\n")+1, app.height; got != want {
		t.Fatalf("base frame lines = %d, want %d:\n%s", got, want, frame)
	}
	wantViewport := app.height - 1 - 1 - 1 - (1 + len(items))
	if got := app.viewport.Height(); got != wantViewport {
		t.Fatalf("viewport height = %d, want %d", got, wantViewport)
	}

	// An empty list clears the widget and restores the transcript rows.
	deliverTodo(t, app, "call_clear", true, nil)
	if len(app.todos) != 0 {
		t.Fatalf("todos after clear = %+v", app.todos)
	}
	if strings.Contains(ansi.Strip(app.baseFrame()), "1/3 done") {
		t.Fatalf("cleared widget still renders:\n%s", app.baseFrame())
	}
	if got, want := app.viewport.Height(), app.height-3; got != want {
		t.Fatalf("viewport height after clear = %d, want %d", got, want)
	}
	finishTodoRun(t, app, run)
}

func TestTodoWidgetRestoresFromHost(t *testing.T) {
	host := newManualHost()
	host.todos = []tools.TodoItem{{Content: "restored step", Status: tools.TodoPending}}
	app := newTestApp(t, host)
	if len(app.todos) != 1 || app.todos[0].Content != "restored step" {
		t.Fatalf("restored todos = %+v", app.todos)
	}
	if frame := ansi.Strip(app.baseFrame()); !strings.Contains(frame, "[ ] restored step") {
		t.Fatalf("frame lacks the restored item:\n%s", frame)
	}
}

func TestTodoWidgetBoundsRowsAndKeepsInProgressVisible(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "many steps")

	items := make([]tools.TodoItem, 20)
	for i := range items {
		items[i] = tools.TodoItem{Content: fmt.Sprintf("step %d", i+1), Status: tools.TodoPending}
	}
	items[15].Status = tools.TodoInProgress
	deliverTodo(t, app, "call_todo", true, items)

	if got := app.todoHeight(); got != maxTodoWidgetRows {
		t.Fatalf("widget height = %d, want the %d-row cap", got, maxTodoWidgetRows)
	}
	frame := ansi.Strip(app.baseFrame())
	if !strings.Contains(frame, "step 16") {
		t.Fatalf("in-progress item is not visible:\n%s", frame)
	}
	if !strings.Contains(frame, "more") {
		t.Fatalf("hidden items are not summarized:\n%s", frame)
	}
	if strings.Contains(frame, "step 1\n") || strings.Contains(frame, "step 20") {
		t.Fatalf("frame shows items outside the window:\n%s", frame)
	}
	if got, want := strings.Count(frame, "\n")+1, app.height; got != want {
		t.Fatalf("base frame lines = %d, want %d:\n%s", got, want, frame)
	}

	// At the minimum terminal height the widget keeps a transcript row and
	// still fits the frame exactly.
	app.setSize(40, 10)
	if got := app.todoHeight(); got < 2 || got > 6 {
		t.Fatalf("widget height at 40x10 = %d, want 2..6", got)
	}
	if got := app.viewport.Height(); got < 1 {
		t.Fatalf("transcript height at 40x10 = %d, want at least 1", got)
	}
	if got, want := strings.Count(app.baseFrame(), "\n")+1, 10; got != want {
		t.Fatalf("40x10 base frame lines = %d, want %d", got, want)
	}
	finishTodoRun(t, app, run)
}

func TestTodoWidgetHidesWhileCompletionMenuIsOpen(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "plan the work")
	deliverTodo(t, app, "call_todo", true, []tools.TodoItem{
		{Content: "write the widget", Status: tools.TodoInProgress},
	})
	if app.todoHeight() == 0 {
		t.Fatal("widget is hidden with an idle composer")
	}
	typeText(t, app, "/")
	if app.todoHeight() != 0 {
		t.Fatal("widget takes rows from the completion menu")
	}
	if frame := ansi.Strip(app.baseFrame()); strings.Contains(frame, "todo · 0/1 done") {
		t.Fatalf("frame shows the widget under the completion menu:\n%s", frame)
	}
	press(t, app, "esc")
	if app.todoHeight() == 0 {
		t.Fatal("widget did not return after dismissing the menu")
	}
	finishTodoRun(t, app, run)
}

func TestTodoWidgetIgnoresFailedAndForeignDetails(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "plan the work")
	deliverTodo(t, app, "call_todo", true, []tools.TodoItem{
		{Content: "keep me", Status: tools.TodoPending},
	})

	deliverTodo(t, app, "call_failed", false, []tools.TodoItem{
		{Content: "failed update", Status: tools.TodoInProgress},
	})
	if len(app.todos) != 1 || app.todos[0].Content != "keep me" {
		t.Fatalf("failed result changed the widget: %+v", app.todos)
	}

	deliver(app, agent.ToolCompleted{
		Turn: 1, CallID: "call_read", Name: "read_file", OK: true, Code: tools.CodeOK,
		Details: todoDetailsJSON(t, []tools.TodoItem{{Content: "foreign", Status: tools.TodoPending}}),
	})
	if len(app.todos) != 1 || app.todos[0].Content != "keep me" {
		t.Fatalf("foreign tool changed the widget: %+v", app.todos)
	}

	deliver(app, agent.ToolCompleted{
		Turn: 1, CallID: "call_bad", Name: "todo", OK: true, Code: tools.CodeOK,
		Details: json.RawMessage(`{"items":"not a list"}`),
	})
	if len(app.todos) != 1 || app.todos[0].Content != "keep me" {
		t.Fatalf("malformed details changed the widget: %+v", app.todos)
	}
	finishTodoRun(t, app, run)
}

func TestTodoWidgetReloadsOnSessionSwitch(t *testing.T) {
	host := &switchingHost{manualHost: newManualHost()}
	host.todos = []tools.TodoItem{{Content: "old step", Status: tools.TodoPending}}
	host.newSnapshot = SessionSnapshot{
		Info:   Info{SessionID: "0190a0c4-0000-7000-8000-0000000000cc", Workspace: "/tmp/ws"},
		Models: host.models,
	}
	app := newTestApp(t, host)
	if len(app.todos) != 1 || app.todos[0].Content != "old step" {
		t.Fatalf("initial todos = %+v", app.todos)
	}

	host.todos = []tools.TodoItem{{Content: "new step", Status: tools.TodoInProgress}}
	submitComposer(t, app, "/new")
	if len(app.todos) != 1 || app.todos[0].Content != "new step" {
		t.Fatalf("todos after switch = %+v", app.todos)
	}
	if frame := ansi.Strip(app.baseFrame()); !strings.Contains(frame, "[~] new step") {
		t.Fatalf("frame lacks the switched session's item:\n%s", frame)
	}
}

func TestTodoWindowPrefersTheActiveStep(t *testing.T) {
	items := make([]tools.TodoItem, 20)
	for i := range items {
		items[i] = tools.TodoItem{Content: fmt.Sprintf("step %d", i+1), Status: tools.TodoPending}
	}
	items[15].Status = tools.TodoInProgress

	start, end := todoWindow(items, 5)
	if start > 15 || end <= 15 {
		t.Fatalf("window = [%d,%d), want the in-progress item at 15 inside", start, end)
	}
	if end-start != 5 {
		t.Fatalf("window size = %d, want 5", end-start)
	}
	if start, end := todoWindow(items[:3], 5); start != 0 || end != 3 {
		t.Fatalf("short list window = [%d,%d), want [0,3)", start, end)
	}
	if start, end := todoWindow(items, 0); start != 0 || end != 0 {
		t.Fatalf("zero-width window = [%d,%d), want [0,0)", start, end)
	}
}
