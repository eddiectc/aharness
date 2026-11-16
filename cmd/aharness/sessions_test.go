package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/model"
	"github.com/eddiectc/aharness/internal/session"
	"github.com/eddiectc/aharness/internal/tools"
	"github.com/eddiectc/aharness/internal/tui"
)

func testSessionConfig(t *testing.T, stateDir, workspace, modelName string) config {
	t.Helper()
	cfg := config{
		Provider:         providerName,
		Model:            modelName,
		Dialect:          model.DialectChat,
		BaseURL:          "https://example.invalid/v1",
		Workspace:        workspace,
		StateDir:         stateDir,
		PromptVersion:    agent.PromptVersion,
		ToolsetVersion:   tools.Version,
		HarnessVersion:   "test",
		MaxContextTokens: 200_000,
	}
	if err := validateEffective(&cfg); err != nil {
		t.Fatalf("validate test session config: %v", err)
	}
	return cfg
}

// createTestSession writes a valid session log and returns its ID. The first
// user message is optional.
func createTestSession(t *testing.T, stateDir, workspace, modelName, thinkingEffort, firstMessage string) string {
	t.Helper()
	cfg := testSessionConfig(t, stateDir, workspace, modelName)
	cfg.ThinkingEffort = thinkingEffort
	id := session.NewID()
	store, err := session.Create(stateDir, id)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := agent.WriteSessionStart(store, runnerConfig(cfg), nil); err != nil {
		_ = store.Close()
		t.Fatalf("write session start: %v", err)
	}
	if firstMessage != "" {
		ev, err := session.NewEvent(session.TypeUserMessage, 0, "", struct {
			Text   string `json:"text"`
			Source string `json:"source"`
		}{Text: firstMessage, Source: "tui"})
		if err != nil {
			_ = store.Close()
			t.Fatalf("build user message: %v", err)
		}
		if err := store.Append(ev); err != nil {
			_ = store.Close()
			t.Fatalf("append user message: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	return id
}

func TestListPreviousSessionsFiltersWorkspaceAndOrdersNewest(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	older := createTestSession(t, stateDir, workspace, "model-a", "", "older task")
	newer := createTestSession(t, stateDir, workspace, "model-b", "", "  newer\n task  ")
	other := createTestSession(t, stateDir, otherWorkspace, "model-c", "", "other task")
	current := createTestSession(t, stateDir, workspace, "model-d", "", "current task")

	base := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	for id, at := range map[string]time.Time{
		older:   base,
		newer:   base.Add(time.Hour),
		other:   base.Add(2 * time.Hour),
		current: base.Add(3 * time.Hour),
	} {
		path := session.SessionPath(stateDir, id)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatalf("set mtime %s: %v", id, err)
		}
	}
	// A corrupt log in the same workspace is skipped instead of failing the
	// whole picker.
	corrupt := session.SessionPath(stateDir, session.NewID())
	if err := os.WriteFile(corrupt, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	choices, err := listPreviousSessions(stateDir, workspace, current)
	if err != nil {
		t.Fatalf("listPreviousSessions: %v", err)
	}
	if len(choices) != 2 {
		t.Fatalf("choices = %+v, want the two same-workspace sessions", choices)
	}
	if choices[0].ID != newer || choices[1].ID != older {
		t.Fatalf("choices = %+v, want newest first", choices)
	}
	if choices[0].Summary != "newer task" || choices[0].Model != "model-b" {
		t.Fatalf("newer choice = %+v", choices[0])
	}
	if choices[0].Modified.IsZero() {
		t.Fatal("newer choice has no modification time")
	}
}

func TestListPreviousSessionsMissingDirectory(t *testing.T) {
	choices, err := listPreviousSessions(t.TempDir(), "/workspace", "")
	if err != nil {
		t.Fatalf("listPreviousSessions: %v", err)
	}
	if len(choices) != 0 {
		t.Fatalf("choices = %+v, want none", choices)
	}
}

func TestSessionPreviewCapsAndSanitizes(t *testing.T) {
	got := sessionPreview(strings.Repeat("x", sessionPreviewBytes+5) + "\n\x1b")
	if len(got) > sessionPreviewBytes+32 {
		t.Fatalf("preview length = %d", len(got))
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("preview contains ESC: %q", got)
	}
}

// newSessionHost builds a TUI host around one open current session.
func newSessionHost(t *testing.T, stateDir, workspace string) (*tuiHost, string) {
	t.Helper()
	current := createTestSession(t, stateDir, workspace, "model-current", "", "current task")
	store, err := session.Open(stateDir, current)
	if err != nil {
		t.Fatalf("open current session: %v", err)
	}
	ws, err := tools.OpenWorkspace(workspace)
	if err != nil {
		_ = store.Close()
		t.Fatalf("open workspace: %v", err)
	}
	cfg := testSessionConfig(t, stateDir, workspace, "model-current")
	host := &tuiHost{
		cfg:       cfg,
		store:     store,
		ws:        ws,
		sessionID: current,
		info:      sessionInfo(cfg, store, agent.State{}),
	}
	t.Cleanup(func() { _ = host.Close() })
	return host, current
}

func TestTUIHostNewSessionReplacesTheStore(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	host, current := newSessionHost(t, stateDir, workspace)

	snapshot, err := host.NewSession(tui.RunOptions{
		Model: "model-current", ThinkingEffort: "high", ThinkingEffortSet: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if snapshot.Info.SessionID == current {
		t.Fatal("NewSession reused the active session ID")
	}
	if host.sessionID != snapshot.Info.SessionID || host.store.ID() != snapshot.Info.SessionID {
		t.Fatalf("host session = %q, store = %q, snapshot = %q",
			host.sessionID, host.store.ID(), snapshot.Info.SessionID)
	}
	if host.cfg.ThinkingEffort != "high" || snapshot.Info.ThinkingEffort != "high" {
		t.Fatalf("effort: cfg=%q info=%q", host.cfg.ThinkingEffort, snapshot.Info.ThinkingEffort)
	}
	if snapshot.Info.Turns != 0 || len(snapshot.History) != 0 {
		t.Fatalf("new session inherited state: info=%+v history=%v", snapshot.Info, snapshot.History)
	}
	events, err := session.Read(snapshot.Info.TracePath)
	if err != nil {
		t.Fatalf("read new log: %v", err)
	}
	if len(events) != 1 || events[0].Type != session.TypeSessionStart {
		t.Fatalf("new log events = %+v", events)
	}
	if !strings.Contains(string(events[0].Data), `"thinking_effort":"high"`) {
		t.Fatalf("session_start lacks the next-run effort: %s", events[0].Data)
	}
	// The old session lock was released with the old store.
	reopened, err := session.Open(stateDir, current)
	if err != nil {
		t.Fatalf("old session stayed locked: %v", err)
	}
	_ = reopened.Close()
}

func TestTUIHostResumeSessionInheritsRecordedSettings(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	host, current := newSessionHost(t, stateDir, workspace)
	previous := createTestSession(t, stateDir, workspace, "model-previous", "low", "earlier task")

	snapshot, err := host.ResumeSession(previous)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if snapshot.Info.SessionID != previous || host.store.ID() != previous {
		t.Fatalf("snapshot = %q, store = %q, want %q",
			snapshot.Info.SessionID, host.store.ID(), previous)
	}
	if host.cfg.Model != "model-previous" || host.cfg.ThinkingEffort != "low" {
		t.Fatalf("resumed config model=%q effort=%q", host.cfg.Model, host.cfg.ThinkingEffort)
	}
	if snapshot.Info.Model != "model-previous" || snapshot.Info.ThinkingEffort != "low" {
		t.Fatalf("resumed info = %+v", snapshot.Info)
	}
	if host.cfg.MaxContextTokens != 200_000 {
		t.Fatalf("max context = %d, want the recorded 200000", host.cfg.MaxContextTokens)
	}
	if len(snapshot.History) == 0 {
		t.Fatal("resume returned no history")
	}
	// The old store was closed, so its lock is available.
	reopened, err := session.Open(stateDir, current)
	if err != nil {
		t.Fatalf("previous session stayed locked: %v", err)
	}
	_ = reopened.Close()
}

func TestTUIHostResumeRejectsOtherWorkspaceAndKeepsCurrent(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	host, current := newSessionHost(t, stateDir, workspace)
	other := createTestSession(t, stateDir, otherWorkspace, "model-other", "", "other task")

	if _, err := host.ResumeSession(other); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ResumeSession error = %v, want workspace mismatch", err)
	}
	if host.sessionID != current || host.store.ID() != current {
		t.Fatalf("failed resume switched to %q", host.store.ID())
	}
	// The current session is still open and locked.
	if _, err := session.Open(stateDir, current); !errors.Is(err, session.ErrLocked) {
		t.Fatalf("current session lock = %v, want ErrLocked", err)
	}
	// The rejected log is not left locked.
	if reopened, err := session.Open(stateDir, other); err != nil {
		t.Fatalf("rejected session stayed locked: %v", err)
	} else {
		_ = reopened.Close()
	}
}

func TestTUIHostResumeLockedSessionKeepsCurrent(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	host, current := newSessionHost(t, stateDir, workspace)
	locked := createTestSession(t, stateDir, workspace, "model-locked", "", "locked task")
	held, err := session.Open(stateDir, locked)
	if err != nil {
		t.Fatalf("hold session: %v", err)
	}
	defer func() { _ = held.Close() }()

	if _, err := host.ResumeSession(locked); !errors.Is(err, session.ErrLocked) {
		t.Fatalf("ResumeSession error = %v, want ErrLocked", err)
	}
	if host.sessionID != current {
		t.Fatalf("failed resume switched the host to %q", host.sessionID)
	}
}
