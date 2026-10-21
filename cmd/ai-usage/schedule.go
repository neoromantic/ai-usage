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
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/state"
)

// disabledByHand is the schedule error for an entry the person paused.
const disabledByHand = "disabled by hand in the system scheduler; `ai-usage schedule install` turns it back on"

// refusedAtLastRun says what is wrong with a launch agent launchd refused to
// start at its last run, which any run a person starts repairs.
const refusedAtLastRun = "launchd refused to start the updated binary at its last run; `ai-usage status` has launchd start it now, or `ai-usage schedule install` loads it again"

// stillRefused is the schedule error while launchd refuses to start the
// launch agent after a repair.
const stillRefused = "launchd refuses to start the updated binary; `ai-usage schedule install` loads it again"

// staleAgent says launchd still runs the launch agent it loaded before its
// plist changed, as after a scheduled run of the installer's copy handed
// the schedule to Homebrew's.
const staleAgent = "launchd still runs the launch agent it loaded before, for another binary path or state folder; `ai-usage schedule install` loads this one"

// unloadedAgent says launchd has not loaded the launch agent whose plist
// runs this binary.
const unloadedAgent = "launchd has not loaded the launch agent, so nothing runs it; `ai-usage schedule install` loads it"

// ensureSchedule registers this binary and state folder unless the scheduler
// already runs them. An entry the person commented out or disabled stays so.
// Where Homebrew installed ai-usage and the installer did too, the scheduler
// runs only Homebrew's copy, and each says the installer's should go.
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
	switch {
	case err != nil:
	case got == schedule.Refused:
		// Starting the agent has launchd repair its pin. Loading it again
		// would leave it pinned to nothing until the next login, whose
		// first run launchd could refuse once more.
		err = s.Repair(ctx, false)
	case got != schedule.Active:
		err = s.Install(ctx, exe, home, os.Getenv("PATH"))
		if err == nil && s.InAgent {
			// The run the agent started wrote the plist, but cannot load it
			// without stopping itself, so launchd may run what it loaded
			// before.
			got, err = s.Lookup(ctx, exe, home)
		}
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
	case errors.Is(err, schedule.ErrRefused):
		st.Schedule.Error = stillRefused
	case errors.Is(err, exec.ErrNotFound):
		// Containers and minimal systems often have no crontab.
		st.Schedule.Error = err.Error() + "; keep `ai-usage schedule run` running instead, such as beside a container's other services"
	case err != nil:
		st.Schedule.Error = err.Error()
	case s.InAgent && got == schedule.Stale:
		// A run a person starts loads the plist; until then, or the next
		// login, launchd runs what it loaded before. Removing a copy of
		// ai-usage, as yieldToBrew would say, could leave it nothing to run.
		st.Schedule.Registered, st.Schedule.Error = false, staleAgent
	case brewCopy() != "":
		st.Schedule.Error = yieldToBrew(brewCopy())
	case brewPrefix() != "":
		switch p := scriptCopy(); {
		case p == filepath.Join(brewPrefix(), "bin", "ai-usage"):
			st.Schedule.Error = "another ai-usage at " + p + " keeps Homebrew from linking this one; replace it: " + brewLink
		case p != "":
			st.Schedule.Error = "another ai-usage at " + p + " also runs; remove it: rm " + p
		}
	}
}

// yieldToBrew says that the copy of ai-usage Homebrew installed under prefix
// runs the schedule, and how this copy, the installer's, goes. One in
// Homebrew's own bin folder keeps brew from linking its copy there.
func yieldToBrew(prefix string) string {
	bin := filepath.Join(prefix, "bin", "ai-usage")
	self, _ := executable()
	if self == bin {
		return "Homebrew's ai-usage runs the schedule, but this copy at " + bin + " keeps brew from linking it; replace this copy: " + brewLink
	}
	return "Homebrew's ai-usage at " + bin + " runs the schedule; remove this copy: rm " + self
}

