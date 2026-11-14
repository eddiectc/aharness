package tools

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTodoLifecycle(t *testing.T) {
	r, _ := newTestRegistry(t)

	res := executeArgs(t, r, "todo", map[string]any{"intent": "check the plan"})
	if !res.OK || res.Code != CodeOK {
		t.Fatalf("read empty todo = ok=%v code=%s error=%q", res.OK, res.Code, res.Error)
	}
	if got, want := res.Output, "Todo list is empty.\n"; got != want {
		t.Errorf("empty output = %q, want %q", got, want)
	}

	res = executeArgs(t, r, "todo", map[string]any{
		"intent": "set the plan",
		"items": []map[string]any{
			{"content": "Read the docs", "status": "completed"},
			{"content": "Write the tool", "status": "in_progress"},
			{"content": "Run the tests", "status": "pending"},
		},
	})
	if !res.OK {
		t.Fatalf("set todo = ok=false code=%s error=%q", res.Code, res.Error)
	}
	wantSet := "Todo list updated (3 items: 1 pending, 1 in progress, 1 completed)\n" +
		"[x] Read the docs\n" +
		"[~] Write the tool\n" +
		"[ ] Run the tests\n"
	if res.Output != wantSet {
		t.Errorf("set output = %q, want %q", res.Output, wantSet)
	}

	res = executeArgs(t, r, "todo", map[string]any{"intent": "re-read the plan"})
	if !res.OK {
		t.Fatalf("read todo = ok=false code=%s error=%q", res.Code, res.Error)
	}
	wantRead := strings.Replace(wantSet, "Todo list updated", "Todo list", 1)
	if res.Output != wantRead {
		t.Errorf("read output = %q, want %q", res.Output, wantRead)
	}

	res = executeArgs(t, r, "todo", map[string]any{"intent": "clear the plan", "items": []any{}})
	if !res.OK {
		t.Fatalf("clear todo = ok=false code=%s error=%q", res.Code, res.Error)
	}
	if got, want := res.Output, "Todo list cleared.\n"; got != want {
		t.Errorf("clear output = %q, want %q", got, want)
	}
	res = executeArgs(t, r, "todo", map[string]any{"intent": "check the plan"})
	if got, want := res.Output, "Todo list is empty.\n"; got != want {
		t.Errorf("output after clear = %q, want %q", got, want)
	}
}

func TestTodoNormalizesContent(t *testing.T) {
	r, _ := newTestRegistry(t)
	res := executeArgs(t, r, "todo", map[string]any{
		"intent": "set the plan",
		"items": []map[string]any{
			{"content": "  read\n\tthe\r\ndocs  ", "status": "pending"},
			{"content": "write\x00the\x1btool", "status": "pending"},
		},
	})
	if !res.OK {
		t.Fatalf("set todo = ok=false code=%s error=%q", res.Code, res.Error)
	}
	for _, want := range []string{"[ ] read the docs\n", "[ ] write the tool\n"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("normalized output missing %q: %q", want, res.Output)
		}
	}
	if strings.ContainsAny(res.Output, "\x00\x1b") {
		t.Errorf("normalized output contains a control character: %q", res.Output)
	}
}

