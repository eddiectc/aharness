package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

type engineModel struct{}

func (engineModel) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: "ok", FinishReason: model.FinishStop}, nil
}

func baseSpec(t *testing.T, mode Mode) Spec {
	t.Helper()
	return Spec{
		Workspace:      t.TempDir(),
		StateDir:       t.TempDir(),
		Mode:           mode,
		UserAgent:      "test-host/1.0",
		Provider:       "test",
		Dialect:        "custom",
		Model:          "engine-model",
		Limits:         agent.Limits{MaxContextTokens: 10_000, MaxOutputTokens: 100, SafetyMargin: 1000, MaxInputTokens: 9000, CompactionTarget: 9000},
		Grant:          []string{},
		GrantSet:       true,
		CustomToolsSet: true,
		NewCompleter: func(model.Dialect, string, string) model.Completer {
			return engineModel{}
		},
	}
}

func TestOpenNewPreservesEmptyAndNilGrants(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grant []string
		nil   bool
	}{
		{name: "empty", grant: []string{}},
		{name: "nil", grant: nil, nil: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseSpec(t, ModeNew)
			spec.Grant = tc.grant
			host, err := Open(context.Background(), spec)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = host.Close() }()
			state, err := host.State()
			if err != nil {
				t.Fatalf("State: %v", err)
			}
			if tc.nil {
				if state.Tools != nil {
					t.Fatalf("nil grant recorded as %#v", state.Tools)
				}
			} else {
				if state.Tools == nil || len(state.Tools) != 0 {
					t.Fatalf("empty grant recorded as %#v", state.Tools)
				}
			}
		})
	}
}

func TestPromptTagValidatedBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		tag  string
	}{
		{name: "space", tag: "acode prompt"},
		{name: "newline", tag: "acode\nprompt"},
		{name: "at sign", tag: "acode@prompt"},
		{name: "non ascii", tag: "acodé"},
		{name: "sixty five bytes", tag: strings.Repeat("a", 65)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseSpec(t, ModeNew)
			spec.PromptTag = tc.tag
			_, err := Open(context.Background(), spec)
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Field != "PromptTag" {
				t.Fatalf("Open with PromptTag %q: err = %v, want ConfigError for PromptTag", tc.tag, err)
			}
			if paths, globErr := filepath.Glob(filepath.Join(spec.StateDir, "sessions", "*.jsonl")); globErr != nil || len(paths) != 0 {
				t.Fatalf("invalid tag created session logs %v (err %v)", paths, globErr)
			}
		})
	}

	spec := baseSpec(t, ModeNew)
	spec.PromptTag = "acode/prompt-v1"
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open with valid tag: %v", err)
	}
	defer func() { _ = host.Close() }()
	state, err := host.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.PromptTag != "acode/prompt-v1" {
		t.Fatalf("State.PromptTag = %q", state.PromptTag)
	}
	if host.Spec().PromptTag != "acode/prompt-v1" {
		t.Fatalf("Spec.PromptTag = %q", host.Spec().PromptTag)
	}

	// 64 bytes is the inclusive limit.
	long := baseSpec(t, ModeNew)
	long.PromptTag = strings.Repeat("a", 64)
	longHost, err := Open(context.Background(), long)
	if err != nil {
		t.Fatalf("Open with a 64-byte tag: %v", err)
	}
	defer func() { _ = longHost.Close() }()
	if state, stateErr := longHost.State(); stateErr != nil || state.PromptTag != long.PromptTag {
		t.Fatalf("64-byte tag state = %+v (err %v)", state, stateErr)
	}
}

