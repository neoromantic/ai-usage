package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/state"
)

// updateFloor is the least time between release checks. Scheduled runs are
// 15 minutes apart, so each of them checks, and a release reaches a device
// within about a quarter of an hour. Runs a person starts in between, such as
// the view's r, do not ask GitHub again.
const updateFloor = 10 * time.Minute

// updateTimeout bounds a release check in a run. A slow link needs minutes
// for the download. Finding the latest release has a shorter limit of its own.
const updateTimeout = 7 * time.Minute

// retryFailed is how long a release that was downloaded and did not install,
// such as one that does not start here, is not downloaded again. Runs still
// look up the latest release, so a newer one is installed at once.
const retryFailed = 6 * time.Hour

// menuBarApp is where install.sh puts the macOS menu bar app, which
// self-update keeps at the binary's release; "" on other systems.
func menuBarApp() string {
	home, err := os.UserHomeDir()
	if runtime.GOOS != "darwin" || err != nil {
		return ""
	}
	return filepath.Join(home, "Applications", selfupdate.AppBundle)
}

// housekeeping registers with the scheduler and applies self-update, both
// inside the run lock. A development build does neither on its own. scheduled
// says the scheduler started the run, with collect --quiet.
func housekeeping(ctx context.Context, d state.Dir, cfg *state.Config, st *state.State, scheduled bool) {
	now := clock().UTC()
	// The release staged by an earlier run is this binary now, or was replaced.
	if st.Update.Installed != "" && !selfupdate.Newer(st.Update.Installed, version) {
		st.Update.Installed = ""
	}
	switch {
	case d.Foreground():
		// `ai-usage schedule run` is the scheduler, so the system's is not
		// needed; a run never registers there while it runs.
		st.Schedule = state.Schedule{Registered: true, CheckedAt: now, Foreground: true}
	case !selfupdate.Dev(version) && !cfg.ScheduleOff && os.Getenv("AI_USAGE_NO_SCHEDULE") == "":
		ensureSchedule(ctx, d, st, now)
	case cfg.ScheduleOff:
		st.Schedule.Registered, st.Schedule.Foreground = false, false
		st.Schedule.Error = "removed by `ai-usage schedule remove`"
	}
	updateIfDue(ctx, st, now, scheduled)
}

// updateIfDue checks for a release on a release build, unless the last check
// was less than updateFloor ago, and reports whether it did. A scheduled run
// tries a failed check again, so a failure that passes shows for one run at
// most. A run a person starts, such as the view's r, does not, so it never
// asks GitHub twice within updateFloor. A check time in the future, left by a
// clock that once ran ahead, counts as due: otherwise updates would stop
// until the clock caught up with it.
func updateIfDue(ctx context.Context, st *state.State, now time.Time, scheduled bool) bool {
	if selfupdate.Dev(version) {
		return false
	}
	if since := now.Sub(st.Update.CheckedAt); since >= 0 && since < updateFloor && (st.Update.Error == "" || !scheduled) {
		return false
	}
	u := newUpdater()
	// A release that was downloaded and did not install would most likely
	// fail the same way at every run, and so would its menu bar app.
	skip := func(f *state.FailedRelease) string {
		if f != nil {
			if since := now.Sub(f.At); since >= 0 && since < retryFailed {
				return f.Tag
			}
		}
		return ""
	}
	u.Skip, u.SkipApp = skip(st.Update.Failed), skip(st.Update.AppFailed)
	uctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	res, err := u.Check(uctx)
	if err != nil && ctx.Err() != nil {
		// A check the run's stop cut short is left for the next run.
		return false
	}
	noteUpdate(st, now, res, err)
	return true
}

func noteUpdate(st *state.State, now time.Time, res selfupdate.Result, err error) {
	st.Update.CheckedAt = now
	if res.Latest != "" {
		// A failed check keeps the last release it did see.
		st.Update.Latest = res.Latest
	}
	st.Update.Error = ""
	switch {
	case errors.Is(err, selfupdate.ErrSkipped) && st.Update.Failed != nil:
		// The latest release is still the one that failed, for the same
		// reason as far as anyone knows.
		st.Update.Error = st.Update.Failed.Error
	case errors.Is(err, selfupdate.ErrAppSkipped) && st.Update.AppFailed != nil:
		st.Update.Error = st.Update.AppFailed.Error
	case err != nil:
		st.Update.Error = err.Error()
		// A binary that installed failed nothing: the error is the menu
		// bar app's.
		if res.Downloaded && !res.Installed {
			st.Update.Failed = &state.FailedRelease{Tag: res.Latest, At: now, Error: err.Error()}
		}
		if res.AppDownloaded {
			st.Update.AppFailed = &state.FailedRelease{Tag: res.Latest, At: now, Error: err.Error()}
		}
	default:
		st.Update.Failed, st.Update.AppFailed = nil, nil
	}
	if res.Installed {
		st.Update.Installed = res.Latest
	}
}

func cmdUpdate(ctx context.Context, stdout, stderr io.Writer) error {
	if selfupdate.Dev(version) {
		return errors.New("this is a development build (" + version + "); install a release to self-update")
	}
	d, err := state.DefaultDir()
	if err != nil {
		return err
	}
	// The lock keeps a scheduled run from replacing the binary at the same time.
	unlock, err := waitLock(ctx, d, stderr)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := d.LoadState()
	if err != nil {
		return err
	}
	res, err := newUpdater().Check(ctx)
	noteUpdate(st, clock().UTC(), res, err)
	if serr := d.SaveState(st); serr != nil && err == nil {
		err = serr
	}
	// The menu bar app's error comes with the binary at the latest release,
	// which is said first.
	if err != nil && !errors.Is(err, selfupdate.ErrApp) {
		return err
	}
	if res.Installed {
		fmt.Fprintf(stdout, "installed %s; the next run uses it\n", res.Latest)
	} else {
		fmt.Fprintf(stdout, "up to date (%s; latest %s)\n", version, res.Latest)
	}
	if res.AppDownloaded && err == nil {
		fmt.Fprintf(stdout, "installed the menu bar app of %s\n", res.Latest)
	}
	return err
}
