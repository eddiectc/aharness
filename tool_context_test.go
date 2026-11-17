package aharness_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/eddiectc/aharness"
)

func TestToolExecutionContextAndPolicyPath(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()

	var (
		mu      sync.Mutex
		toolRun []aharness.ToolCall
		policy  []aharness.ToolRequest
	)
	recordTool := func(call aharness.ToolCall) {
		mu.Lock()
		defer mu.Unlock()
		toolRun = append(toolRun, call)
	}
	recordPolicy := func(req aharness.ToolRequest) {
		mu.Lock()
		defer mu.Unlock()
		policy = append(policy, req)
	}

	parent := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls:    []aharness.ToolCall{{ID: "parent-lookup", Name: "lookup", Arguments: json.RawMessage(`{"id":"parent"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "write",
				Name:      "edit_file",
				Arguments: json.RawMessage(`{"intent":"write","path":"report.txt","old_string":"","new_string":"hello"}`),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{
			ToolCalls:    []aharness.ToolCall{{ID: "search", Name: "search", Arguments: json.RawMessage(`{"pattern":"hello"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "delegate",
				Name:      "delegate",
				Arguments: delegateArgs(map[string]string{"agent": "analyst", "task": "read the report"}),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	modelCfg := testModel(parent)
	modelCfg.NewCompleter = func(string) aharness.Model { return &contextChildModel{} }

	toolsCfg := &aharness.ToolsConfig{
		Extra: []aharness.Tool{{
			Name:        "lookup",
			Description: "Look up one record.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
			Run: func(_ context.Context, call aharness.ToolCall) (aharness.ToolResult, error) {
				recordTool(call)
				return aharness.TextResult("ok"), nil
			},
		}},
		Delegation: &aharness.DelegationConfig{Agents: []aharness.AgentDefinition{{
			Name:        "analyst",
			Description: "Reads and reports.",
			Prompt:      "Read the report and answer.",
			Tools:       []aharness.ToolName{"read_file", "lookup"},
		}}},
	}

	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		Model:     modelCfg,
		Tools:     toolsCfg,
		Authorize: func(_ context.Context, req aharness.ToolRequest) error {
			recordPolicy(req)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()

	result, err := ag.Run(context.Background(), "use the tools", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != aharness.StopCompleted {
		t.Fatalf("status = %q (err %v)", result.Status, result.Err)
	}

	var parentLookup, childLookup *aharness.ToolCall
	for i := range toolRun {
		call := &toolRun[i]
		switch call.Agent {
		case "":
			parentLookup = call
		case "analyst":
			childLookup = call
		}
	}
	if parentLookup == nil || parentLookup.Session != ag.ID() {
		t.Fatalf("parent tool context = %+v, want session %s", parentLookup, ag.ID())
	}
	if childLookup == nil || childLookup.Session == "" || childLookup.Session == ag.ID() {
		t.Fatalf("child tool context = %+v, want a distinct child session", childLookup)
	}
	childSession := childLookup.Session

	var sawParentEdit, sawSearch, sawChildRead bool
	for _, req := range policy {
		if req.Call.Session != req.Session || req.Call.Agent != req.Agent {
			t.Fatalf("policy call context %q/%q differs from request %q/%q",
				req.Call.Session, req.Call.Agent, req.Session, req.Agent)
		}
		switch {
		case req.Tool == aharness.ToolEditFile && req.Agent == "":
			sawParentEdit = true
			if req.Path != filepath.Join(workspace, "report.txt") {
				t.Fatalf("edit_file path = %q, want %q", req.Path, filepath.Join(workspace, "report.txt"))
			}
		case req.Tool == aharness.ToolSearch && req.Agent == "":
			sawSearch = true
			if req.Path != workspace {
				t.Fatalf("search default path = %q, want workspace %q", req.Path, workspace)
			}
		case req.Tool == aharness.ToolReadFile && req.Agent == "analyst":
			sawChildRead = true
			if req.Session != childSession || req.Path != filepath.Join(workspace, "report.txt") {
				t.Fatalf("child read policy = session %q path %q", req.Session, req.Path)
			}
		case req.Tool == aharness.ToolDelegate:
			if req.Path != "" {
				t.Fatalf("delegate path = %q, want empty", req.Path)
			}
		}
	}
	if !sawParentEdit || !sawSearch || !sawChildRead {
		t.Fatalf("policy observations: parent edit=%v search=%v child read=%v", sawParentEdit, sawSearch, sawChildRead)
	}
}

type contextChildModel struct {
	mu    sync.Mutex
	calls int
}

func (m *contextChildModel) Complete(_ context.Context, _ aharness.Request) (aharness.Response, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	switch call {
	case 1:
		return aharness.Response{
			ToolCalls:    []aharness.ToolCall{{ID: "child-read", Name: "read_file", Arguments: json.RawMessage(`{"path":"report.txt"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		}, nil
	case 2:
		return aharness.Response{
			ToolCalls:    []aharness.ToolCall{{ID: "child-lookup", Name: "lookup", Arguments: json.RawMessage(`{"id":"child"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		}, nil
	default:
		return aharness.Response{Text: "child done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
	}
}
