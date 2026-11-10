package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

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
	return runArgs(os.Args[1:], os.Stdout, os.Stderr)
}

// runArgs is the testable CLI entry point.
func runArgs(args []string, stdout, stderr io.Writer) int {
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
	return execute(cfg, stdout, stderr)
}

// execute wires the workspace, session state, provider adapter, tool registry,
// and runner, then performs terminal cleanup. Configuration has already been
// validated, so failures here are startup or run failures.
func execute(cfg config, stdout, stderr io.Writer) int {
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ws, err := tools.OpenWorkspace(cfg.Workspace)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: open workspace %s: %v\n", cfg.Workspace, sanitizeText(err.Error()))
		return exitConfig
	}
	cfg.Workspace = ws.Dir()
	defer func() {
		if err := ws.Close(); err != nil {
			logger.Warn("close workspace", "error", sanitizeText(err.Error()))
		}
	}()

	if err := prepareStateDir(cfg.StateDir); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitConfig
	}

	sessionID := session.NewID()
	workDir := filepath.Join(cfg.StateDir, "work", sessionID)
	homeDir := filepath.Join(workDir, "home")
	tmpDir := filepath.Join(workDir, "tmp")
	created, err := createWorkDirs(workDir, homeDir, tmpDir)
	if err != nil {
		if created {
			removeWorkDir(workDir, stderr)
		}
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitSetup
	}

	store, err := session.NewStore(cfg.StateDir, sessionID)
	if err != nil {
		removeWorkDir(workDir, stderr)
		_, _ = fmt.Fprintf(stderr, "aharness: create session log: %v\n", sanitizeText(err.Error()))
		return exitSetup
	}
	defer removeWorkDir(workDir, stderr)

	logger.Info("session started",
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

	registry := tools.NewRegistry(ws, tools.Options{HomeDir: homeDir, TmpDir: tmpDir})
	runner := agent.New(agent.Config{
		Provider:       cfg.Provider,
		Dialect:        string(cfg.Dialect),
		Model:          cfg.Model,
		Workspace:      cfg.Workspace,
		StateDir:       cfg.StateDir,
		HarnessVersion: cfg.HarnessVersion,
		MaxTurns:       cfg.MaxTurns,
		MaxTotalTokens: cfg.MaxTotalTokens,
		Limits:         cfg.Limits,
		SessionTimeout: cfg.SessionTimeout,
		ModelTimeout:   cfg.ModelTimeout,
		LogRequests:    cfg.LogRequests,
		Logger:         logger,
	}, newCompleter(cfg, sessionID), registry, store)

	result, runErr := runner.Run(ctx, cfg.Task, cfg.TaskSource)
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
	if closeErr != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: close session log: %v\n", sanitizeText(closeErr.Error()))
		// A failed close means the durable log cannot be trusted, regardless
		// of the loop's status.
		status = agent.StopInternalError
	}
	if result.FinalText != "" {
		_, _ = fmt.Fprint(stdout, result.FinalText)
		if !strings.HasSuffix(result.FinalText, "\n") {
			_, _ = fmt.Fprintln(stdout)
		}
	}
	return status.ExitCode()
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

// createWorkDirs creates the per-session work subtree before the session log
// is created. It reports whether workDir was created so the caller removes
// only a directory the harness owns.
func createWorkDirs(workDir, homeDir, tmpDir string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(workDir), 0o700); err != nil {
		return false, fmt.Errorf("create work directory %s: %w", filepath.Dir(workDir), err)
	}
	if err := os.Mkdir(workDir, 0o700); err != nil {
		return false, fmt.Errorf("create work directory %s: %w", workDir, err)
	}
	if err := os.Chmod(workDir, 0o700); err != nil {
		return true, fmt.Errorf("set work directory mode %s: %w", workDir, err)
	}
	for _, dir := range []string{homeDir, tmpDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return true, fmt.Errorf("create work directory %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return true, fmt.Errorf("set work directory mode %s: %w", dir, err)
		}
	}
	return true, nil
}

// removeWorkDir removes the per-session work tree best-effort. A failure is
// reported with the leftover path and never changes the terminal status.
func removeWorkDir(dir string, stderr io.Writer) {
	if err := os.RemoveAll(dir); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: cleanup leftover work directory %s: %v\n", dir, sanitizeText(err.Error()))
	}
}
