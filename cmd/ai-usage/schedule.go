package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/schedule"
	"github.com/neoromantic/ai-usage/internal/state"
)

// disabledByHand is the schedule error for an entry the person paused.
const disabledByHand = "disabled by hand in the system scheduler; `ai-usage schedule install` turns it back on"

// ensureSchedule registers this binary and state folder unless the scheduler
// already runs them. An entry the person commented out or disabled stays so.
func ensureSchedule(ctx context.Context, d state.Dir, st *state.State, now time.Time) {
	last := st.Schedule
	st.Schedule.CheckedAt = now
	st.Schedule.Foreground = false
	exe, home, err := job(d)
	if err != nil {
		st.Schedule.Registered, st.Schedule.Error = false, err.Error()
		return
	}
	s := newScheduler()
	got, err := s.Lookup(ctx, exe, home)
	if err == nil && got == schedule.Disabled {
		st.Schedule.Registered, st.Schedule.Error = false, disabledByHand
		return
	}
	if err == nil && got != schedule.Active {
		err = s.Install(ctx, exe, home, os.Getenv("PATH"))
	}
	if err != nil && ctx.Err() != nil {
		// A lookup or install the run's stop cut short leaves the schedule
		// as the last run found it.
		st.Schedule = last
		return
	}
	st.Schedule.Registered = err == nil
	st.Schedule.Error = ""
	switch {
	case errors.Is(err, exec.ErrNotFound):
		// Containers and minimal systems often have no crontab.
		st.Schedule.Error = err.Error() + "; keep `ai-usage schedule run` running instead, such as beside a container's other services"
	case err != nil:
		st.Schedule.Error = err.Error()
	}
}

// job is what the scheduler should run: this binary, collecting into the
// absolute state folder, since the scheduler starts it in another directory.
func job(d state.Dir) (exe, home string, err error) {
	if exe, err = executable(); err != nil {
		return "", "", err
	}
	if home, err = filepath.Abs(string(d)); err != nil {
		return "", "", err
	}
	return exe, home, nil
}

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return fsutil.RealPath(exe), nil
}

func cmdSchedule(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return usageError("schedule takes install, remove, status, or run")
	}
	d, _, err := loadConfig()
	if err != nil {
		return err
	}
	s := newScheduler()
	exe, home, err := job(d)
	if err != nil {
		return err
	}
	if args[0] == "install" || args[0] == "remove" {
		// A run registers itself again unless the config says otherwise, so
		// the entry and the config change between runs, never under one.
		unlock, err := waitLock(ctx, d, stderr)
		if err != nil {
			return err
		}
		defer unlock()
	}
	switch args[0] {
	case "run":
		return scheduleRun(ctx, d, exe, home, stderr)
	case "install":
		if err := s.Install(ctx, exe, home, os.Getenv("PATH")); err != nil {
			return err
		}
		if _, err := d.EditConfig(func(c *state.Config) error { c.ScheduleOff = false; return nil }); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "registered %s to run every %s with state folder %s\n", exe, schedule.Interval, home)
	case "remove":
		if err := s.Remove(ctx); err != nil {
			return err
		}
		if _, err := d.EditConfig(func(c *state.Config) error { c.ScheduleOff = true; return nil }); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "removed from the system scheduler; later runs will not register again until `ai-usage schedule install`")
	case "status":
		return scheduleStatus(ctx, d, s, exe, home, stdout)
	default:
		return usageError("schedule takes install, remove, status, or run")
	}
	return nil
}

func scheduleStatus(ctx context.Context, d state.Dir, s schedule.Scheduler, exe, home string, stdout io.Writer) error {
	if d.Foreground() {
		fmt.Fprintf(stdout, "`ai-usage schedule run` collects every %s with state folder %s\n", schedule.Interval, home)
		return nil
	}
	got, err := s.Lookup(ctx, exe, home)
	if err != nil {
		return err
	}
	switch got {
	case schedule.Active:
		fmt.Fprintf(stdout, "registered: %s runs every %s with state folder %s\n", exe, schedule.Interval, home)
	case schedule.Other:
		fmt.Fprintln(stdout, "registered, but for a different binary path or state folder; `ai-usage schedule install` registers this one")
	case schedule.Disabled:
		fmt.Fprintln(stdout, disabledByHand)
	default:
		fmt.Fprintln(stdout, "not registered")
	}
	return nil
}

// scheduleRun collects now and then at every quarter hour, as the system
// schedulers do, until it is stopped. Each collection is a new process of the
// binary on disk, so a release one collection installs runs from the next.
// It holds the schedule lock throughout, which tells runs it is there.
func scheduleRun(ctx context.Context, d state.Dir, exe, home string, stderr io.Writer) error {
	unlock, err := d.ScheduleLock()
	if errors.Is(err, state.ErrBusy) {
		return errors.New("another `ai-usage schedule run` already collects into " + home)
	}
	if err != nil {
		return err
	}
	defer unlock()
	fmt.Fprintf(stderr, "ai-usage: collecting every %s with state folder %s\n", schedule.Interval, home)
	for {
		if err := collectOnce(ctx, exe, home, stderr); err != nil && ctx.Err() == nil {
			fmt.Fprintf(stderr, "ai-usage: %s\n", err)
		}
		now := clock()
		if !sleepCtx(ctx, now.Truncate(schedule.Interval).Add(schedule.Interval).Sub(now)) {
			return nil
		}
	}
}

// liveSchedule shows a schedule that `ai-usage schedule run` kept as gone
// once it no longer runs.
func liveSchedule(d state.Dir, st *state.State) *state.State {
	if st == nil || !st.Schedule.Foreground || d.Foreground() {
		return st
	}
	c := *st
	c.Schedule = state.Schedule{CheckedAt: st.Schedule.CheckedAt, Error: "`ai-usage schedule run` has stopped; start it again, or use `ai-usage schedule install` where there is a system scheduler"}
	return &c
}

// collectOnce and sleepCtx are replaced in tests.
var (
	collectOnce = func(ctx context.Context, exe, home string, stderr io.Writer) error {
		cmd := exec.CommandContext(ctx, exe, "collect", "--quiet", "--home", home)
		cmd.Stdout, cmd.Stderr = stderr, stderr
		// A stopped schedule lets the collection release its lock, and save
		// none of what the stop made fail; Windows cannot send the signal,
		// so it waits and then kills.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 30 * time.Second
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.Exited() {
			// The collection printed its own error.
			return nil
		}
		if exit != nil {
			// Killed, as by a container's memory limit: it said nothing.
			return fmt.Errorf("the collection was stopped: %s", exit)
		}
		return err
	}
	sleepCtx = func(ctx context.Context, d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
)
