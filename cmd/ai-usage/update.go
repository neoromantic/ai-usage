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
	brew := brewPrefix() != ""
	if brew {
		// Only a copy that replaces itself downloads a release that can
		// fail to install.
		st.Update.Failed = nil
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
	if brew && st.RanVersion != "" && st.RanVersion != version && !selfupdate.Dev(version) {
		// brew upgraded the binary the launch agent runs since the last
		// run, as a brew upgrade a person runs does.
		repairAgent(ctx, d, st, true)
	}
	st.RanVersion = version
	updateRepairing(ctx, d, st, now, scheduled)
}

// updateRepairing is updateIfDue, which reports whether it checked, followed
// by a repair of the macOS launch agent once a release it installed replaced
// the binary the agent runs. A copy beside one Homebrew installed leaves
// releases to that one, so the two do not take turns with the scheduler's
// entry.
func updateRepairing(ctx context.Context, d state.Dir, st *state.State, now time.Time, scheduled bool) bool {
	if brewCopy() != "" {
		return false
	}
	staged := st.Update.Installed
	checked := updateIfDue(ctx, st, now, scheduled)
	if st.Update.Installed != "" && st.Update.Installed != staged {
		repairAgent(ctx, d, st, true)
	}
	return checked
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

// errBrewRoot is why root does not update a copy Homebrew installed.
//
//lint:ignore ST1005 Homebrew is a name, and the error is a whole sentence the person reads
var errBrewRoot = errors.New("Homebrew installs are per-user; run ai-usage update as the user who installed it")

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
			st.Update.AppFailed = &state.FailedRelease{Tag: res.AppTag, At: now, Error: err.Error()}
		}
	default:
		st.Update.Failed, st.Update.AppFailed = nil, nil
	}
	if res.Installed {
		st.Update.Installed = res.Latest
	}
}

func cmdUpdate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flags("update")
	// The binary brew installed finishes an update with this; the help
	// does not list it.
	afterBrewed := fs.String("after-brew", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if selfupdate.Dev(version) {
		return errors.New("this is a development build (" + version + "); install a release to self-update")
	}
	if p := brewPrefix(); p != "" {
		switch {
		case os.Geteuid() == 0:
			return errBrewRoot
		case *afterBrewed != "":
			return afterBrew(ctx, stdout, stderr)
		}
		return brewUpdate(ctx, p, stdout, stderr)
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
	if res.Installed {
		// The menu bar app's Update button comes here too.
		repairAgent(ctx, d, st, true)
	}
	// A state a newer release saved is left as it is, and installing the
	// latest release is what that refusal asks for.
	if serr := d.SaveState(st); serr != nil && err == nil && !(res.Installed && errors.Is(serr, state.ErrNewer)) {
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