func TestForkInstructionsAppendToRenderedPromptForLegacyLog(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	id := session.NewID()
	store, err := session.Create(stateDir, id)
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	// A log written before system_prompt was recorded: the fork must render the
	// version's prompt and append to that.
	start := map[string]any{
		"prompt_version":  agent.PromptVersion,
		"toolset_version": tools.Version,
		"workspace":       workspace,
		"state_dir":       stateDir,
		"provider":        "test",
		"dialect":         "custom",
		"model":           "engine-model",
		"limits":          agent.Limits{MaxContextTokens: 10_000, MaxOutputTokens: 100, SafetyMargin: 1000, MaxInputTokens: 9000, CompactionTarget: 9000},
	}
	ev, err := session.NewEvent(session.TypeSessionStart, 0, "", start)
	if err != nil {
		t.Fatalf("build session_start: %v", err)
	}
	if err := store.Append(ev); err != nil {
		t.Fatalf("append session_start: %v", err)
	}
	if err := store.Sync(); err != nil {
		t.Fatalf("sync session_start: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	spec := baseSpec(t, ModeFork)
	spec.StateDir = stateDir
	spec.Workspace = workspace
	spec.SessionID = id
	spec.AppendPrompt = "extra rule"
	spec.AppendPromptSet = true
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open fork: %v", err)
	}
	defer func() { _ = host.Close() }()
	state, err := host.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if !strings.HasPrefix(state.SystemPrompt, "You are aharness,") {
		t.Fatalf("fork prompt = %q, want the rendered prompt first", state.SystemPrompt)
	}
	if !strings.Contains(state.SystemPrompt, "extra rule") {
		t.Fatalf("fork prompt = %q, want the appended instructions", state.SystemPrompt)
	}
	if state.ContractOverride == nil || !state.ContractOverride.Prompt {
		t.Fatalf("fork contract override = %+v, want a prompt override", state.ContractOverride)
	}
}

func TestOpenRequiresUserAgentAndFillsToolOptions(t *testing.T) {
	for _, value := range []string{"", "   "} {
		spec := baseSpec(t, ModeNew)
		spec.UserAgent = value
		_, err := Open(context.Background(), spec)
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Field != "UserAgent" {
			t.Fatalf("Open with UserAgent %q: err = %v, want ConfigError for UserAgent", value, err)
		}
	}

	spec := baseSpec(t, ModeNew)
	spec.UserAgent = "host/1.0"
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = host.Close() }()
	if got := host.Spec().ToolOptions.UserAgent; got != "host/1.0" {
		t.Fatalf("ToolOptions.UserAgent = %q, want the host identity", got)
	}

	spec = baseSpec(t, ModeNew)
	spec.UserAgent = "host/1.0"
	spec.ToolOptions.UserAgent = "other/2.0"
	_, err = Open(context.Background(), spec)
	var configErr *ConfigError
	if !errors.As(err, &configErr) || configErr.Field != "ToolOptions.UserAgent" {
		t.Fatalf("mismatched ToolOptions.UserAgent: err = %v, want ConfigError", err)
	}
}

func TestIdentityDefaultsTrimsAndValidates(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "aharness"},
		{name: "whitespace only", in: " \t\n ", want: "aharness"},
		{name: "trimmed", in: "  acode  ", want: "acode"},
		{name: "sixty four bytes", in: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
		{name: "printable non ascii", in: "acode-日本", want: "acode-日本"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseSpec(t, ModeNew)
			spec.Identity = tc.in
			host, err := Open(context.Background(), spec)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = host.Close() }()
			if got := host.Spec().Identity; got != tc.want {
				t.Fatalf("Spec.Identity = %q, want %q", got, tc.want)
			}
			state, err := host.State()
			if err != nil {
				t.Fatalf("State: %v", err)
			}
			wantFirst := fmt.Sprintf("You are %s, a coding harness working in the workspace %q.", tc.want, host.Workspace())
			first, _, _ := strings.Cut(state.SystemPrompt, "\n")
			if first != wantFirst {
				t.Fatalf("recorded prompt first line = %q, want %q", first, wantFirst)
			}
		})
	}

	for _, tc := range []struct {
		name string
		in   string
	}{
		{name: "newline", in: "aco\nde"},
		{name: "carriage return", in: "aco\rde"},
		{name: "tab", in: "aco\tde"},
		{name: "control", in: "aco\x01de"},
		{name: "delete", in: "aco\x7fde"},
		{name: "c1 control", in: "aco\u0085de"},
		{name: "invalid utf8", in: string([]byte{0xff})},
		{name: "too long", in: strings.Repeat("a", 65)},
		{name: "too many bytes not runes", in: strings.Repeat("日", 22)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseSpec(t, ModeNew)
			spec.Identity = tc.in
			_, err := Open(context.Background(), spec)
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Field != "Identity" {
				t.Fatalf("Open with Identity %q: err = %v, want ConfigError for Identity", tc.in, err)
			}
		})
	}
}

func TestNewSessionRecordsCurrentPromptBody(t *testing.T) {
	spec := baseSpec(t, ModeNew)
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = host.Close() }()
	state, err := host.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.PromptVersion != agent.PromptVersion || agent.PromptVersion != "prompt-v3" {
		t.Fatalf("recorded prompt version = %q", state.PromptVersion)
	}
	for _, want := range []string{
		"Every tool call receives exactly one result.",
		"If the workspace carries project instructions (AGENTS.md or the file it names)",
		"Run the relevant tests before reporting the change done",
		"when the conclusion must be reached without the context that produced the work",
	} {
		if !strings.Contains(state.SystemPrompt, want) {
			t.Fatalf("recorded v3 prompt lacks %q", want)
		}
	}
	if strings.Contains(state.SystemPrompt, "Run the relevant tests after changing code") {
		t.Fatal("recorded prompt still carries the frozen v2 test rule")
	}
}

