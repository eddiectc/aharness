//go:build unix

package session

import (
	"errors"
	"os"
	"syscall"
)

// errWouldLock reports that a non-blocking flock could not be acquired.
var errWouldLock = errors.New("session lock would block")

// lockFile takes a non-blocking shared or exclusive advisory flock on the
// session log itself. A busy session maps to errWouldLock.
func lockFile(file *os.File, exclusive bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(file.Fd()), how|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return errWouldLock
		}
		return err
	}
	return nil
}

// unlockFile releases the advisory session lock.
func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
