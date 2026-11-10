package agent

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

type runOutcome struct {
	res Result
	err error
}

func synctestTest(t *testing.T, fn func(*testing.T)) {
	t.Helper()
	synctest.Test(t, fn)
}

type scriptStep struct {
	resp           model.Response
	err            error
	blockOnContext bool
}

type fakeCompleter struct {
	steps    []scriptStep
	calls    int
	requests []model.Request
	called   chan int
}

func (f *fakeCompleter) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	f.requests = append(f.requests, req)
	index := f.calls
	f.calls++
	if f.called != nil {
		f.called <- index
	}
	if index >= len(f.steps) {
		return model.Response{}, fmt.Errorf("unexpected model call %d", index+1)
	}
	step := f.steps[index]
	if step.blockOnContext {
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	}
	return step.resp, step.err
}

type fakeStore struct {
	events    []session.Event
	syncs     int
	appendErr error
	failAfter int
	syncErr   error
}

func (s *fakeStore) Append(ev session.Event) error {
	if s.appendErr != nil && (s.failAfter == 0 || len(s.events) >= s.failAfter) {
		return s.appendErr
	}
	ev.V = session.Version
	ev.Seq = uint64(len(s.events) + 1)
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	if ev.Session == "" {
		ev.Session = "test-session"
	}
	s.events = append(s.events, ev)
	return nil
}

func (s *fakeStore) Sync() error {
	s.syncs++
	return s.syncErr
}

func (s *fakeStore) Events() []session.Event {
	return append([]session.Event(nil), s.events...)
}

type toolCallRecord struct {
	name string
}

type fakeToolset struct {
	schemas []model.ToolSchema
	exec    func(ctx context.Context, name string, args json.RawMessage) tools.Result
	calls   []toolCallRecord
}

func (f *fakeToolset) Schemas() []model.ToolSchema {
	if f.schemas != nil {
		return f.schemas
	}
	return tools.Schemas()
}

func (f *fakeToolset) Execute(ctx context.Context, name string, args json.RawMessage) tools.Result {
	f.calls = append(f.calls, toolCallRecord{name: name})
	if f.exec != nil {
		return f.exec(ctx, name, args)
	}
	return tools.Success("ok")
}

func testConfig() Config {
	return Config{
		Provider:       "opencode-go",
		Dialect:        string(model.DialectChat),
		Model:          "test-model",
		Workspace:      "/tmp/workspace",
		StateDir:       "/tmp/state",
		HarnessVersion: "test-harness",
		MaxTurns:       10,
		MaxTotalTokens: 1_000_000,
		Limits: Limits{
			MaxContextTokens: 100_000,
			MaxOutputTokens:  1_000,
			SafetyMargin:     5_000,
			MaxInputTokens:   90_000,
			CompactionTarget: 85_000,
		},
		SessionTimeout: time.Hour,
		ModelTimeout:   10 * time.Second,
	}
}

func runTask(t *testing.T, cfg Config, comp completer, ts toolset) (*fakeStore, Result, error) {
	t.Helper()
	store := &fakeStore{}
	runner := newRunner(cfg, comp, ts, store)
	res, err := runner.Run(t.Context(), "do the task", "-task")
	return store, res, err
}

func eventsByType(store *fakeStore, typ session.EventType) []session.Event {
	var out []session.Event
	for _, ev := range store.events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func countEvents(store *fakeStore, typ session.EventType) int {
	return len(eventsByType(store, typ))
}

func decodePayload[T any](t *testing.T, ev session.Event) T {
	t.Helper()
	var out T
	if err := jsonv2.Unmarshal(ev.Data, &out); err != nil {
		t.Fatalf("decode %s payload: %v", ev.Type, err)
	}
	return out
}

func newTestEvent(t *testing.T, typ session.EventType, turn int, role string, payload any) session.Event {
	t.Helper()
	ev, err := session.NewEvent(typ, turn, role, payload)
	if err != nil {
		t.Fatalf("build %s event: %v", typ, err)
	}
	return ev
}

func startEvent(t *testing.T, workspace string) session.Event {
	t.Helper()
	return newTestEvent(t, session.TypeSessionStart, 0, "", sessionStartData{
		PromptVersion:  PromptVersion,
		ToolsetVersion: tools.Version,
		Workspace:      workspace,
		StateDir:       "/tmp/state",
		Provider:       "opencode-go",
		Dialect:        "openai-chat",
		Model:          "test-model",
		Limits:         testConfig().Limits,
		HarnessVersion: "test-harness",
	})
}

func toolCall(id, name, args string) model.ToolCall {
	return model.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

func assertExactlyOne(t *testing.T, store *fakeStore, typ session.EventType) {
	t.Helper()
	if got := countEvents(store, typ); got != 1 {
		t.Fatalf("%s events = %d, want 1", typ, got)
	}
}

func exactOutput(size int) string {
	output := make([]byte, size)
	for i := range output {
		output[i] = 'x'
	}
	return string(output)
}
