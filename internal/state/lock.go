package state

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"
)

// ErrBusy is Lock's error while another run holds the lock.
var ErrBusy = errors.New("another ai-usage run is in progress")

// Lock takes the run lock. A collection holds it from start to end, since it
// reads and rewrites the state, samples, team cache, and binary. It is the
// system's lock on run.lock, which ends with the process that holds it
// however that process ends, so a run that was killed never leaves it behind.
func (d Dir) Lock() (func(), error) { return d.lock("run.lock") }

// ScheduleLock is held by `ai-usage schedule run` for as long as it runs, so
// other runs can tell that it schedules this folder. A check holds a shared
// lock on the same file for a moment, so a second is allowed for that to end.
func (d Dir) ScheduleLock() (func(), error) {
	return d.lockWait(context.Background(), "schedule.lock", checkWait, nil)
}

// Foreground reports whether `ai-usage schedule run` runs for this folder now.
// It takes a shared lock, which only the exclusive one `schedule run` holds
// keeps out, so it answers at once, and checks never wait for each other.
func (d Dir) Foreground() bool {
	f, err := os.OpenFile(d.Path("schedule.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	return errors.Is(sharedLockFile(f), ErrBusy)
}

// checkWait is how long ScheduleLock waits for a check's shared lock to end.
const checkWait = time.Second

func (d Dir) lock(name string) (func(), error) {
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return nil, err
	}
	// The file stays in place: removing it would let a later run lock a new
	// file while an earlier one still holds the old.
	f, err := os.OpenFile(d.Path(name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	// The holder's process id, for a person who wonders which run it is.
	if f.Truncate(0) == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() {
		_ = f.Truncate(0)
		_ = f.Close()
	}, nil
}

// lockPoll is how often LockWait tries the lock again.
var lockPoll = 250 * time.Millisecond

// LockWait takes the run lock like Lock, waiting up to wait for the run that
// holds it, such as a scheduled one, to finish. waiting is called once, when
// the wait starts.
func (d Dir) LockWait(ctx context.Context, wait time.Duration, waiting func()) (func(), error) {
	return d.lockWait(ctx, "run.lock", wait, waiting)
}

func (d Dir) lockWait(ctx context.Context, name string, wait time.Duration, waiting func()) (func(), error) {
	deadline := time.Now().Add(wait)
	for first := true; ; first = false {
		unlock, err := d.lock(name)
		if !errors.Is(err, ErrBusy) || !time.Now().Before(deadline) {
			return unlock, err
		}
		if first && waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			return nil, ErrBusy
		case <-time.After(min(lockPoll, time.Until(deadline))):
		}
	}
}