func TestTodoRejectsInvalid(t *testing.T) {
	tooMany := make([]map[string]any, maxTodoItems+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"content": "task", "status": "pending"}
	}
	tooManyJSON, err := jsonv2.Marshal(tooMany)
	if err != nil {
		t.Fatalf("marshal the oversized item list: %v", err)
	}
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "items is not an array",
			raw:  `{"items":"one"}`,
			want: "unmarshal",
		},
		{
			name: "unknown item member",
			raw:  `{"items":[{"content":"a","status":"pending","extra":1}]}`,
			want: "unknown object member",
		},
		{
			name: "missing content",
			raw:  `{"items":[{"status":"pending"}]}`,
			want: "item 1: content is required",
		},
		{
			name: "blank content",
			raw:  `{"items":[{"content":" \n\t ","status":"pending"}]}`,
			want: "item 1: content is required",
		},
		{
			name: "content too long",
			raw:  `{"items":[{"content":"` + strings.Repeat("a", maxTodoContentBytes+1) + `","status":"pending"}]}`,
			want: "the limit is 500",
		},
		{
			name: "invalid utf8",
			raw:  "{\"items\":[{\"content\":\"\xff\",\"status\":\"pending\"}]}",
			want: "UTF-8",
		},
		{
			name: "unknown status",
			raw:  `{"items":[{"content":"a","status":"done"}]}`,
			want: "status must be pending, in_progress, or completed",
		},
		{
			name: "missing status",
			raw:  `{"items":[{"content":"a"}]}`,
			want: "status must be pending, in_progress, or completed",
		},
		{
			name: "two in progress",
			raw:  `{"items":[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]}`,
			want: "item 2: at most one item may be in_progress",
		},
		{
			name: "too many items",
			raw:  `{"items":` + string(tooManyJSON) + `}`,
			want: "exceeds the limit of 50",
		},
	}
	r, _ := newTestRegistry(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := executeRaw(t, r, "todo", tc.raw)
			if res.OK || res.Code != CodeInvalidArgs {
				t.Fatalf("todo(%s) = ok=%v code=%s, want invalid_args", tc.raw, res.OK, res.Code)
			}
			if !strings.Contains(res.Error, "todo") || !strings.Contains(res.Error, tc.want) {
				t.Errorf("error = %q, want it to name todo and contain %q", res.Error, tc.want)
			}
		})
	}
}

func TestTodoInvalidUpdateKeepsPreviousList(t *testing.T) {
	r, _ := newTestRegistry(t)
	set := executeArgs(t, r, "todo", map[string]any{
		"intent": "set the plan",
		"items":  []map[string]any{{"content": "keep me", "status": "pending"}},
	})
	if !set.OK {
		t.Fatalf("set todo = ok=false code=%s error=%q", set.Code, set.Error)
	}
	bad := executeRaw(t, r, "todo", `{"items":[{"content":"a","status":"done"}]}`)
	if bad.OK || bad.Code != CodeInvalidArgs {
		t.Fatalf("invalid update = ok=%v code=%s, want invalid_args", bad.OK, bad.Code)
	}
	res := executeArgs(t, r, "todo", map[string]any{"intent": "read the plan"})
	want := "Todo list (1 item: 1 pending, 0 in progress, 0 completed)\n[ ] keep me\n"
	if res.Output != want {
		t.Errorf("output after invalid update = %q, want %q", res.Output, want)
	}
}

// decodeTodoDetails reads the structured state from a todo result.
func decodeTodoDetails(t *testing.T, res Result) []TodoItem {
	t.Helper()
	if len(res.Details) == 0 {
		t.Fatalf("todo result has no details: %+v", res)
	}
	var details struct {
		Items []TodoItem `json:"items"`
	}
	if err := jsonv2.Unmarshal(res.Details, &details); err != nil {
		t.Fatalf("decode todo details %s: %v", res.Details, err)
	}
	return details.Items
}

func TestTodoDetailsRoundTrip(t *testing.T) {
	r, _ := newTestRegistry(t)
	want := []TodoItem{
		{Content: "read the docs", Status: TodoCompleted},
		{Content: "write the tool", Status: TodoInProgress},
	}
	set := executeArgs(t, r, "todo", map[string]any{
		"intent": "set the plan",
		"items":  want,
	})
	if !set.OK {
		t.Fatalf("set todo = ok=false code=%s error=%q", set.Code, set.Error)
	}
	if got := decodeTodoDetails(t, set); !slices.Equal(got, want) {
		t.Errorf("set details = %+v, want %+v", got, want)
	}

	read := executeArgs(t, r, "todo", map[string]any{"intent": "read the plan"})
	if got := decodeTodoDetails(t, read); !slices.Equal(got, want) {
		t.Errorf("read details = %+v, want %+v", got, want)
	}

	clear := executeArgs(t, r, "todo", map[string]any{"intent": "clear the plan", "items": []any{}})
	if got := decodeTodoDetails(t, clear); len(got) != 0 || got == nil {
		t.Errorf("clear details = %+v (%v), want a non-nil empty list", got, got)
	}
	if !strings.Contains(string(clear.Details), `"items":[]`) {
		t.Errorf("clear details = %s, want an explicit empty array", clear.Details)
	}
}

