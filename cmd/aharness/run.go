package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

const (
	exitConfig = 2
	exitSetup  = 1
)

// run executes the CLI and returns the process exit code. main alone calls
// os.Exit.
func run() int {
	return runWithIO(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}

// runArgs is the testable CLI entry point.
func runArgs(args []string, stdout, stderr io.Writer) int {
	return runWithIO(args, os.Stdin, stdout, stderr)
}

func runWithIO(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	cfg, err := parseConfig(args, os.Getenv, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitConfig
	}
	if cfg.ShowVersion {
		printVersion(stdout)
		return 0
	}
	if cfg.ProvisionalContext {
		_, _ = fmt.Fprintf(stderr, "aharness: warning: max context tokens not set; using provisional default %d; set -max-context-tokens to the confirmed model window\n", cfg.MaxContextTokens)
	}
	cfg.HarnessVersion = buildVersion()
	return execute(cfg, stdin, stdout, stderr)
}

// execute wires the workspace, session state, provider adapter, tool registry,
// and runner, then performs terminal cleanup. Configuration has already been
// validated for a new session; resume and fork inherit before running.
func execute(cfg config, stdin io.Reader, stdout, stderr io.Writer) int {
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	switch {
	case cfg.ResumeID != "":
		return executeResume(cfg, stdin, stdout, stderr, logger)
	case cfg.ForkID != "":
		return executeFork(cfg, stdin, stdout, stderr, logger)
	default:
		return executeNew(cfg, stdin, stdout, stderr, logger)
	}
}

func executeNew(cfg config, stdin io.Reader, stdout, stderr io.Writer, logger *slog.Logger) int {
	ws, code := openWorkspace(cfg, stderr)
	if ws == nil {
		return code
	}
	cfg.Workspace = ws.Dir()
	defer closeWorkspace(ws, logger)

	if err := prepareStateDir(cfg.StateDir); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitConfig
	}

	sessionID := session.NewID()
	store, err := session.Create(cfg.StateDir, sessionID)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: create session log: %v\n", sanitizeText(err.Error()))
		return exitSetup
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Warn("close session log", "error", sanitizeText(err.Error()))
		}
	}()

	rcfg := runnerConfig(cfg)
	if err := agent.WriteSessionStart(store, rcfg, nil); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitSetup
	}
	return executeRun(cfg, store, ws, sessionID, stdin, stdout, stderr, logger, agent.RunOptions{
		Task:     cfg.Task,
		TaskSet:  cfg.TaskSet,
		TaskFrom: cfg.TaskSource,
	}, "session started")
}

func executeResume(cfg config, stdin io.Reader, stdout, stderr io.Writer, logger *slog.Logger) int {
	if err := prepareStateDir(cfg.StateDir); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitConfig
	}
	store, err := session.Open(cfg.StateDir, cfg.ResumeID)
	if err != nil {
		return sessionFailure(err, stderr)
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Warn("close session log", "error", sanitizeText(err.Error()))
		}
	}()

	state, err := agent.Inspect(store.Events())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: read session %s: %v\n", cfg.ResumeID, sanitizeText(err.Error()))
		return exitSetup
	}
	if err := applySessionDefaults(&cfg, state); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitConfig
	}
	ws, code := openSessionWorkspace(cfg, state, stderr)
	if ws == nil {
		return code
	}
	cfg.Workspace = ws.Dir()
	defer closeWorkspace(ws, logger)
	if err := validateSessionRules(&cfg, state); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitConfig
	}

	resumedFrom := lastSeq(store.Events())
	return executeRun(cfg, store, ws, store.ID(), stdin, stdout, stderr, logger, agent.RunOptions{
		Task:           cfg.Task,
		TaskSet:        cfg.TaskSet,
		TaskFrom:       cfg.TaskSource,
		ResumedFromSeq: resumedFrom,
	}, "session resumed")
}

