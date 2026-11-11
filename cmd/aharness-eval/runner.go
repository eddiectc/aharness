package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"aharness/internal/session"
)

// Run statuses.
const (
	statusSolved   = "solved"
	statusUnsolved = "unsolved"
	statusInvalid  = "invalid"
	statusError    = "error"
)

// runRecord is one results.jsonl line.
type runRecord struct {
	Task        string     `json:"task"`
	Cluster     string     `json:"cluster"`
	Language    string     `json:"language"`
	Arm         string     `json:"arm"`
	Repeat      int        `json:"repeat"`
	Seed        int64      `json:"seed"`
	ArmOrder    []string   `json:"arm_order"`
	Status      string     `json:"status"`
	SetupExit   *int       `json:"setup_exit"`
	VerifyExit  *int       `json:"verify_exit"`
	HarnessExit *int       `json:"harness_exit"`
	DurationMS  int64      `json:"duration_ms"`
	Error       string     `json:"error"`
	StdoutTail  string     `json:"stdout_tail"`
	StderrTail  string     `json:"stderr_tail"`
	SessionPath string     `json:"session_path"`
	Metrics     runMetrics `json:"metrics"`
}

// execute runs the whole matrix. Arms rotate deterministically per task and
// repeat, which keeps the arm order balanced without randomizing ad hoc.
func execute(ctx context.Context, cfg config, tasks []task, prices *priceTable, progress io.Writer) ([]runRecord, error) {
	var records []runRecord
	for taskIndex, t := range tasks {
		for repeat := 0; repeat < cfg.Repeats; repeat++ {
			order := rotateArms(cfg.Arms, taskIndex, repeat, cfg.Seed)
			names := armNames(order)
			for _, a := range order {
				if err := ctx.Err(); err != nil {
					return nil, fmt.Errorf("eval canceled: %w", err)
				}
				record := runOnce(ctx, cfg, t, a, repeat+1, names, prices)
				records = append(records, record)
				if progress != nil {
					_, _ = fmt.Fprintf(progress, "eval: %s repeat %d %s: %s\n", t.ID, repeat+1, a.Name, record.Status)
				}
				if err := ctx.Err(); err != nil {
					return nil, fmt.Errorf("eval canceled: %w", err)
				}
			}
		}
	}
	return records, nil
}

// rotateArms returns the arms starting at (taskIndex + repeat + seed) % n.
func rotateArms(arms []arm, taskIndex, repeat int, seed int64) []arm {
	n := len(arms)
	if n == 0 {
		return nil
	}
	sum := int64(taskIndex) + int64(repeat) + seed
	start := int(((sum % int64(n)) + int64(n)) % int64(n))
	order := make([]arm, n)
	for i := range n {
		order[i] = arms[(start+i)%n]
	}
	return order
}

func armNames(arms []arm) []string {
	names := make([]string, len(arms))
	for i, a := range arms {
		names[i] = a.Name
	}
	return names
}

// runOnce executes setup, the harness, verification, and metrics extraction
// for one task/arm/repeat. Every run gets a fresh workspace and state
// directory under one temporary root, which is removed after metrics are read.
func runOnce(ctx context.Context, cfg config, t task, a arm, repeat int, order []string, prices *priceTable) runRecord {
	record := runRecord{
		Task:     t.ID,
		Cluster:  t.Meta.Cluster,
		Language: t.Meta.Language,
		Arm:      a.Name,
		Repeat:   repeat,
		Seed:     cfg.Seed,
		ArmOrder: order,
		Status:   statusError,
		Metrics:  runMetrics{ReadCallsPerFile: []fileCallCount{}},
	}
	root, err := os.MkdirTemp("", "aharness-eval-")
	if err != nil {
		record.Error = fmt.Sprintf("create temporary run root: %v", err)
		return record
	}
	defer func() {
		// Best effort: a leftover temp directory is not a result.
		_ = os.RemoveAll(root)
	}()
	workspace := filepath.Join(root, "workspace")
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		record.Error = fmt.Sprintf("create workspace: %v", err)
		return record
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		record.Error = fmt.Sprintf("create state directory: %v", err)
		return record
	}
	env := withEnv(os.Environ(),
		"WORKSPACE", workspace,
		"TASK_DIR", t.Dir,
		"TASK_ID", t.ID,
	)

	setup := runProcess(ctx, t.Timeout, "/bin/sh", []string{filepath.Join(t.Dir, setupFile)}, t.Dir, env)
	record.SetupExit = exitPointer(setup)
	if ctx.Err() != nil {
		record.Error = phaseMessage("setup", setup, t.Timeout)
		return record
	}
	if setup.StartErr != nil || setup.TimedOut || setup.ExitCode != 0 {
		record.Status = statusInvalid
		record.Error = phaseMessage("setup", setup, t.Timeout)
		return record
	}

	args := []string{
		"-batch",
		"-workspace", workspace,
		"-state-dir", stateDir,
		"-task-file", filepath.Join(t.Dir, promptFile),
		"-model", cfg.Model,
		"-thinking-effort", cfg.ThinkingEffort,
		"-require-usage",
	}
	harness := runProcess(ctx, t.Timeout, a.Path, args, workspace, withEnv(os.Environ(), "AHARNESS_LOG_REQUESTS", "1"))
	record.HarnessExit = exitPointer(harness)
	record.DurationMS = harness.Duration.Milliseconds()
	record.StdoutTail = harness.Stdout
	record.StderrTail = harness.Stderr
	if ctx.Err() != nil {
		record.Error = phaseMessage("harness", harness, t.Timeout)
		return record
	}

	var problems []string
	if harness.StartErr != nil {
		problems = append(problems, phaseMessage("harness", harness, t.Timeout))
	} else if harness.TimedOut {
		problems = append(problems, phaseMessage("harness", harness, t.Timeout))
	} else if harness.ExitCode != 0 {
		problems = append(problems, phaseMessage("harness", harness, t.Timeout))
	}

	sessionPath, err := findSession(stateDir)
	if err != nil {
		problems = append(problems, err.Error())
	} else {
		record.SessionPath = sessionPath
		events, readErr := session.Read(sessionPath)
		if readErr != nil {
			problems = append(problems, fmt.Sprintf("read session log: %v", readErr))
		} else {
			metrics, metricsErr := extractMetrics(events, prices)
			if metricsErr != nil {
				problems = append(problems, fmt.Sprintf("extract metrics: %v", metricsErr))
			} else {
				record.Metrics = metrics
			}
		}
	}

	verify := runProcess(ctx, t.Timeout, "/bin/sh", []string{filepath.Join(t.Dir, verifyFile)}, t.Dir, env)
	record.VerifyExit = exitPointer(verify)
	if ctx.Err() != nil {
		record.Error = phaseMessage("verify", verify, t.Timeout)
		return record
	}

	switch {
	case len(problems) > 0:
		record.Status = statusError
	case verify.StartErr != nil:
		record.Status = statusError
		problems = append(problems, phaseMessage("verify", verify, t.Timeout))
	case verify.TimedOut:
		record.Status = statusUnsolved
		problems = append(problems, phaseMessage("verify", verify, t.Timeout))
	case verify.ExitCode != 0:
		record.Status = statusUnsolved
		problems = append(problems, phaseMessage("verify", verify, t.Timeout))
	default:
		record.Status = statusSolved
	}
	record.Error = strings.Join(problems, "; ")
	return record
}

