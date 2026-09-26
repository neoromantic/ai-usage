//go:build !windows

package state

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes the system's exclusive lock on f without waiting for it.
func lockFile(f *os.File) error { return flock(f, syscall.LOCK_EX) }

// sharedLockFile takes a shared lock on f without waiting for it. Only an
// exclusive holder keeps it out.
func sharedLockFile(f *os.File) error { return flock(f, syscall.LOCK_SH) }

func flock(f *os.File, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return ErrBusy
		}
		return err
	}
}
