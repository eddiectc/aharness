//go:build !unix

package main

import "os/exec"

// configureProcess is a no-op on platforms without POSIX process groups.
func configureProcess(cmd *exec.Cmd) {}
