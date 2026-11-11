//go:build unix

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// configureProcess makes the child a process-group leader and kills the whole
// group when the context ends. WaitDelay unblocks inherited output pipes.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
}
