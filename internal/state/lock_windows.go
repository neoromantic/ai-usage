package state

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes the system's exclusive lock on f without waiting for it. It
// locks a byte far past the end, so the holder's process id stays readable.
func lockFile(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{OffsetHigh: 0x7fffffff})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrBusy
	}
	return err
}

// processAlive cannot tell on Windows whether another user's process exists,
// so an earlier release's lock there is held by its age alone.
func processAlive(int) bool { return true }
