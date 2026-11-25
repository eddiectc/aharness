package aharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
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
		childID      string
		execution    string
		childEvents  int
		steerOnce    sync.Once
		childSteerID uint64
		childAckID   uint64
		finished     aharness.Result
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
				id, err := run.SteerChild(ev.ChildSession, "focus on the summary")
				if err != nil {
					t.Fatalf("SteerChild: %v", err)
				}
				childSteerID = id
				close(toolRelease)
			})
		case aharness.SubagentMessageRecorded:
			if ev.ID != 0 {
				childAckID = ev.ID
			}
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
	if childSteerID == 0 || childAckID != childSteerID {
		t.Fatalf("child steering id = %d, ack = %d", childSteerID, childAckID)
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
	if _, err := run.SteerChild(childID, "after finish"); !errors.Is(err, aharness.ErrNoSuchChild) {
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

// newObservingAgent opens a facade agent whose delegation config is supplied by
// the caller and whose delegated children use childModel when non-nil.
func newObservingAgent(t *testing.T, parent aharness.Model, childModel func() aharness.Model, delegation *aharness.DelegationConfig) (*aharness.Agent, string) {
	t.Helper()
	modelCfg := testModel(parent)
	if childModel != nil {
		modelCfg.NewCompleter = func(string) aharness.Model { return childModel() }
	}
	stateDir := t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     modelCfg,
		Tools:     &aharness.ToolsConfig{Delegation: delegation},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })
	return ag, stateDir
}

// observingRecorder collects one child's public events per child session.
type observingRecorder struct {
	mu      sync.Mutex
	targets []aharness.ChildTarget
	events  map[string][]aharness.Event
}

func newObservingRecorder() *observingRecorder {
	return &observingRecorder{events: make(map[string][]aharness.Event)}
}

func (r *observingRecorder) observe(target aharness.ChildTarget) func(aharness.Event) error {
	r.mu.Lock()
	r.targets = append(r.targets, target)
	r.mu.Unlock()
	return func(ev aharness.Event) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events[target.Session] = append(r.events[target.Session], ev)
		return nil
	}
}

func (r *observingRecorder) registrations() []aharness.ChildTarget {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]aharness.ChildTarget(nil), r.targets...)
}

func (r *observingRecorder) childEvents(session string) []aharness.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]aharness.Event(nil), r.events[session]...)
}

func (r *observingRecorder) kinds(session string) []string {
	var kinds []string
	for _, ev := range r.childEvents(session) {
		kinds = append(kinds, aharness.ViewEvent(ev).Kind)
	}
	return kinds
}

// observableChildModel streams two tool rounds with text, then answers.
type observableChildModel struct {
	mu    sync.Mutex
	calls int
}

