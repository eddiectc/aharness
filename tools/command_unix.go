//go:build unix

package tools

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// newShellCommand builds a /bin/sh -c command in its own process group. The
// Cancel function kills the whole group so a shell cannot leave children
// behind on timeout or cancellation. WaitDelay bounds pipe draining after the
// process exits.
func newShellCommand(ctx context.Context, command, dir string, env []string, waitDelay time.Duration) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	cmd.WaitDelay = waitDelay
	return cmd
}