func exitPointer(result processResult) *int {
	code := result.ExitCode
	return &code
}

// phaseMessage describes a phase failure without matching error strings.
func phaseMessage(phase string, result processResult, timeout time.Duration) string {
	switch {
	case result.Canceled:
		return phase + " canceled"
	case result.StartErr != nil:
		return fmt.Sprintf("%s failed to start: %v", phase, result.StartErr)
	case result.TimedOut:
		return fmt.Sprintf("%s timed out after %s", phase, timeout)
	default:
		return fmt.Sprintf("%s exited %d", phase, result.ExitCode)
	}
}

// findSession returns the single session log written under stateDir.
func findSession(stateDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(stateDir, "sessions", "*.jsonl"))
	if err != nil {
		return "", fmt.Errorf("find session log: %w", err)
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session log under %s", filepath.Join(stateDir, "sessions"))
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%d session logs under %s, want exactly one", len(matches), filepath.Join(stateDir, "sessions"))
	}
}

// processResult is the outcome of one bounded subprocess.
type processResult struct {
	ExitCode int
	TimedOut bool
	Canceled bool
	StartErr error
	Duration time.Duration
	Stdout   string
	Stderr   string
}

// runProcess runs a command with a timeout, kills its process group, and caps
// the captured output tails.
func runProcess(parent context.Context, timeout time.Duration, name string, args []string, dir string, env []string) processResult {
	result := processResult{ExitCode: -1}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = nil
	configureProcess(cmd)
	var stdout, stderr cappedTail
	stdout.limit = tailLimit
	stderr.limit = tailLimit
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	result.Duration = time.Since(start)
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case parent.Err() != nil:
		result.Canceled = true
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.TimedOut = true
	case err != nil:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			result.StartErr = fmt.Errorf("run %s: %w", name, err)
		}
	}
	return result
}

// cappedTail retains at most limit bytes of a stream, discarding the oldest.
type cappedTail struct {
	limit int
	buf   []byte
}

func (w *cappedTail) Write(p []byte) (int, error) {
	n := len(p)
	if w.limit <= 0 {
		return n, nil
	}
	if len(p) >= w.limit {
		w.buf = append(w.buf[:0], p[len(p)-w.limit:]...)
		return n, nil
	}
	if overflow := len(w.buf) + len(p) - w.limit; overflow > 0 {
		w.buf = append(w.buf[:0], w.buf[overflow:]...)
	}
	w.buf = append(w.buf, p...)
	return n, nil
}

func (w *cappedTail) String() string {
	return strings.ToValidUTF8(string(w.buf), "\uFFFD")
}

// withEnv returns base with the provided key/value pairs replacing any
// existing entries for the same keys.
func withEnv(base []string, kv ...string) []string {
	keys := make(map[string]bool, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		keys[kv[i]] = true
	}
	merged := make([]string, 0, len(base)+len(kv)/2)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if keys[key] {
			continue
		}
		merged = append(merged, entry)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		merged = append(merged, kv[i]+"="+kv[i+1])
	}
	return merged
}