func TestTodoSeedRestoresState(t *testing.T) {
	seed := []TodoItem{
		{Content: "read the docs", Status: TodoCompleted},
		{Content: "run the tests", Status: TodoPending},
	}
	r, _ := newTestRegistry(t)
	r.SeedTodo(seed)
	res := executeArgs(t, r, "todo", map[string]any{"intent": "read the plan"})
	if !res.OK {
		t.Fatalf("read todo = ok=false code=%s error=%q", res.Code, res.Error)
	}
	want := "Todo list (2 items: 1 pending, 0 in progress, 1 completed)\n[x] read the docs\n[ ] run the tests\n"
	if res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
	if got := decodeTodoDetails(t, res); !slices.Equal(got, seed) {
		t.Errorf("restored details = %+v, want %+v", got, seed)
	}

	// A seed that violates the invariants is ignored rather than failing the
	// run; the checklist simply stays empty.
	bad, _ := newTestRegistry(t)
	bad.SeedTodo([]TodoItem{
		{Content: "a", Status: TodoInProgress},
		{Content: "b", Status: TodoInProgress},
	})
	res = executeArgs(t, bad, "todo", map[string]any{"intent": "read the plan"})
	if got, want := res.Output, "Todo list is empty.\n"; got != want {
		t.Errorf("output after a rejected seed = %q, want %q", got, want)
	}
}

func TestTodoSchemaNestedShape(t *testing.T) {
	var doc struct {
		Properties struct {
			Items struct {
				MaxItems int `json:"maxItems"`
				Items    struct {
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				} `json:"items"`
			} `json:"items"`
		} `json:"properties"`
	}
	todo := findSchema(t, SchemasForMust(t, Version), "todo")
	if err := jsonv2.Unmarshal(todo.Parameters, &doc); err != nil {
		t.Fatalf("unmarshal todo schema: %v", err)
	}
	items := doc.Properties.Items
	if items.MaxItems != maxTodoItems {
		t.Errorf("maxItems = %d, want %d", items.MaxItems, maxTodoItems)
	}
	fields := jsonFieldNames(reflect.TypeOf(TodoItem{}))
	for name := range items.Items.Properties {
		if !fields[name] {
			t.Errorf("item schema property %q has no todoItem field", name)
		}
	}
	for name := range fields {
		if _, ok := items.Items.Properties[name]; !ok {
			t.Errorf("todoItem field %q is missing from the item schema", name)
		}
	}
	if got, want := strings.Join(items.Items.Required, ","), "content,status"; got != want {
		t.Errorf("item required = %q, want %q", got, want)
	}
}

func TestTodoStateIsPerRegistry(t *testing.T) {
	first, _ := newTestRegistry(t)
	second, _ := newTestRegistry(t)
	set := executeArgs(t, first, "todo", map[string]any{
		"intent": "set the plan",
		"items":  []map[string]any{{"content": "task", "status": "pending"}},
	})
	if !set.OK {
		t.Fatalf("set todo = ok=false code=%s error=%q", set.Code, set.Error)
	}
	res := executeArgs(t, second, "todo", map[string]any{"intent": "read the plan"})
	if got, want := res.Output, "Todo list is empty.\n"; got != want {
		t.Errorf("second registry output = %q, want %q", got, want)
	}
}

func TestTodoCanceled(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res := r.Execute(ctx, "todo", []byte(`{"intent":"read the plan"}`))
	if res.OK || res.Code != CodeCanceled {
		t.Errorf("canceled todo = ok=%v code=%s, want canceled", res.OK, res.Code)
	}
}