func TestCleanupWorkRemovesAndBuildRunnerRecreates(t *testing.T) {
	spec := baseSpec(t, ModeNew)
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = host.Close() }()
	if _, err := host.BuildRunner(RunSpec{}, nil, nil, nil); err != nil {
		t.Fatalf("BuildRunner: %v", err)
	}
	workDir := host.WorkDir()
	expected := filepath.Join(host.StateDir(), "work", host.ID())
	if workDir != expected {
		t.Fatalf("WorkDir = %q, want %q", workDir, expected)
	}
	if _, err := os.Stat(filepath.Join(workDir, "tmp")); err != nil {
		t.Fatalf("tmp dir missing: %v", err)
	}
	// A child command may leave a read-only directory with contents behind.
	readOnly := filepath.Join(workDir, "locked")
	if err := os.MkdirAll(filepath.Join(readOnly, "nested"), 0o700); err != nil {
		t.Fatalf("mkdir read-only: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readOnly, "nested", "file"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write read-only file: %v", err)
	}
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	if err := host.CleanupWork(); err != nil {
		t.Fatalf("CleanupWork: %v", err)
	}
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("work dir still exists: %v", err)
	}
	if _, err := host.BuildRunner(RunSpec{}, nil, nil, nil); err != nil {
		t.Fatalf("BuildRunner after cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "tmp")); err != nil {
		t.Fatalf("tmp dir was not recreated: %v", err)
	}
	if err := host.CleanupWork(); err != nil {
		t.Fatalf("second CleanupWork: %v", err)
	}
}

