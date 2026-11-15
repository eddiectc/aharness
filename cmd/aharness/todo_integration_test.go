//go:build unix

package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chatTodoToolCallResponse() map[string]any {
	return map[string]any{
		"id": "chatcmpl-todo", "object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_todo", "type": "function",
					"function": map[string]any{
						"name":      "todo",
						"arguments": `{"intent":"plan the work","items":[{"content":"write tests","status":"in_progress"},{"content":"run tests","status":"pending"}]}`,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
	}
}

func chatTodoReadResponse() map[string]any {
	return map[string]any{
		"id": "chatcmpl-todo-read", "object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_todo_read", "type": "function",
					"function": map[string]any{"name": "todo", "arguments": `{"intent":"read the plan"}`},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
	}
}

func TestBinaryTodoRestoresOnResume(t *testing.T) {
	bin := buildHarness(t)
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")

	first := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatTodoToolCallResponse())
			return
		}
		writeJSON(w, 200, chatFinalResponse("planned"))
	})
	run := runHarness(t, bin, workspace, stateDir, first.url,
		"-task", "plan the work", "-model", "deepseek-v4.1-flash", "-max-turns", "5")
	if run.exit != 0 || run.id == "" {
		t.Fatalf("exit = %d id = %q\nstderr:\n%s", run.exit, run.id, run.stderr)
	}

	resumed := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		switch call {
		case 1:
			writeJSON(w, 200, chatTodoReadResponse())
		case 2:
			// The read runs in a new process with an empty registry; only the
			// recorded details can supply the list.
			for _, want := range []string{"[~] write tests", "[ ] run tests"} {
				if !bytes.Contains(body, []byte(want)) {
					t.Errorf("resumed read result missing %q:\n%s", want, body)
				}
			}
			writeJSON(w, 200, chatFinalResponse("resumed"))
		}
	})
	out := runHarness(t, bin, workspace, stateDir, resumed.url,
		"-resume", run.id, "-task", "continue", "-model", "deepseek-v4.1-flash", "-max-turns", "5")
	if out.exit != 0 {
		t.Fatalf("resume exit = %d\nstderr:\n%s", out.exit, out.stderr)
	}
	if !strings.Contains(out.stdout, "resumed") {
		t.Errorf("stdout = %q, want the resumed final text", out.stdout)
	}
	if !strings.Contains(out.log, `\n[~] write tests\n[ ] run tests\n`) {
		t.Errorf("session log does not record the restored checklist:\n%s", out.log)
	}
}

func TestBinaryTodo(t *testing.T) {
	bin := buildHarness(t)
	workspace := t.TempDir()
	provider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatTodoToolCallResponse())
			return
		}
		for _, want := range []string{"Todo list updated", "[~] write tests", "[ ] run tests"} {
			if !bytes.Contains(body, []byte(want)) {
				t.Errorf("second provider request missing %q:\n%s", want, body)
			}
		}
		writeJSON(w, 200, chatFinalResponse("todo done"))
	})

	run := runHarness(t, bin, workspace, filepath.Join(t.TempDir(), "state"), provider.url,
		"-task", "plan the work", "-model", "deepseek-v4.1-flash", "-max-turns", "5")
	if run.exit != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stdout, "todo done") {
		t.Errorf("stdout = %q, want the final assistant text", run.stdout)
	}
	for _, want := range []string{
		`"type":"tool_result"`,
		`"name":"todo"`,
		`"code":"ok"`,
		`"output":"Todo list updated (2 items: 1 pending, 1 in progress, 0 completed)\n[~] write tests\n[ ] run tests\n"`,
	} {
		if !strings.Contains(run.log, want) {
			t.Errorf("session log missing %q:\n%s", want, run.log)
		}
	}
	if got := strings.Count(run.log, `"type":"tool_result"`); got != 1 {
		t.Errorf("tool_result events = %d, want 1", got)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("todo wrote to the workspace: %v", entries)
	}
}

// sseTodoToolCall streams one todo tool call with two checklist items.
func sseTodoToolCall(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_todo","type":"function","function":{"name":"todo","arguments":""}}]}}]}`)
	writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"intent\":\"plan the work\",\"items\":[{\"content\":\"write tests\",\"status\":\"in_progress\"},{\"content\":\"run tests\",\"status\":\"pending\"}]}"}}]}}]}`)
	writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	writeSSE(t, w, "", `{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
	writeSSE(t, w, "", `[DONE]`)
}

// TestBinaryUIJSONTodoWidget proves a completed todo call updates the
// persistent widget in a frame, and that a resumed process restores the widget
// from the session log before any new tool call.
func TestBinaryUIJSONTodoWidget(t *testing.T) {
	bin := buildHarness(t)
	workspace := t.TempDir()
	stateDir := t.TempDir()
	provider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		switch call {
		case 1:
			sseTodoToolCall(t, w)
		case 2:
			sseFinal(t, w, "planned")
		default:
			t.Errorf("unexpected provider call %d", call)
			http.Error(w, "unexpected call", http.StatusInternalServerError)
		}
	})
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"plan the work"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, workspace, stateDir, provider.url, strings.NewReader(script),
		"-ui-json", "-ui-size", "80x24", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	frame := lastFrameText(t, stdout)
	for _, want := range []string{"todo · 0/2 done", "[~] write tests", "[ ] run tests"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame lacks %q:\n%s", want, frame)
		}
	}
	files := sessionFiles(t, stateDir)
	if len(files) != 1 {
		t.Fatalf("session files = %v", files)
	}
	id := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")

	resumedScript := strings.Join([]string{
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	resumed, resumedErr, resumedExit := runHarnessRaw(t, bin, workspace, stateDir, provider.url,
		strings.NewReader(resumedScript), "-ui-json", "-resume", id, "-ui-size", "80x24")
	if resumedExit != 0 {
		t.Fatalf("resume exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", resumedExit, resumed, resumedErr)
	}
	if frame := lastFrameText(t, resumed); !strings.Contains(frame, "todo · 0/2 done") {
		t.Fatalf("resumed frame does not restore the widget:\n%s", frame)
	}
}
