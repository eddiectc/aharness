//go:build unix

package tools

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func newCommandRegistry(t *testing.T) (r *Registry, dir, home, tmp string) {
	t.Helper()
	ws, dir := newTestWorkspace(t)
	home = t.TempDir()
	tmp = t.TempDir()
	t.Setenv("HOME", home)
	return NewRegistry(ws, Options{TmpDir: tmp}), dir, home, tmp
}

func TestRunCommandNonZeroExit(t *testing.T) {
	r, _, _, _ := newCommandRegistry(t)
	res := executeArgs(t, r, "run_command", map[string]any{
		"command": "echo out; echo err >&2; exit 7",
	})
	if !res.OK {
		t.Fatalf("run_command(non-zero) OK = false: code=%s error=%s", res.Code, res.Error)
	}
	if res.Code != CodeOK {
		t.Errorf("code = %s, want %s", res.Code, CodeOK)
	}
	if res.ExitCode == nil {
		t.Fatalf("ExitCode = nil, want 7")
	}
	if *res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", *res.ExitCode)
	}
	if !strings.Contains(res.Output, "stdout:\nout") {
		t.Errorf("output missing labeled stdout: %q", res.Output)
	}
	if !strings.Contains(res.Output, "stderr:\nerr") {
		t.Errorf("output missing labeled stderr: %q", res.Output)
	}
	if res.TimedOut {
		t.Errorf("TimedOut = true for a normal non-zero exit")
	}
}

func TestRunCommandSuccess(t *testing.T) {
	r, _, _, _ := newCommandRegistry(t)
	res := executeArgs(t, r, "run_command", map[string]any{"command": "printf out"})
	if !res.OK {
		t.Fatalf("run_command(printf) failed: code=%s error=%s", res.Code, res.Error)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want pointer to 0", res.ExitCode)
	}
	if want := "stdout:\nout\n"; res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}

	res = executeArgs(t, r, "run_command", map[string]any{"command": "true"})
	if !res.OK {
		t.Fatalf("run_command(true) failed: %s", res.Error)
	}
	if res.Output != "" {
		t.Errorf("run_command(true) output = %q, want empty", res.Output)
	}
}

func TestRunCommandSpawnFailure(t *testing.T) {
	r, _, _, _ := newCommandRegistry(t)
	// A NUL byte cannot be passed through execve; Start must fail.
	res := executeArgs(t, r, "run_command", map[string]any{"command": "echo \x00"})
	if res.OK {
		t.Fatalf("run_command(NUL command) OK = true, want spawn failure")
	}
	if res.Code != CodeSpawnFailed {
		t.Errorf("code = %s, want %s (error %q)", res.Code, CodeSpawnFailed, res.Error)
	}
	if res.Error == "" {
		t.Errorf("Error is empty, want the spawn error")
	}
}

func TestRunCommandTimeoutKillsProcessGroup(t *testing.T) {
	r, _, _, _ := newCommandRegistry(t)
	start := time.Now()
	res := executeArgs(t, r, "run_command", map[string]any{
		"command":    "sleep 30 & wait",
		"timeout_ms": 100,
	})
	elapsed := time.Since(start)
	if res.OK {
		t.Fatalf("run_command(timeout) OK = true, want timeout")
	}
	if res.Code != CodeTimeout {
		t.Errorf("code = %s, want %s (error %q)", res.Code, CodeTimeout, res.Error)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut = false, want true")
	}
	// Without the process-group kill, the background sleep would hold the
	// output pipes open until WaitDelay (5s).
	if elapsed > 3*time.Second {
		t.Errorf("timeout took %v; process group was probably not killed", elapsed)
	}
	if res.DurationMS > 3000 {
		t.Errorf("DurationMS = %d, want < 3000", res.DurationMS)
	}
}