func executeFork(cfg config, stdin io.Reader, stdout, stderr io.Writer, logger *slog.Logger) int {
	if err := prepareStateDir(cfg.StateDir); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitConfig
	}
	source, err := session.OpenShared(cfg.StateDir, cfg.ForkID)
	if err != nil {
		return sessionFailure(err, stderr)
	}
	defer func() {
		if err := source.Close(); err != nil {
			logger.Warn("close parent session log", "error", sanitizeText(err.Error()))
		}
	}()

	state, err := agent.Inspect(source.Events())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: read parent session %s: %v\n", cfg.ForkID, sanitizeText(err.Error()))
		return exitSetup
	}
	if err := applySessionDefaults(&cfg, state); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitConfig
	}
	ws, code := openSessionWorkspace(cfg, state, stderr)
	if ws == nil {
		return code
	}
	cfg.Workspace = ws.Dir()
	defer closeWorkspace(ws, logger)
	if err := validateSessionRules(&cfg, state); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitConfig
	}

	sessionID := session.NewID()
	store, err := session.Create(cfg.StateDir, sessionID)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: create forked session log: %v\n", sanitizeText(err.Error()))
		return exitSetup
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Warn("close session log", "error", sanitizeText(err.Error()))
		}
	}()

	rcfg := runnerConfig(cfg)
	parent := &agent.ParentRef{Session: source.ID(), Seq: source.LastSeq()}
	if err := agent.WriteSessionStart(store, rcfg, parent); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitSetup
	}
	if err := store.CopyHistory(source.Events()); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: copy parent session: %v\n", sanitizeText(err.Error()))
		return exitSetup
	}
	if err := store.Sync(); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: sync forked session: %v\n", sanitizeText(err.Error()))
		return exitSetup
	}
	if err := source.Close(); err != nil {
		logger.Warn("close parent session log", "error", sanitizeText(err.Error()))
	}

	resumedFrom := lastSeq(store.Events())
	return executeRun(cfg, store, ws, sessionID, stdin, stdout, stderr, logger, agent.RunOptions{
		Task:           cfg.Task,
		TaskSet:        cfg.TaskSet,
		TaskFrom:       cfg.TaskSource,
		ResumedFromSeq: resumedFrom,
	}, "session forked")
}

// executeRun prepares the work directory, installs signals and streaming, and
// performs one run over an already-open session.
func executeRun(cfg config, store *session.Store, ws *tools.Workspace, sessionID string, stdin io.Reader, stdout, stderr io.Writer, logger *slog.Logger, opts agent.RunOptions, action string) int {
	workDir, homeDir, tmpDir := workPaths(cfg.StateDir, sessionID)
	if err := resetWorkDirs(workDir, homeDir, tmpDir); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitSetup
	}
	defer removeWorkDir(workDir, stderr)

	logger.Info(action,
		"session", sessionID,
		"trace", store.Path(),
		"model", cfg.Model,
		"dialect", cfg.Dialect,
		"workspace", cfg.Workspace,
		"auth", authState(cfg.Token),
		"max_turns", cfg.MaxTurns,
		"max_context_tokens", cfg.MaxContextTokens,
		"max_total_tokens", cfg.MaxTotalTokens,
	)
	if cfg.Token == "" && cfg.BaseURL == model.DefaultBaseURL {
		logger.Warn("provider token is unset; hosted OpenCode Go requires AHARNESS_PROVIDER_TOKEN")
	}

	runCtx, controller, stopSignals := setupSignals(cfg.Interactive)
	defer stopSignals()
	var sink *textSink
	if cfg.Stream {
		sink = &textSink{w: stdout}
	}
	var steering <-chan string
	if cfg.Interactive {
		steering = startSteering(runCtx, stdin, stderr)
	}

	rcfg := runnerConfig(cfg)
	rcfg.Controller = controller
	if sink != nil {
		rcfg.EmitText = func(s string) error {
			_, err := sink.Write([]byte(s))
			return err
		}
	}
	rcfg.Steering = steering
	registry := tools.NewRegistry(ws, tools.Options{HomeDir: homeDir, TmpDir: tmpDir})
	runner := agent.New(rcfg, newCompleter(cfg, sessionID), registry, store)

	result, runErr := runner.Run(runCtx, opts)
	var sinkErr error
	if sink != nil {
		sinkErr = sink.finish()
	}
	closeErr := store.Close()

	status := result.Status
	if status == "" {
		status = agent.StopInternalError
	}
	if runErr != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(runErr.Error()))
		if cfg.Token == "" && authFailureHint(runErr) {
			_, _ = fmt.Fprintln(stderr, "aharness: hint: set AHARNESS_PROVIDER_TOKEN to an OpenCode Console API key")
		}
		if status == agent.StopCompleted {
			status = agent.StopInternalError
		}
	}
	if sinkErr != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: write streamed output: %v\n", sanitizeText(sinkErr.Error()))
		status = agent.StopInternalError
	}
	if closeErr != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: close session log: %v\n", sanitizeText(closeErr.Error()))
		// A failed close means the durable log cannot be trusted, regardless
		// of the loop's status.
		status = agent.StopInternalError
	}
	if !cfg.Stream && result.FinalText != "" {
		_, _ = fmt.Fprint(stdout, result.FinalText)
		if !strings.HasSuffix(result.FinalText, "\n") {
			_, _ = fmt.Fprintln(stdout)
		}
	}
	return status.ExitCode()
}

// runnerConfig converts the CLI configuration into the runner's immutable
// per-run configuration.
func runnerConfig(cfg config) agent.Config {
	return agent.Config{
		Provider:       cfg.Provider,
		Dialect:        string(cfg.Dialect),
		Model:          cfg.Model,
		BaseURL:        cfg.BaseURL,
		Workspace:      cfg.Workspace,
		StateDir:       cfg.StateDir,
		PromptVersion:  cfg.PromptVersion,
		ToolsetVersion: cfg.ToolsetVersion,
		HarnessVersion: cfg.HarnessVersion,
		MaxTurns:       cfg.MaxTurns,
		MaxTotalTokens: cfg.MaxTotalTokens,
		Limits:         cfg.Limits,
		SessionTimeout: cfg.SessionTimeout,
		ModelTimeout:   cfg.ModelTimeout,
		LogRequests:    cfg.LogRequests,
		Interactive:    cfg.Interactive,
		Stream:         cfg.Stream,
	}
}

