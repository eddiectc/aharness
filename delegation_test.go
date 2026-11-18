package aharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eddiectc/aharness"
)

func delegateArgs(tasks ...map[string]string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"intent": "delegate research", "tasks": tasks})
	return raw
}

func TestDelegationFanOutAndChildSteering(t *testing.T) {
	parentModel := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "delegate-1",
				Name:      "delegate",
				Arguments: delegateArgs(map[string]string{"agent": "analyst", "task": "read the data"}),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true},
		},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true}},
	}}
	toolStarted := make(chan struct{})
	toolRelease := make(chan struct{})
	var toolOnce sync.Once
	modelCfg := testModel(parentModel)
	modelCfg.NewCompleter = func(string) aharness.Model {
		return &steeredChildModel{}
	}
	tools := &aharness.ToolsConfig{
		Extra: []aharness.Tool{{
			Name:        "blocking_read",
			Description: "Read after a steering window.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
			Run: func(_ context.Context, _ aharness.ToolCall) (aharness.ToolResult, error) {
				toolOnce.Do(func() { close(toolStarted) })
				<-toolRelease
				return aharness.TextResult("read ok"), nil
			},
		}},
		Delegation: &aharness.DelegationConfig{Agents: []aharness.AgentDefinition{{
			Name:        "analyst",
			Description: "Read-only data analyst.",
			Prompt:      "Read the assigned data and report.",
			Tools:       []aharness.ToolName{"blocking_read"},
		}}},
	}
	workspace := t.TempDir()
	stateDir := t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     modelCfg,
		Tools:     tools,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	run, err := ag.Start(context.Background(), "delegate", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var (
		childID     string
		execution   string
		childEvents int
		steerOnce   sync.Once
		finished    aharness.Result
	)
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		switch ev := ev.(type) {
		case aharness.SubagentStarted:
			childID = ev.ChildSession
			childEvents++
		case aharness.SubagentProgress:
			steerOnce.Do(func() {
				<-toolStarted
				if err := run.SteerChild(ev.ChildSession, "focus on the summary"); err != nil {
					t.Fatalf("SteerChild: %v", err)
				}
				close(toolRelease)
			})
		case aharness.SubagentFinished:
			childEvents++
			if ev.Status != "completed" {
				t.Fatalf("child status = %q", ev.Status)
			}
		case aharness.ToolCompleted:
			if ev.Name == "delegate" {
				if !ev.OK {
					t.Fatalf("delegate failed: %+v", ev)
				}
				var details struct {
					Execution string `json:"execution"`
				}
				if err := json.Unmarshal(ev.Details, &details); err != nil {
					t.Fatalf("delegate details: %v", err)
				}
				execution = details.Execution
			}
		case aharness.RunFinished:
			finished = ev.Result
		}
	}
	if finished.Status != aharness.StopCompleted {
		t.Fatalf("status = %q (err %v)", finished.Status, finished.Err)
	}
	if execution != "concurrent" && execution != "sequential" {
		t.Fatalf("execution = %q", execution)
	}
	if childID == "" {
		t.Fatal("no child session was observed")
	}
	if childEvents < 2 {
		t.Fatalf("child events = %d", childEvents)
	}
	childState, err := aharness.InspectSession(context.Background(), stateDir, childID)
	if err != nil {
		t.Fatalf("InspectSession child: %v", err)
	}
	if childState.Parent == nil || childState.Parent.Session != ag.ID() || childState.Parent.CallID == "" {
		t.Fatalf("child parent = %+v", childState.Parent)
	}
	if childState.Turns != 2 {
		t.Fatalf("child turns = %d, want 2 after steering", childState.Turns)
	}
	if err := run.SteerChild(childID, "after finish"); !errors.Is(err, aharness.ErrNoSuchChild) {
		t.Fatalf("SteerChild after finish = %v, want ErrNoSuchChild", err)
	}
}