func TestRunCommandParentCancel(t *testing.T) {
	r, _, _, _ := newCommandRegistry(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	raw, err := jsonv2.Marshal(map[string]any{"command": "sleep 30", "timeout_ms": 5000})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res := r.Execute(ctx, "run_command", raw)
	if res.OK || res.Code != CodeCanceled {
		t.Errorf("run_command(canceled) = ok=%v code=%s, want canceled", res.OK, res.Code)
	}
	if res.TimedOut {
		t.Errorf("TimedOut = true for parent cancellation, want false")
	}
}

func TestRunCommandOutputCap(t *testing.T) {
	r, _, _, _ := newCommandRegistry(t)
	res := executeArgs(t, r, "run_command", map[string]any{
		"command": "head -c 200000 /dev/zero | tr '\\000' 'x'",
	})
	if !res.OK {
		t.Fatalf("run_command(output cap) failed: code=%s error=%s", res.Code, res.Error)
	}
	if !res.Truncated {
		t.Errorf("Truncated = false, want true")
	}
	if len(res.Output) > commandOutputCap {
		t.Errorf("output length = %d, want <= %d", len(res.Output), commandOutputCap)
	}
	if !strings.Contains(res.Output, "[truncated ") {
		t.Errorf("output missing truncation marker")
	}
	if len(res.Details) != 0 {
		t.Errorf("Details = %s, want none when WorkDir is empty", res.Details)
	}
	if strings.Contains(res.Output, "full output:") {
		t.Errorf("output advertises a spill path without a work dir")
	}
}

func newSpillRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	ws, _ := newTestWorkspace(t)
	work := t.TempDir()
	return NewRegistry(ws, Options{
		TmpDir:  t.TempDir(),
		WorkDir: work,
	}), work
}

