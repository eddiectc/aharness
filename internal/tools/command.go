package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultCommandTimeoutMS = 120_000
	maxCommandTimeoutMS     = 600_000
	commandWaitDelay        = 5 * time.Second
)

// runCommandArgs is the typed argument shape for run_command. It must agree
// with the run_command schema in schemas.go.
type runCommandArgs struct {
	Command   string  `json:"command"`
	Cwd       *string `json:"cwd,omitempty"`
	TimeoutMS *int    `json:"timeout_ms,omitempty"`
}

func (r *Registry) executeRunCommand(ctx context.Context, args json.RawMessage) Result {
	var a runCommandArgs
	if err := decodeArgs("run_command", args, &a); err != nil {
		return Failure(CodeInvalidArgs, err.Error())
	}
	if a.Command == "" {
		return invalidArgs("run_command", "command is required")
	}
	cwd := "."
	if a.Cwd != nil {
		cwd = *a.Cwd
		if cwd == "" {
			return invalidArgs("run_command", "cwd must not be empty")
		}
	}
	timeoutMS := defaultCommandTimeoutMS
	if a.TimeoutMS != nil {
		timeoutMS = *a.TimeoutMS
		if timeoutMS < 1 {
			return invalidArgs("run_command", "timeout_ms must be at least 1")
		}
		if timeoutMS > maxCommandTimeoutMS {
			timeoutMS = maxCommandTimeoutMS
		}
	}

	rel, err := r.workspaceRel(cwd)
	if err != nil {
		return errorResult(err)
	}
	dir, err := r.ws.resolveDir(rel)
	if err != nil {
		return errorResult(fmt.Errorf("cwd %s: %w", cwd, err))
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	stdout := &cappedBuffer{max: commandOutputCap}
	stderr := &cappedBuffer{max: commandOutputCap}
	spill := newSpillCapture(r.opts.WorkDir, commandOutputCap)
	cmd := newShellCommand(runCtx, a.Command, dir, commandEnv(r.opts), commandWaitDelay)
	cmd.Stdout = spillWriter{inline: stdout, capture: spill, stream: 0}
	cmd.Stderr = spillWriter{inline: stderr, capture: spill, stream: 1}
	cmd.Stdin = nil

	start := time.Now()
	if err := cmd.Start(); err != nil {
		elapsed := time.Since(start).Milliseconds()
		if code, msg, timedOut := commandContextFailure(ctx, runCtx); code != "" {
			return Result{OK: false, Code: code, Error: msg, TimedOut: timedOut, DurationMS: elapsed}
		}
		return Result{OK: false, Code: CodeSpawnFailed, Error: sanitizeUTF8(fmt.Sprintf("start command: %v", err)), DurationMS: elapsed}
	}
	waitErr := cmd.Wait()
	elapsed := time.Since(start).Milliseconds()

	out, streamTruncated := commandOutput(stdout, stderr)
	out, truncated, details := finalizeCommandOutput(out, streamTruncated, spill)
	if code, msg, timedOut := commandContextFailure(ctx, runCtx); code != "" {
		return Result{
			OK:         false,
			Code:       code,
			Output:     out,
			Error:      msg,
			TimedOut:   timedOut,
			Truncated:  truncated,
			Details:    details,
			DurationMS: elapsed,
		}
	}

	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		code := 0
		return Result{OK: true, Code: CodeOK, Output: out, ExitCode: &code, Truncated: truncated, Details: details, DurationMS: elapsed}
	case errors.As(waitErr, &exitErr):
		code := exitErr.ExitCode()
		return Result{OK: true, Code: CodeOK, Output: out, ExitCode: &code, Truncated: truncated, Details: details, DurationMS: elapsed}
	case cmd.ProcessState != nil:
		// WaitDelay (or a post-exit wait error) can surface after the process
		// itself finished; the process status is still the normal result.
		code := cmd.ProcessState.ExitCode()
		return Result{OK: true, Code: CodeOK, Output: out, ExitCode: &code, Truncated: truncated, Details: details, DurationMS: elapsed}
	default:
		return Result{
			OK:         false,
			Code:       CodeSpawnFailed,
			Output:     out,
			Error:      sanitizeUTF8(fmt.Sprintf("wait for command: %v", waitErr)),
			Truncated:  truncated,
			Details:    details,
			DurationMS: elapsed,
		}
	}
}

// commandContextFailure classifies a canceled or timed-out command context.
// The parent context's cancellation is reported as canceled; a deadline is a
// timeout whether it came from the tool timeout or a parent deadline.
func commandContextFailure(parent, runCtx context.Context) (Code, string, bool) {
	if err := parent.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return CodeCanceled, "command canceled", false
		}
		return CodeTimeout, "command timed out", true
	}
	if err := runCtx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return CodeTimeout, "command timed out", true
		}
		return CodeCanceled, "command canceled", false
	}
	return "", "", false
}

// commandOutput renders labeled streams and applies the combined cap.
func commandOutput(stdout, stderr *cappedBuffer) (string, bool) {
	stdoutStr, stdoutTruncated := stdout.render()
	stderrStr, stderrTruncated := stderr.render()
	stdoutStr = sanitizeUTF8(stdoutStr)
	stderrStr = sanitizeUTF8(stderrStr)

	var b strings.Builder
	if stdoutStr != "" {
		b.WriteString("stdout:\n")
		b.WriteString(stdoutStr)
		if !strings.HasSuffix(stdoutStr, "\n") {
			b.WriteByte('\n')
		}
	}
	if stderrStr != "" {
		b.WriteString("stderr:\n")
		b.WriteString(stderrStr)
		if !strings.HasSuffix(stderrStr, "\n") {
			b.WriteByte('\n')
		}
	}
	out, combinedTruncated := capOutput(b.String(), commandOutputCap)
	return out, combinedTruncated || stdoutTruncated || stderrTruncated
}

// commandEnv returns the explicit child environment allowlist. It never
// inherits os.Environ wholesale.
func commandEnv(opts Options) []string {
	env := make([]string, 0, 6)
	add := func(name, value string) {
		if envDenied(name) {
			return
		}
		env = append(env, name+"="+value)
	}
	for _, name := range []string{"PATH", "LANG", "LC_ALL", "TERM"} {
		if value, ok := os.LookupEnv(name); ok {
			add(name, value)
		}
	}
	if opts.HomeDir != "" {
		add("HOME", opts.HomeDir)
	}
	if opts.TmpDir != "" {
		add("TMPDIR", opts.TmpDir)
	}
	return env
}

// envDenied reports whether a variable name matches the defense-in-depth
// credential denylist.
func envDenied(name string) bool {
	upper := strings.ToUpper(name)
	for _, suffix := range []string{"_TOKEN", "_SECRET", "_KEY", "_PASSWORD"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}