// steeredChildModel calls one blocking tool, then answers after steering.
type steeredChildModel struct {
	mu    sync.Mutex
	calls int
}

func (m *steeredChildModel) Complete(_ context.Context, _ aharness.Request) (aharness.Response, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		return aharness.Response{
			ToolCalls:    []aharness.ToolCall{{ID: "child-tool", Name: "blocking_read", Arguments: json.RawMessage(`{"id":"x"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		}, nil
	}
	return aharness.Response{Text: "steered child answer", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
}

func TestConcurrencySafeCustomToolFansOut(t *testing.T) {
	var active, maxActive atomicInt
	var bothStarted sync.Once
	barrier := make(chan struct{})
	tools := &aharness.ToolsConfig{
		Extra: []aharness.Tool{{
			Name:            "safe_lookup",
			Description:     "Concurrency-safe lookup.",
			InputSchema:     json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
			ConcurrencySafe: true,
			Run: func(_ context.Context, _ aharness.ToolCall) (aharness.ToolResult, error) {
				current := active.add(1)
				if current > maxActive.load() {
					maxActive.store(current)
				}
				if current == 2 {
					bothStarted.Do(func() { close(barrier) })
				}
				select {
				case <-barrier:
				case <-time.After(2 * time.Second):
					active.add(-1)
					return aharness.ErrorResult("timeout", "sibling child did not start concurrently"), nil
				}
				active.add(-1)
				return aharness.TextResult("ok"), nil
			},
		}},
		Delegation: &aharness.DelegationConfig{Agents: []aharness.AgentDefinition{{
			Name:        "reader",
			Description: "Safe reader.",
			Prompt:      "Read.",
			Tools:       []aharness.ToolName{"safe_lookup"},
		}}},
	}
	parent := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:   "d1",
				Name: "delegate",
				Arguments: delegateArgs(
					map[string]string{"agent": "reader", "task": "one"},
					map[string]string{"agent": "reader", "task": "two"},
				),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	modelCfg := testModel(parent)
	modelCfg.NewCompleter = func(string) aharness.Model { return &toolCallingChild{} }
	var authActive, authMax atomicInt
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  t.TempDir(),
		UserAgent: "aharness-test/1.0",
		Model:     modelCfg,
		Tools:     tools,
		Authorize: func(_ context.Context, _ aharness.ToolRequest) error {
			current := authActive.add(1)
			if current > authMax.load() {
				authMax.store(current)
			}
			waitABit()
			authActive.add(-1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	run, err := ag.Start(context.Background(), "fan out", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var execution string
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		if completed, ok := ev.(aharness.ToolCompleted); ok && completed.Name == "delegate" {
			var details struct {
				Execution string `json:"execution"`
			}
			_ = json.Unmarshal(completed.Details, &details)
			execution = details.Execution
		}
	}
	if execution != "concurrent" {
		t.Fatalf("execution = %q, want concurrent", execution)
	}
	if authMax.load() != 1 {
		t.Fatalf("max concurrent Authorize calls = %d, want 1", authMax.load())
	}
	if maxActive.load() < 2 {
		t.Fatalf("max concurrent custom tool calls = %d, want at least 2", maxActive.load())
	}
}

type atomicInt struct {
	value atomic.Int32
}

func (a *atomicInt) add(delta int32) int32 { return a.value.Add(delta) }
func (a *atomicInt) load() int32           { return a.value.Load() }
func (a *atomicInt) store(value int32)     { a.value.Store(value) }

func waitABit() { time.Sleep(5 * time.Millisecond) }

type toolCallingChild struct {
	mu    sync.Mutex
	calls int
}

func (m *toolCallingChild) Complete(_ context.Context, _ aharness.Request) (aharness.Response, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		return aharness.Response{
			ToolCalls:    []aharness.ToolCall{{ID: "child-call", Name: "safe_lookup", Arguments: json.RawMessage(`{"id":"x"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		}, nil
	}
	return aharness.Response{Text: "child done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
}