func TestHostReadSurface(t *testing.T) {
	spec := baseSpec(t, ModeNew)
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = host.Close() }()
	todoDetails := json.RawMessage(`{"items":[{"content":"one","status":"pending"}]}`)
	events := []struct {
		typ     session.EventType
		role    string
		payload any
	}{
		{session.TypeUserMessage, "user", map[string]string{"text": "hello", "source": "task"}},
		{session.TypeToolResult, "tool", map[string]any{
			"call_id": "call-1", "name": "todo", "ok": true, "code": "ok", "details": todoDetails,
		}},
	}
	for _, item := range events {
		ev, err := session.NewEvent(item.typ, 1, item.role, item.payload)
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		if err := host.Store().Append(ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	history, err := host.History()
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) == 0 || history[0].Text != "hello" {
		t.Fatalf("history = %+v", history)
	}
	todo := host.Todo()
	if len(todo) != 1 || todo[0].Content != "one" || todo[0].Status != "pending" {
		t.Fatalf("todo = %+v", todo)
	}
	if got, want := host.LastSeq(), host.Store().LastSeq(); got != want {
		t.Fatalf("LastSeq = %d, want %d", got, want)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := host.History(); err == nil {
		t.Fatal("History after Close succeeded")
	}
}

func TestListSummariesFiltersAndReadsHeads(t *testing.T) {
	parentSpec := baseSpec(t, ModeNew)
	parent, err := Open(context.Background(), parentSpec)
	if err != nil {
		t.Fatalf("Open parent: %v", err)
	}
	parentID := parent.ID()
	stateDir := parent.StateDir()
	workspace := parent.Workspace()
	ev, err := session.NewEvent(session.TypeUserMessage, 0, "user", map[string]string{"text": "first message", "source": "task"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if err := parent.Store().Append(ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatalf("Close parent: %v", err)
	}
	forkSpec := baseSpec(t, ModeFork)
	forkSpec.StateDir = stateDir
	forkSpec.Workspace = workspace
	forkSpec.SessionID = parentID
	fork, err := Open(context.Background(), forkSpec)
	if err != nil {
		t.Fatalf("Open fork: %v", err)
	}
	forkID := fork.ID()
	if err := fork.Close(); err != nil {
		t.Fatalf("Close fork: %v", err)
	}

	roots, err := ListSummaries(context.Background(), stateDir, ListOptions{})
	if err != nil {
		t.Fatalf("ListSummaries roots: %v", err)
	}
	if len(roots) != 1 || roots[0].ID != parentID {
		t.Fatalf("root summaries = %+v", roots)
	}
	root := roots[0]
	if root.Workspace != workspace || root.Model != "engine-model" || root.FirstMessage != "first message" || root.ParentSession != "" {
		t.Fatalf("root summary = %+v", root)
	}
	if root.CreatedAt.IsZero() || root.UpdatedAt.IsZero() {
		t.Fatalf("summary timestamps = %v / %v", root.CreatedAt, root.UpdatedAt)
	}

	all, err := ListSummaries(context.Background(), stateDir, ListOptions{IncludeChildren: true, Workspace: workspace})
	if err != nil {
		t.Fatalf("ListSummaries all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all summaries = %+v", all)
	}
	var child *Summary
	for i := range all {
		if all[i].ID == forkID {
			child = &all[i]
		}
	}
	if child == nil || child.ParentSession != parentID {
		t.Fatalf("child summary = %+v", child)
	}
	if got, err := ListSummaries(context.Background(), stateDir, ListOptions{Workspace: "/other/workspace"}); err != nil || len(got) != 0 {
		t.Fatalf("workspace filter = %+v, %v", got, err)
	}
	if got, err := ListSummaries(context.Background(), stateDir, ListOptions{IncludeChildren: true, Limit: 1}); err != nil || len(got) != 1 {
		t.Fatalf("limit = %+v, %v", got, err)
	}

	// A corrupt tail is outside the head read, so MaxEvents still succeeds
	// while a full snapshot skips the log.
	file, err := os.OpenFile(filepath.Join(stateDir, "sessions", parentID+".jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open parent log: %v", err)
	}
	if _, err := file.WriteString("{not-json\n{\"more\":true}\n"); err != nil {
		t.Fatalf("write corrupt tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close parent log: %v", err)
	}
	head, err := ListSummaries(context.Background(), stateDir, ListOptions{MaxEvents: 1})
	if err != nil || len(head) != 1 || head[0].ID != parentID {
		t.Fatalf("head summaries = %+v, %v", head, err)
	}
	full, err := ListSummaries(context.Background(), stateDir, ListOptions{})
	if err != nil || len(full) != 0 {
		t.Fatalf("full summaries = %+v, %v; want the corrupt log skipped", full, err)
	}
}

func TestDisplayStopsAtRunFinished(t *testing.T) {
	spec := baseSpec(t, ModeNew)
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = host.Close() }()
	var (
		mu       sync.Mutex
		kinds    []string
		returned bool
	)
	display := func(ev agent.DisplayEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if returned {
			t.Errorf("display event %T after Run returned", ev)
		}
		kinds = append(kinds, fmt.Sprintf("%T", ev))
		return nil
	}
	runner, err := host.BuildRunner(RunSpec{}, display, nil, nil)
	if err != nil {
		t.Fatalf("BuildRunner: %v", err)
	}
	if _, err := runner.Run(context.Background(), agent.RunOptions{Task: "work", TaskSet: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	returned = true
	last := kinds[len(kinds)-1]
	mu.Unlock()
	if last != "agent.RunFinished" {
		t.Fatalf("last display event = %s, want agent.RunFinished", last)
	}
}

func TestResumeInheritsModelAndLimits(t *testing.T) {
	spec := baseSpec(t, ModeNew)
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open new: %v", err)
	}
	sessionID := host.ID()
	stateDir := host.StateDir()
	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, err := Open(context.Background(), Spec{
		StateDir:  stateDir,
		Mode:      ModeResume,
		SessionID: sessionID,
		UserAgent: "test-host/1.0",
		NewCompleter: func(model.Dialect, string, string) model.Completer {
			return engineModel{}
		},
	})
	if err != nil {
		t.Fatalf("Open resume: %v", err)
	}
	defer func() { _ = resumed.Close() }()
	got := resumed.Spec()
	if got.Model != "engine-model" || got.Limits.MaxContextTokens != 10_000 {
		t.Fatalf("resumed spec = %+v", got)
	}
	if got.Grant == nil || len(got.Grant) != 0 {
		t.Fatalf("resumed grant = %#v, want explicit empty", got.Grant)
	}
}

func TestBuildRunnerCreatesAndCloseRemovesWorkDir(t *testing.T) {
	spec := baseSpec(t, ModeNew)
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	workDir := host.workDir
	if _, err := host.BuildRunner(RunSpec{}, nil, nil, nil); err != nil {
		t.Fatalf("BuildRunner: %v", err)
	}
	if info, err := os.Stat(host.tmpDir); err != nil || !info.IsDir() {
		t.Fatalf("tmp dir missing: %v", err)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("work dir still exists after Close: %v", err)
	}
}