func (m *observableChildModel) Complete(_ context.Context, _ aharness.Request) (aharness.Response, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call <= 2 {
		path := "README.md"
		if call == 2 {
			path = "src/main.go"
		}
		return aharness.Response{
			Text: "checking " + path,
			ToolCalls: []aharness.ToolCall{{
				ID:        "child-read",
				Name:      string(aharness.ToolReadFile),
				Arguments: json.RawMessage(`{"intent":"read","path":"` + path + `"}`),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 2, OutputTokens: 1, Reported: true},
		}, nil
	}
	return aharness.Response{Text: "child report", FinishReason: aharness.FinishStop,
		Usage: aharness.Usage{InputTokens: 2, OutputTokens: 1, Reported: true}}, nil
}

func TestObserveChildReceivesFacadeEventsWithChildSession(t *testing.T) {
	parent := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "d1",
				Name:      "delegate",
				Arguments: delegateArgs(map[string]string{"agent": "reader", "task": "read the file"}),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	recorder := newObservingRecorder()
	ag, _ := newObservingAgent(t, parent, func() aharness.Model { return &observableChildModel{} },
		&aharness.DelegationConfig{
			Agents: []aharness.AgentDefinition{{
				Name:        "reader",
				Description: "Read-only reader.",
				Prompt:      "Read the assigned input.\n",
				Tools:       []aharness.ToolName{aharness.ToolReadFile},
				MaxTurns:    2,
			}},
			ObserveChild: recorder.observe,
		})
	var (
		mu         sync.Mutex
		childTaken []string
	)
	result, err := ag.Run(context.Background(), "delegate", aharness.RunOptions{
		OnEvent: func(ev aharness.Event) {
			if started, ok := ev.(aharness.SubagentStarted); ok {
				mu.Lock()
				childTaken = append(childTaken, started.ChildSession)
				mu.Unlock()
			}
		},
	})
	if err != nil || result.Status != aharness.StopCompleted {
		t.Fatalf("Run = %q, %v", result.Status, err)
	}
	registrations := recorder.registrations()
	if len(registrations) != 1 {
		t.Fatalf("registrations = %d, want 1", len(registrations))
	}
	target := registrations[0]
	mu.Lock()
	taken := append([]string(nil), childTaken...)
	mu.Unlock()
	if len(taken) != 1 || target.Session != taken[0] {
		t.Fatalf("target session = %q, SubagentStarted = %v", target.Session, taken)
	}
	if target.CallID != "d1" || target.Index != 0 || target.Agent != "reader" || target.Intent != "read the file" {
		t.Fatalf("target = %+v", target)
	}
	if len(target.Tools) != 1 || target.Tools[0] != aharness.ToolReadFile {
		t.Fatalf("target tools = %v", target.Tools)
	}

	events := recorder.childEvents(target.Session)
	if len(events) == 0 {
		t.Fatal("the child sink received no events")
	}
	for _, ev := range events {
		view := aharness.ViewEvent(ev)
		if view.Session != target.Session {
			t.Fatalf("event %T session = %q, want %q", ev, view.Session, target.Session)
		}
		if view.Kind == "" || view.Kind == "unknown" {
			t.Fatalf("child sink received %T, which has no public event form", ev)
		}
	}
	kinds := recorder.kinds(target.Session)
	for _, kind := range kinds {
		if strings.HasPrefix(kind, "subagent_") {
			t.Fatalf("child sink received the parent-level event %s", kind)
		}
	}
	for _, want := range []string{
		aharness.EventKindRunStarted,
		aharness.EventKindMessageRecorded,
		aharness.EventKindToolStarted,
		aharness.EventKindToolCompleted,
		aharness.EventKindTextDelta,
		aharness.EventKindResponseCompleted,
		aharness.EventKindNotice, // the child stopped at its own turn limit
		aharness.EventKindRunFinished,
	} {
		if !slices.Contains(kinds, want) {
			t.Fatalf("child kinds = %v, missing %s", kinds, want)
		}
	}
}

func TestObserveChildNotCalledForPreStartFailure(t *testing.T) {
	tests := []struct {
		name  string
		args  json.RawMessage
		limit int
		want  int
	}{
		{
			name: "unknown agent",
			args: delegateArgs(map[string]string{"agent": "nobody", "task": "t"}),
			want: 0,
		},
		{
			name: "unreadable continuation",
			args: delegateArgs(map[string]string{
				"agent":   "reader",
				"task":    "t",
				"session": "00000000-0000-7000-8000-000000000000",
			}),
			want: 0,
		},
		{
			name: "subagent limit",
			args: delegateArgs(
				map[string]string{"agent": "reader", "task": "one"},
				map[string]string{"agent": "reader", "task": "two"},
			),
			limit: 1,
			want:  1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := &scriptedModel{responses: []aharness.Response{
				{ToolCalls: []aharness.ToolCall{{ID: "d1", Name: "delegate", Arguments: tc.args}},
					FinishReason: aharness.FinishToolCalls,
					Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
				{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
			}}
			recorder := newObservingRecorder()
			ag, _ := newObservingAgent(t, parent, func() aharness.Model { return &observableChildModel{} },
				&aharness.DelegationConfig{
					Agents: []aharness.AgentDefinition{{
						Name:        "reader",
						Description: "Read-only reader.",
						Prompt:      "Read the assigned input.\n",
						Tools:       []aharness.ToolName{aharness.ToolReadFile},
					}},
					MaxSubagents: tc.limit,
					ObserveChild: recorder.observe,
				})
			if _, err := ag.Run(context.Background(), "delegate", aharness.RunOptions{}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := len(recorder.registrations()); got != tc.want {
				t.Fatalf("registrations = %d, want %d", got, tc.want)
			}
		})
	}
}

// streamingChildModel implements aharness.StreamingModel.
type streamingChildModel struct {
	mu       sync.Mutex
	streamed bool
}

func (m *streamingChildModel) Complete(context.Context, aharness.Request) (aharness.Response, error) {
	m.mu.Lock()
	m.streamed = false
	m.mu.Unlock()
	return aharness.Response{Text: "child report", FinishReason: aharness.FinishStop,
		Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
}

func (m *streamingChildModel) CompleteStream(_ context.Context, _ aharness.Request, stream aharness.StreamHandler) (aharness.Response, error) {
	m.mu.Lock()
	m.streamed = true
	m.mu.Unlock()
	for _, delta := range []string{"child ", "report"} {
		if err := stream.Text(delta); err != nil {
			return aharness.Response{}, err
		}
	}
	return aharness.Response{Text: "child report", FinishReason: aharness.FinishStop,
		Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
}

func (m *streamingChildModel) didStream() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streamed
}

func TestObserveChildEnablesChildStreaming(t *testing.T) {
	parent := &scriptedModel{responses: []aharness.Response{
		{ToolCalls: []aharness.ToolCall{{ID: "d1", Name: "delegate",
			Arguments: delegateArgs(map[string]string{"agent": "reader", "task": "read"})}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	child := &streamingChildModel{}
	recorder := newObservingRecorder()
	ag, _ := newObservingAgent(t, parent, func() aharness.Model { return child },
		&aharness.DelegationConfig{
			Agents: []aharness.AgentDefinition{{
				Name:        "reader",
				Description: "Read-only reader.",
				Prompt:      "Read the assigned input.\n",
				Tools:       []aharness.ToolName{aharness.ToolReadFile},
			}},
			ObserveChild: recorder.observe,
		})
	if _, err := ag.Run(context.Background(), "delegate", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !child.didStream() {
		t.Fatal("the child used Complete, want CompleteStream with an observer")
	}
	registrations := recorder.registrations()
	if len(registrations) != 1 {
		t.Fatalf("registrations = %d, want 1", len(registrations))
	}
	deltas := 0
	streamedResponse := false
	for _, ev := range recorder.childEvents(registrations[0].Session) {
		switch ev := ev.(type) {
		case aharness.TextDelta:
			deltas++
		case aharness.ResponseCompleted:
			streamedResponse = ev.Streamed
		}
	}
	if deltas != 2 {
		t.Fatalf("child text deltas = %d, want 2 incremental deltas", deltas)
	}
	if !streamedResponse {
		t.Fatal("child ResponseCompleted.Streamed = false, want true")
	}
}

func TestObserveChildErrorIsolatesChild(t *testing.T) {
	parent := &scriptedModel{responses: []aharness.Response{
		{ToolCalls: []aharness.ToolCall{{ID: "d1", Name: "delegate",
			Arguments: delegateArgs(
				map[string]string{"agent": "reader", "task": "one"},
				map[string]string{"agent": "reader", "task": "two"},
			)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	ag, _ := newObservingAgent(t, parent, func() aharness.Model { return &observableChildModel{} },
		&aharness.DelegationConfig{
			Agents: []aharness.AgentDefinition{{
				Name:        "reader",
				Description: "Read-only reader.",
				Prompt:      "Read the assigned input.\n",
				Tools:       []aharness.ToolName{aharness.ToolReadFile},
			}},
			ObserveChild: func(target aharness.ChildTarget) func(aharness.Event) error {
				return func(aharness.Event) error {
					if target.Index == 0 {
						return errors.New("the renderer is broken")
					}
					return nil
				}
			},
		})
	result, err := ag.Run(context.Background(), "delegate", aharness.RunOptions{})
	if err != nil || result.Status != aharness.StopCompleted {
		t.Fatalf("Run = %q, %v; a bad sink must not fail the run", result.Status, err)
	}
	if len(result.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(result.Children))
	}
	failed, sibling := result.Children[0], result.Children[1]
	if failed.Status != aharness.SubagentInternalError || !strings.Contains(failed.Error, "the renderer is broken") {
		t.Fatalf("failed child = %+v", failed)
	}
	if sibling.Status != aharness.SubagentCompleted || sibling.Error != "" {
		t.Fatalf("sibling = %+v", sibling)
	}
}
