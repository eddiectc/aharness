package aharness_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/eddiectc/aharness"
	"github.com/eddiectc/aharness/agent"
)

func transcriptTexts(items []aharness.TranscriptItem, kind aharness.TranscriptKind) []string {
	var texts []string
	for _, item := range items {
		if item.Kind == kind {
			texts = append(texts, item.Text)
		}
	}
	return texts
}

func transcriptToolNames(items []aharness.TranscriptItem) []string {
	var names []string
	for _, item := range items {
		if item.Kind == aharness.TranscriptTool {
			names = append(names, item.Name)
		}
	}
	return names
}

func TestTranscriptRootAndChild(t *testing.T) {
	parent := &scriptedModel{responses: []aharness.Response{
		{ToolCalls: []aharness.ToolCall{{ID: "d1", Name: "delegate",
			Arguments: delegateArgs(map[string]string{"agent": "reader", "task": "read it"})}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	ag, stateDir := newObservingAgent(t, parent, func() aharness.Model { return &observableChildModel{} },
		&aharness.DelegationConfig{Agents: []aharness.AgentDefinition{{
			Name:        "reader",
			Description: "Read-only reader.",
			Prompt:      "Read the assigned input.\n",
			Tools:       []aharness.ToolName{aharness.ToolReadFile},
		}}})
	var childSession string
	result, err := ag.Run(context.Background(), "delegate", aharness.RunOptions{
		OnEvent: func(ev aharness.Event) {
			if started, ok := ev.(aharness.SubagentStarted); ok {
				childSession = started.ChildSession
			}
		},
	})
	if err != nil || result.Status != aharness.StopCompleted {
		t.Fatalf("Run = %q, %v", result.Status, err)
	}
	if len(result.Children) != 1 || result.Children[0].Session != childSession {
		t.Fatalf("children = %+v, child session = %q", result.Children, childSession)
	}

	root, err := aharness.Transcript(context.Background(), stateDir, ag.ID())
	if err != nil {
		t.Fatalf("Transcript root: %v", err)
	}
	if got := transcriptTexts(root, aharness.TranscriptUser); len(got) != 1 || got[0] != "delegate" {
		t.Fatalf("root user items = %q", got)
	}
	if got := transcriptTexts(root, aharness.TranscriptAssistant); len(got) != 1 || got[0] != "parent done" {
		t.Fatalf("root assistant items = %q", got)
	}
	if names := transcriptToolNames(root); len(names) != 1 || names[0] != "delegate" {
		t.Fatalf("root tool items = %q", names)
	}
	foundRun := false
	for _, item := range root {
		if item.Kind != aharness.TranscriptRun {
			continue
		}
		foundRun = true
		if item.Run != 1 || item.Model != "test-model" || item.Status != aharness.StopCompleted {
			t.Fatalf("root run item = %+v", item)
		}
	}
	if !foundRun {
		t.Fatal("root transcript has no run header")
	}

	child, err := aharness.Transcript(context.Background(), stateDir, childSession)
	if err != nil {
		t.Fatalf("Transcript child: %v", err)
	}
	if got := transcriptTexts(child, aharness.TranscriptUser); len(got) != 1 || got[0] != "read it" {
		t.Fatalf("child user items = %q", got)
	}
	if got := transcriptTexts(child, aharness.TranscriptAssistant); len(got) == 0 || got[len(got)-1] != "child report" {
		t.Fatalf("child assistant items = %q", got)
	}
	childTools := transcriptToolNames(child)
	if len(childTools) == 0 || childTools[0] != string(aharness.ToolReadFile) {
		t.Fatalf("child tool items = %q", childTools)
	}
	for _, text := range transcriptTexts(child, aharness.TranscriptAssistant) {
		if text == "parent done" {
			t.Fatal("the child transcript carries the parent's assistant text")
		}
	}

	// The projections are the same shape for every session: the child log has
	// its own run header, so the reader does not need to know what it is.
	childRuns := 0
	for _, item := range child {
		if item.Kind == aharness.TranscriptRun {
			childRuns++
		}
	}
	if childRuns != 1 {
		t.Fatalf("child run headers = %d, want 1", childRuns)
	}
}

// gatedModel blocks its first call until the test releases it.
type gatedModel struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (m *gatedModel) Complete(ctx context.Context, _ aharness.Request) (aharness.Response, error) {
	m.once.Do(func() { close(m.started) })
	select {
	case <-m.release:
	case <-ctx.Done():
		return aharness.Response{}, ctx.Err()
	}
	return aharness.Response{Text: "live answer", FinishReason: aharness.FinishStop,
		Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
}

func TestTranscriptLiveWriter(t *testing.T) {
	model := &gatedModel{started: make(chan struct{}), release: make(chan struct{})}
	ag, _, stateDir := newTestAgent(t, nil, model)
	// Run consumes its own event stream, so the test goroutine is free to read
	// the log while the model call is in flight.
	done := make(chan error, 1)
	var result aharness.Result
	go func() {
		var err error
		result, err = ag.Run(context.Background(), "live task", aharness.RunOptions{})
		done <- err
	}()
	<-model.started

	items, err := aharness.Transcript(context.Background(), stateDir, ag.ID())
	if err != nil {
		if errors.Is(err, aharness.ErrSessionLocked) {
			t.Fatalf("Transcript took the session lock: %v", err)
		}
		t.Fatalf("Transcript during a run: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("live transcript = %d items, want the run header and the task", len(items))
	}
	if got := transcriptTexts(items, aharness.TranscriptUser); len(got) != 1 || got[0] != "live task" {
		t.Fatalf("live user items = %q", got)
	}
	for _, item := range items {
		if item.Kind == aharness.TranscriptNotice && item.NoticeKind == aharness.NoticeRepair {
			t.Fatalf("a live read reported a repair: %+v", item)
		}
	}

	close(model.release)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != aharness.StopCompleted {
		t.Fatalf("status = %q", result.Status)
	}
	after, err := aharness.Transcript(context.Background(), stateDir, ag.ID())
	if err != nil {
		t.Fatalf("Transcript after the run: %v", err)
	}
	if got := transcriptTexts(after, aharness.TranscriptAssistant); len(got) != 1 || got[0] != "live answer" {
		t.Fatalf("assistant items after the run = %q", got)
	}
}

func TestTranscriptTornFinalLine(t *testing.T) {
	ag, _, stateDir := newTestAgent(t, nil, &scriptedModel{responses: []aharness.Response{{
		Text: "answer", FinishReason: aharness.FinishStop,
		Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
	}}})
	if _, err := ag.Run(context.Background(), "task", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	before, err := aharness.Transcript(context.Background(), stateDir, ag.ID())
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	file, err := os.OpenFile(ag.SessionPath(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open session log: %v", err)
	}
	// A crash artifact: a final line cut mid-object, with no newline.
	if _, err := file.WriteString(`{"v":2,"seq":99,"session":"` + ag.ID() + `","type":"final","data":{`); err != nil {
		_ = file.Close()
		t.Fatalf("write torn line: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close session log: %v", err)
	}
	after, err := aharness.Transcript(context.Background(), stateDir, ag.ID())
	if err != nil {
		t.Fatalf("Transcript with a torn final line: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("transcript changed after a torn final line:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestTranscriptNotFound(t *testing.T) {
	stateDir := t.TempDir()
	if _, err := aharness.Transcript(context.Background(), stateDir, "00000000-0000-7000-8000-000000000000"); !errors.Is(err, aharness.ErrSessionNotFound) {
		t.Fatalf("missing session = %v, want ErrSessionNotFound", err)
	}
	if _, err := aharness.Transcript(context.Background(), stateDir, "not-a-uuid"); !errors.Is(err, aharness.ErrInvalidSessionID) {
		t.Fatalf("invalid id = %v, want ErrInvalidSessionID", err)
	}
	if _, err := aharness.Transcript(context.Background(), "", "00000000-0000-7000-8000-000000000000"); err == nil {
		t.Fatal("an empty state directory was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := aharness.Transcript(ctx, stateDir, "00000000-0000-7000-8000-000000000000"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v, want context.Canceled", err)
	}
}

func TestAgentTranscriptMatchesPackageReader(t *testing.T) {
	ag, _, stateDir := newTestAgent(t, nil, &scriptedModel{responses: []aharness.Response{{
		Text: "answer", FinishReason: aharness.FinishStop,
		Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
	}}})
	result, err := ag.Run(context.Background(), "task", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	live, err := ag.Transcript()
	if err != nil {
		t.Fatalf("Agent.Transcript: %v", err)
	}
	// The read reflects the run that just finished, not merely the same code
	// path: one run header, the task, and the assistant answer.
	if got := transcriptTexts(live, aharness.TranscriptUser); !reflect.DeepEqual(got, []string{"task"}) {
		t.Fatalf("user items = %q", got)
	}
	if got := transcriptTexts(live, aharness.TranscriptAssistant); !reflect.DeepEqual(got, []string{result.FinalText}) {
		t.Fatalf("assistant items = %q, want %q", got, result.FinalText)
	}
	runs := 0
	for _, item := range live {
		if item.Kind != aharness.TranscriptRun {
			continue
		}
		runs++
		if item.Status != aharness.StopCompleted || item.Turns != result.RunTurns || item.Model != "test-model" {
			t.Fatalf("run item = %+v", item)
		}
	}
	if runs != 1 {
		t.Fatalf("run items = %d, want 1", runs)
	}
	reread, err := aharness.Transcript(context.Background(), stateDir, ag.ID())
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if !reflect.DeepEqual(live, reread) {
		t.Fatalf("Agent.Transcript = %+v, want %+v", live, reread)
	}
	if err := ag.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := ag.Transcript(); !errors.Is(err, aharness.ErrClosed) {
		t.Fatalf("Transcript after Close = %v, want ErrClosed", err)
	}
}

func TestTranscriptItemMirrorsHistoryItem(t *testing.T) {
	// The facade DTO copies agent.HistoryItem by hand. Every field must exist
	// in the same position with the same type, except the enumerations and the
	// usage record, which are the facade's public forms.
	converted := map[string]reflect.Type{
		"Kind":       reflect.TypeFor[aharness.TranscriptKind](),
		"NoticeKind": reflect.TypeFor[aharness.NoticeKind](),
		"Status":     reflect.TypeFor[aharness.StopReason](),
		"Usage":      reflect.TypeFor[aharness.Usage](),
	}
	internal := reflect.TypeFor[agent.HistoryItem]()
	public := reflect.TypeFor[aharness.TranscriptItem]()
	if internal.NumField() != public.NumField() {
		t.Fatalf("agent.HistoryItem has %d fields, TranscriptItem has %d", internal.NumField(), public.NumField())
	}
	for i := 0; i < internal.NumField(); i++ {
		in, out := internal.Field(i), public.Field(i)
		if in.Name != out.Name {
			t.Errorf("field %d = %s, want %s", i, out.Name, in.Name)
			continue
		}
		want := in.Type
		if mapped, ok := converted[in.Name]; ok {
			want = mapped
		}
		if out.Type != want {
			t.Errorf("field %s type = %s, want %s", in.Name, out.Type, want)
		}
	}
}
