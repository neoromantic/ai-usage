//go:build !windows

package state

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes the system's exclusive lock on f without waiting for it.
func lockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return ErrBusy
		}
		return err
	}
}

// processAlive reports whether a process with this id exists. A process of
// another user answers with EPERM, and still exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