// repairs says whether this run may have launchd start the macOS launch
// agent: not on other systems, not in the run the agent started, and not in
// a run told to leave the scheduler alone.
func repairs(s schedule.Scheduler) bool {
	return s.GOOS == "darwin" && !s.InAgent && os.Getenv("AI_USAGE_NO_SCHEDULE") == ""
}

// repairAgent has launchd start the macOS launch agent now when it runs this
// binary with this state folder and launchd refused to start it at its last
// run, or replaced says an update has just replaced the binary, which
// launchd would refuse at the next run. A repair that fails is the
// schedule's error. The run the agent started leaves it to launchd, which
// starts the new binary at the run after the one it refuses.
//
// It also says when launchd does not run the agent the plist defines: when
// it still runs an earlier definition, or a schedule the last run found
// registered, or found launchd running an earlier definition, is not loaded
// now. Both come back to the schedule once the agent is loaded again, as by
// `ai-usage schedule install`.
func repairAgent(ctx context.Context, d state.Dir, st *state.State, replaced bool) {
	s := newScheduler()
	if !repairs(s) {
		return
	}
	exe, home, err := job(d)
	if err != nil {
		return
	}
	got, err := s.Lookup(ctx, exe, home)
	if err != nil {
		return
	}
	switch sch := &st.Schedule; {
	case sch.Foreground:
		// `ai-usage schedule run` is the scheduler.
	case got == schedule.Stale:
		sch.Registered, sch.Error = false, staleAgent
		return
	case got == schedule.Unloaded && (sch.Registered || sch.Error == staleAgent):
		sch.Registered, sch.Error = false, unloadedAgent
		return
	case got == schedule.Active && (sch.Error == staleAgent || sch.Error == unloadedAgent):
		sch.Registered, sch.Error = true, ""
	}
	if got != schedule.Refused && (!replaced || got != schedule.Active) {
		return
	}
	err = s.Repair(ctx, replaced)
	switch {
	case err == nil:
		st.Schedule.Registered, st.Schedule.Error = true, ""
	case ctx.Err() != nil:
	case errors.Is(err, schedule.ErrRefused):
		st.Schedule.Registered, st.Schedule.Error = false, stillRefused
	default:
		st.Schedule.Registered, st.Schedule.Error = false, "starting the launch agent: "+err.Error()
	}
}

// healSchedule repairs a macOS launch agent launchd refused to start at its
// last run, so a report a person or the menu bar app reads brings collection
// back at once, not at the next run, and says when launchd runs an earlier
// definition of the agent or none, which the last run may not have seen. A
// copy Homebrew installed whose last collection was another release was
// upgraded by brew since, which launchd would refuse at the next run, so it
// has launchd start the agent as after a self-update; that run collects, so
// this happens once an upgrade. The run launchd starts takes the run lock
// itself.
func healSchedule(ctx context.Context, d state.Dir, st *state.State) {
	if !selfupdate.Dev(version) {
		repairAgent(ctx, d, st, brewPrefix() != "" && st.RanVersion != "" && st.RanVersion != version)
	}
}

// job is what the scheduler should run: this binary, or the copy Homebrew
// installed beside it, collecting into the absolute state folder, since the
// scheduler starts it in another directory.
func job(d state.Dir) (exe, home string, err error) {
	if exe, err = executable(); err != nil {
		return "", "", err
	}
	if p := brewCopy(); p != "" {
		exe = brewOpt(p)
	}
	if home, err = filepath.Abs(string(d)); err != nil {
		return "", "", err
	}
	return exe, home, nil
}

// executable is this binary, by the path Homebrew keeps for it across
// upgrades when Homebrew installed it.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return fsutil.HomebrewStable(fsutil.RealPath(exe)), nil
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
	case schedule.Refused:
		fmt.Fprintln(stdout, "registered, but "+refusedAtLastRun)
	case schedule.Stale:
		fmt.Fprintln(stdout, "registered, but "+staleAgent)
	case schedule.Unloaded:
		fmt.Fprintln(stdout, "registered, but "+unloadedAgent)
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