func TestRunCommandOutputSpill(t *testing.T) {
	r, work := newSpillRegistry(t)
	res := executeArgs(t, r, "run_command", map[string]any{
		"command": "head -c 1000000 /dev/zero | tr '\\000' 'x'",
	})
	if !res.OK {
		t.Fatalf("run_command(spill) failed: code=%s error=%s", res.Code, res.Error)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("ExitCode = %v, want 0", res.ExitCode)
	}
	if !res.Truncated {
		t.Errorf("Truncated = false, want true")
	}
	if len(res.Output) > commandOutputCap {
		t.Errorf("inline output length = %d, want <= %d", len(res.Output), commandOutputCap)
	}
	if len(res.Details) == 0 {
		t.Fatalf("Details is empty, want spilled path")
	}
	var details struct {
		Spilled bool   `json:"spilled"`
		Path    string `json:"path"`
	}
	if err := jsonv2.Unmarshal(res.Details, &details); err != nil {
		t.Fatalf("decode details: %v", err)
	}
	if !details.Spilled {
		t.Errorf("details.spilled = false, want true")
	}
	if details.Path == "" || !filepath.IsAbs(details.Path) {
		t.Fatalf("spill path = %q, want an absolute path", details.Path)
	}
	spillDir := filepath.Join(work, spillDirName)
	if !strings.HasPrefix(details.Path, spillDir+string(filepath.Separator)) {
		t.Errorf("spill path %q escapes %q", details.Path, spillDir)
	}
	if !strings.Contains(res.Output, "\nfull output: "+details.Path+"\n") {
		t.Errorf("inline output does not name the spill path %q", details.Path)
	}

	info, err := os.Stat(details.Path)
	if err != nil {
		t.Fatalf("stat spill file: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("spill file mode = %v, want a regular file", info.Mode())
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("spill file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(spillDir)
	if err != nil {
		t.Fatalf("stat spill dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("spill dir mode = %o, want 700", got)
	}
	content, err := os.ReadFile(details.Path)
	if err != nil {
		t.Fatalf("read spill file: %v", err)
	}
	if len(content) != 1_000_000 {
		t.Errorf("spill length = %d, want 1000000", len(content))
	}
	if len(content) > 0 && (content[0] != 'x' || content[len(content)-1] != 'x') {
		t.Errorf("spill content is not the raw command output")
	}

	if err := os.RemoveAll(work); err != nil {
		t.Fatalf("remove work dir: %v", err)
	}
	if _, err := os.Stat(details.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("spill file survived work-dir cleanup: %v", err)
	}
}

func TestRunCommandNoSpillUnderCap(t *testing.T) {
	r, work := newSpillRegistry(t)
	res := executeArgs(t, r, "run_command", map[string]any{
		"command": "head -c 1000 /dev/zero | tr '\\000' 'x'",
	})
	if !res.OK {
		t.Fatalf("run_command(under cap) failed: %s", res.Error)
	}
	if res.Truncated {
		t.Errorf("Truncated = true, want false")
	}
	if len(res.Details) != 0 {
		t.Errorf("Details = %s, want none under the cap", res.Details)
	}
	if strings.Contains(res.Output, "full output:") {
		t.Errorf("output advertises a spill path under the cap")
	}
	if entries, err := os.ReadDir(filepath.Join(work, spillDirName)); err == nil && len(entries) > 0 {
		t.Errorf("spill directory has %d entries under the cap", len(entries))
	}
}

func TestRunCommandSpillRejectsSymlinkDirectory(t *testing.T) {
	r, work := newSpillRegistry(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(work, spillDirName)); err != nil {
		t.Fatalf("symlink spill dir: %v", err)
	}
	res := executeArgs(t, r, "run_command", map[string]any{
		"command": "head -c 200000 /dev/zero | tr '\\000' 'x'",
	})
	if !res.OK {
		t.Fatalf("run_command(symlink spill dir) failed: code=%s error=%s", res.Code, res.Error)
	}
	if !res.Truncated {
		t.Errorf("Truncated = false, want true for oversized inline output")
	}
	if len(res.Details) != 0 {
		t.Errorf("Details = %s, want none when the spill directory is a symlink", res.Details)
	}
	if strings.Contains(res.Output, "full output:") {
		t.Errorf("output advertises a spill path that was not created")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("spill escaped through a symlink: %d entries", len(entries))
	}
}

func TestRunCommandSanitizesUTF8(t *testing.T) {
	r, _, _, _ := newCommandRegistry(t)
	res := executeArgs(t, r, "run_command", map[string]any{
		"command": `printf '\377\376ok\n'`,
	})
	if !res.OK {
		t.Fatalf("run_command(invalid utf8) failed: code=%s error=%s", res.Code, res.Error)
	}
	if !utf8.ValidString(res.Output) {
		t.Errorf("output is not valid UTF-8: %q", res.Output)
	}
	if !strings.Contains(res.Output, "ok") {
		t.Errorf("output lost the valid text: %q", res.Output)
	}
	if !strings.Contains(res.Output, "\uFFFD") {
		t.Errorf("output does not mark replaced bytes: %q", res.Output)
	}
}

func TestRunCommandEnvAllowlist(t *testing.T) {
	r, _, home, tmp := newCommandRegistry(t)
	t.Setenv("AHARNESS_PROVIDER_TOKEN", "provider-secret-xyz")
	t.Setenv("SOME_SECRET", "secret-xyz")
	t.Setenv("SOME_TOKEN", "token-xyz")
	t.Setenv("HOST_ONLY", "host-only-xyz")
	t.Setenv("HOME", home)
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("TERM", "xterm-test")

	res := executeArgs(t, r, "run_command", map[string]any{"command": `
printf 'HOME=%s\n' "$HOME"
printf 'TMPDIR=%s\n' "$TMPDIR"
printf 'LANG=%s\n' "$LANG"
printf 'TERM=%s\n' "$TERM"
printf 'HOST_ONLY=%s\n' "${HOST_ONLY-unset}"
printf 'TOKEN=%s\n' "${AHARNESS_PROVIDER_TOKEN-unset}"
printf 'SOME_SECRET=%s\n' "${SOME_SECRET-unset}"
printf 'SOME_TOKEN=%s\n' "${SOME_TOKEN-unset}"
`})
	if !res.OK {
		t.Fatalf("run_command(env) failed: code=%s error=%s", res.Code, res.Error)
	}
	for _, forbidden := range []string{"provider-secret-xyz", "host-only-xyz", "secret-xyz", "token-xyz"} {
		if strings.Contains(res.Output, forbidden) {
			t.Errorf("child environment contains %q: %q", forbidden, res.Output)
		}
	}
	for _, want := range []string{
		"HOST_ONLY=unset",
		"TOKEN=unset",
		"SOME_SECRET=unset",
		"SOME_TOKEN=unset",
	} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("child environment check missing %q: %q", want, res.Output)
		}
	}
	if !strings.Contains(res.Output, "LANG=C.UTF-8") {
		t.Errorf("child environment is missing LANG: %q", res.Output)
	}
	if !strings.Contains(res.Output, "TERM=xterm-test") {
		t.Errorf("child environment is missing TERM: %q", res.Output)
	}
	// HOME is inherited so git, ssh, and language toolchains find their user
	// configuration; isolation is the operator's external boundary.
	if !strings.Contains(res.Output, "HOME="+home) {
		t.Errorf("child environment is missing inherited HOME %q: %q", home, res.Output)
	}
	if !strings.Contains(res.Output, "TMPDIR="+tmp) {
		t.Errorf("child environment is missing harness TMPDIR %q: %q", tmp, res.Output)
	}
}

// TestRunCommandInheritsGitIdentity covers the reason HOME is inherited: a
// commit inside the child must find the host user identity.
func TestRunCommandInheritsGitIdentity(t *testing.T) {
	r, _, home, _ := newCommandRegistry(t)
	gitconfig := filepath.Join(home, ".gitconfig")
	content := "[user]\n\tname = Test User\n\temail = test@example.com\n"
	if err := os.WriteFile(gitconfig, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res := executeArgs(t, r, "run_command", map[string]any{
		"command": `git config --global user.email; git config --global user.name`,
	})
	if !res.OK {
		t.Fatalf("run_command(git config) failed: code=%s error=%s", res.Code, res.Error)
	}
	for _, want := range []string{"test@example.com", "Test User"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("child cannot read inherited git identity %q: %q", want, res.Output)
		}
	}
}

func TestCommandEnv(t *testing.T) {
	t.Setenv("PATH", "/test/bin")
	t.Setenv("LANG", "C")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("TERM", "xterm")
	t.Setenv("HOME", "/host/home")
	t.Setenv("GITHUB_TOKEN", "nope")
	env := commandEnv(Options{TmpDir: "/state/tmp"})
	want := []string{
		"PATH=/test/bin",
		"LANG=C",
		"LC_ALL=C.UTF-8",
		"TERM=xterm",
		"HOME=/host/home",
		"TMPDIR=/state/tmp",
	}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Errorf("env = %q, want %q", env, want)
	}
}

// TestCommandEnvNoHome proves an unset HOME is not invented: the child sees the
// harness environment as-is rather than a fabricated path.
func TestCommandEnvNoHome(t *testing.T) {
	t.Setenv("PATH", "/test/bin")
	t.Setenv("HOME", "")
	t.Setenv("GITHUB_TOKEN", "nope")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	env := commandEnv(Options{})
	for _, entry := range env {
		if strings.HasPrefix(entry, "HOME=") {
			t.Errorf("env contains %q with HOME unset", entry)
		}
	}
}

func TestEnvDenied(t *testing.T) {
	for _, name := range []string{"GITHUB_TOKEN", "a_secret", "FOO_KEY", "x_Password"} {
		if !envDenied(name) {
			t.Errorf("envDenied(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "LANG", "KEY", "TOKEN"} {
		if envDenied(name) {
			t.Errorf("envDenied(%q) = true, want false", name)
		}
	}
}

func TestRunCommandNilStdin(t *testing.T) {
	r, _, _, _ := newCommandRegistry(t)
	start := time.Now()
	res := executeArgs(t, r, "run_command", map[string]any{"command": "cat", "timeout_ms": 5000})
	elapsed := time.Since(start)
	if !res.OK {
		t.Fatalf("run_command(cat) failed: code=%s error=%s", res.Code, res.Error)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", res.ExitCode)
	}
	if elapsed > 3*time.Second {
		t.Errorf("cat with nil stdin took %v, want EOF immediately", elapsed)
	}
}

func TestRunCommandCwd(t *testing.T) {
	r, dir, _, _ := newCommandRegistry(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	res := executeArgs(t, r, "run_command", map[string]any{"command": "pwd", "cwd": "sub"})
	if !res.OK {
		t.Fatalf("run_command(pwd) failed: code=%s error=%s", res.Code, res.Error)
	}
	if want := filepath.Join(dir, "sub"); !strings.Contains(res.Output, want) {
		t.Errorf("pwd output = %q, want it to contain %q", res.Output, want)
	}
}

func TestRunCommandCwdContainment(t *testing.T) {
	r, dir, _, _ := newCommandRegistry(t)
	mustWriteFile(t, dir, "file.txt", []byte("x"))
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}

	tests := []struct {
		name string
		cwd  string
		code Code
	}{
		{"parent", "..", CodeOutsideWorkspace},
		{"absolute outside", outside, CodeOutsideWorkspace},
		{"git", ".git", CodeGitProtected},
		{"missing", "missing", CodeNotFound},
		{"file", "file.txt", CodeInvalidArgs},
		{"symlink escape", "escape", CodeOutsideWorkspace},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := executeArgs(t, r, "run_command", map[string]any{"command": "true", "cwd": tc.cwd})
			if res.OK || res.Code != tc.code {
				t.Errorf("run_command(cwd %q) = ok=%v code=%s, want %s (error %q)", tc.cwd, res.OK, res.Code, tc.code, res.Error)
			}
		})
	}
}

func TestRunCommandInvalidArgs(t *testing.T) {
	r, _, _, _ := newCommandRegistry(t)
	tests := []map[string]any{
		{"command": ""},
		{"command": "true", "timeout_ms": 0},
		{"command": "true", "timeout_ms": -1},
		{"command": "true", "cwd": ""},
	}
	for _, args := range tests {
		res := executeArgs(t, r, "run_command", args)
		if res.OK || res.Code != CodeInvalidArgs {
			t.Errorf("run_command(%v) = ok=%v code=%s, want invalid_args", args, res.OK, res.Code)
		}
	}

	res := executeArgs(t, r, "run_command", map[string]any{"command": "true", "timeout_ms": 600001})
	if !res.OK {
		t.Errorf("run_command(timeout over max) = ok=false code=%s error=%s, want the timeout clamped", res.Code, res.Error)
	}
}

func TestShellCommandConfig(t *testing.T) {
	cmd := newShellCommand(t.Context(), "true", t.TempDir(), nil, commandWaitDelay)
	if cmd.WaitDelay != 5*time.Second {
		t.Errorf("WaitDelay = %v, want 5s", cmd.WaitDelay)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Errorf("SysProcAttr.Setpgid is not set")
	}
	if cmd.Stdin != nil {
		t.Errorf("Stdin = %v, want nil", cmd.Stdin)
	}
	if cmd.Cancel == nil {
		t.Errorf("Cancel = nil, want a process-group kill")
	}
}

func TestShellCommandWaitDelay(t *testing.T) {
	dir := t.TempDir()
	cmd := newShellCommand(t.Context(), "sleep 0.5 & exit 0", dir, nil, 50*time.Millisecond)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("Run() error = %v, want exec.ErrWaitDelay", err)
	}
	if elapsed < 50*time.Millisecond {
		t.Errorf("Run() returned after %v, before the WaitDelay", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Run() took %v; WaitDelay did not unblock the inherited pipe", elapsed)
	}
}