// openWorkspace opens the configured workspace for a new session.
func openWorkspace(cfg config, stderr io.Writer) (*tools.Workspace, int) {
	ws, err := tools.OpenWorkspace(cfg.Workspace)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: open workspace %s: %v\n", cfg.Workspace, sanitizeText(err.Error()))
		return nil, exitConfig
	}
	return ws, 0
}

// openSessionWorkspace opens the effective workspace for resume or fork and
// requires it to match the workspace recorded in the session.
func openSessionWorkspace(cfg config, state agent.State, stderr io.Writer) (*tools.Workspace, int) {
	ws, err := tools.OpenWorkspace(cfg.Workspace)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: open workspace %s: %v\n", cfg.Workspace, sanitizeText(err.Error()))
		return nil, exitConfig
	}
	if ws.Dir() != state.Workspace {
		_ = ws.Close()
		_, _ = fmt.Fprintf(stderr, "aharness: workspace %s does not match the session workspace %s\n",
			sanitizeText(ws.Dir()), sanitizeText(state.Workspace))
		return nil, exitConfig
	}
	return ws, 0
}

func closeWorkspace(ws *tools.Workspace, logger *slog.Logger) {
	if err := ws.Close(); err != nil {
		logger.Warn("close workspace", "error", sanitizeText(err.Error()))
	}
}

// sessionFailure maps a session open/validate failure to the CLI exit code.
func sessionFailure(err error, stderr io.Writer) int {
	_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return exitConfig
	case errors.Is(err, session.ErrInvalidID),
		errors.Is(err, session.ErrLocked),
		errors.Is(err, session.ErrUnsupportedVersion),
		errors.Is(err, session.ErrMode):
		return exitConfig
	default:
		return exitSetup
	}
}

func newCompleter(cfg config, sessionID string) model.Completer {
	if cfg.Dialect == model.DialectResponses {
		return model.NewOpenAIResponses(cfg.BaseURL, cfg.Token, sessionID, cfg.HarnessVersion)
	}
	return model.NewOpenAIChat(cfg.BaseURL, cfg.Token, sessionID, cfg.HarnessVersion)
}

// authState reports whether a provider token is configured without exposing
// its value in logs.
func authState(token string) string {
	if token == "" {
		return "unset"
	}
	return "set"
}

// authFailureHint reports whether err is a provider 401, which is actionable
// as a missing or invalid AHARNESS_PROVIDER_TOKEN.
func authFailureHint(err error) bool {
	var httpErr *model.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == 401
}

// prepareStateDir creates the state directory with mode 0700 and verifies that
// the harness can write to it. It runs before any session state is created.
func prepareStateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state directory %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat state directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("state path %s is not a directory", dir)
	}
	if err := checkWritable(dir); err != nil {
		return fmt.Errorf("state directory %s is not writable: %w", dir, err)
	}
	return nil
}

func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".aharness-preflight-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, writeErr := f.Write([]byte("aharness\n"))
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func workPaths(stateDir, sessionID string) (workDir, homeDir, tmpDir string) {
	workDir = filepath.Join(stateDir, "work", sessionID)
	return workDir, filepath.Join(workDir, "home"), filepath.Join(workDir, "tmp")
}

// resetWorkDirs removes any stale per-session work tree and recreates the
// harness-owned HOME and TMPDIR. A failure to clear it is a startup failure,
// not a silent reuse.
func resetWorkDirs(workDir, homeDir, tmpDir string) error {
	if err := os.RemoveAll(workDir); err != nil {
		return fmt.Errorf("clear work directory %s: %w", workDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(workDir), 0o700); err != nil {
		return fmt.Errorf("create work directory %s: %w", filepath.Dir(workDir), err)
	}
	if err := os.Mkdir(workDir, 0o700); err != nil {
		return fmt.Errorf("create work directory %s: %w", workDir, err)
	}
	if err := os.Chmod(workDir, 0o700); err != nil {
		return fmt.Errorf("set work directory mode %s: %w", workDir, err)
	}
	for _, dir := range []string{homeDir, tmpDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return fmt.Errorf("create work directory %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("set work directory mode %s: %w", dir, err)
		}
	}
	return nil
}

// removeWorkDir removes the per-session work tree best-effort. A failure is
// reported with the leftover path and never changes the terminal status.
func removeWorkDir(dir string, stderr io.Writer) {
	if err := os.RemoveAll(dir); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: cleanup leftover work directory %s: %v\n", dir, sanitizeText(err.Error()))
	}
}

func lastSeq(events []session.Event) uint64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Seq
}
