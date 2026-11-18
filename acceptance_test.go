package aharness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eddiectc/aharness"
)

func deterministicTool(name, description string, safe bool) aharness.Tool {
	return aharness.Tool{
		Name:            name,
		Description:     description,
		InputSchema:     json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":false}`),
		ConcurrencySafe: safe,
		Run: func(_ context.Context, call aharness.ToolCall) (aharness.ToolResult, error) {
			return aharness.ToolResult{OK: true, Code: "ok", Output: name + ":ok"}, nil
		},
	}
}

func TestResearchAcceptanceShape(t *testing.T) {
	called := 0
	tools := &aharness.ToolsConfig{
		Disable: []aharness.ToolName{aharness.ToolRunCommand, aharness.ToolEditFile},
		Extra: []aharness.Tool{
			deterministicTool("list_datasets", "List the workspace data sets.", false),
			deterministicTool("read_dataset", "Read one data set.", true),
			deterministicTool("summarize_table", "Summarize one table.", true),
			deterministicTool("validate_report", "Validate one report claim.", false),
		},
		Delegation: &aharness.DelegationConfig{
			Agents: []aharness.AgentDefinition{
				{Name: "data-reader", Description: "Read data.", Prompt: "Read the data and report.", Tools: []aharness.ToolName{"read_file", "search", "read_dataset"}},
				{Name: "summarizer", Description: "Summarize data.", Prompt: "Summarize the data.", Tools: []aharness.ToolName{"read_file", "summarize_table"}},
				{Name: "verifier", Description: "Verify claims.", Prompt: "Verify the report.", Tools: []aharness.ToolName{"read_file", "search"}},
			},
		},
	}
	model := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "list-1",
				Name:      "list_datasets",
				Arguments: json.RawMessage(`{"id":"all"}`),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 4, OutputTokens: 2, Reported: true},
		},
		{Text: "report written", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 6, OutputTokens: 3, Reported: true}},
	}}
	prompt := "You are a data-workspace research agent. Files in, files out. Never use a shell."
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace:    t.TempDir(),
		StateDir:     t.TempDir(),
		UserAgent:    "aharness-test/1.0",
		Model:        testModel(model),
		Tools:        tools,
		SystemPrompt: prompt,
		Authorize: func(_ context.Context, req aharness.ToolRequest) error {
			if req.Tool == aharness.ToolRunCommand {
				return &aharness.Deny{Reason: "this host has no shell"}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	result, err := ag.Run(context.Background(), "produce the report", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != aharness.StopCompleted || result.FinalText != "report written" {
		t.Fatalf("result = %+v", result)
	}
	state, err := ag.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.SystemPrompt != prompt {
		t.Fatalf("recorded prompt = %q", state.SystemPrompt)
	}
	enabled := make(map[aharness.ToolName]bool)
	for _, name := range state.Tools {
		enabled[name] = true
	}
	for _, want := range []aharness.ToolName{"read_file", "search", "list_datasets", "read_dataset"} {
		if !enabled[want] {
			t.Fatalf("grant missing %q: %v", want, state.Tools)
		}
	}
	for _, want := range []aharness.ToolName{aharness.ToolRunCommand, aharness.ToolEditFile} {
		if enabled[want] {
			t.Fatalf("disabled tool %q is in the grant", want)
		}
	}
	called++
	_ = called
}

// renderTranscript is the UI-contract renderer: it builds a transcript from
// Events and Result alone.
type transcript struct {
	text     strings.Builder
	tools    []string
	todos    []string
	children []string
	usage    aharness.Usage
	cost     int64
	status   aharness.StopReason
}

func renderTranscript(events []aharness.Event, result aharness.Result) transcript {
	var out transcript
	for _, ev := range events {
		switch ev := ev.(type) {
		case aharness.TextDelta:
			out.text.WriteString(ev.Text)
		case aharness.ResponseCompleted:
			if ev.Text != "" {
				out.text.WriteString(ev.Text)
			}
		case aharness.ToolCompleted:
			out.tools = append(out.tools, fmt.Sprintf("%s:%s", ev.Name, ev.Code))
			if len(ev.Details) > 0 {
				var details struct {
					Items []struct {
						Content string `json:"content"`
						Status  string `json:"status"`
					} `json:"items"`
				}
				if json.Unmarshal(ev.Details, &details) == nil {
					for _, item := range details.Items {
						out.todos = append(out.todos, item.Status+" "+item.Content)
					}
				}
			}
		case aharness.SubagentStarted:
			out.children = append(out.children, ev.Agent+"->"+ev.ChildSession)
		case aharness.SubagentFinished:
			out.children = append(out.children, ev.Agent+"="+ev.Status)
		}
	}
	out.usage = result.RunUsage
	out.cost = result.RunCostMicroUSD
	out.status = result.Status
	return out
}

func TestPublicTranscriptRendererNeedsOnlyEventsAndResult(t *testing.T) {
	now := time.Now()
	details := json.RawMessage(`{"items":[{"content":"collect data","status":"completed"},{"content":"write report","status":"in_progress"}]}`)
	events := []aharness.Event{
		aharness.RunStarted{Time: now, Session: "s", Model: "m"},
		aharness.ResponseCompleted{Time: now, Session: "s", Text: "analysis done"},
		aharness.ToolCompleted{Time: now, Session: "s", Name: "todo", OK: true, Code: "ok", Details: details},
		aharness.SubagentStarted{Time: now, Session: "s", Agent: "reviewer", ChildSession: "c1"},
		aharness.SubagentFinished{Time: now, Session: "s", Agent: "reviewer", ChildSession: "c1", Status: "completed"},
	}
	result := aharness.Result{
		Status:          aharness.StopCompleted,
		Session:         "s",
		FinalText:       "analysis done",
		RunUsage:        aharness.Usage{InputTokens: 10, OutputTokens: 5, Reported: true},
		RunCostMicroUSD: 1234,
	}
	out := renderTranscript(events, result)
	if out.text.String() != "analysis done" {
		t.Fatalf("text = %q", out.text.String())
	}
	if len(out.tools) != 1 || out.tools[0] != "todo:ok" {
		t.Fatalf("tools = %v", out.tools)
	}
	if len(out.todos) != 2 || out.todos[1] != "in_progress write report" {
		t.Fatalf("todos = %v", out.todos)
	}
	if len(out.children) != 2 {
		t.Fatalf("children = %v", out.children)
	}
	if out.usage.Total() != 15 || out.cost != 1234 || out.status != aharness.StopCompleted {
		t.Fatalf("accounting = %+v", out)
	}
}
