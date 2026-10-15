package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/state"
)

// appCommand runs a macOS command for the menu bar app and returns what it
// printed. Tests replace it.
var appCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// appMacOS is the oldest macOS the menu bar app runs on.
const appMacOS = 14

// macOS is this Mac's version, such as 15.4, and its major number; 0 when
// it cannot tell.
func macOS(ctx context.Context) (string, int) {
	out, err := appCommand(ctx, "sw_vers", "-productVersion")
	v := strings.TrimSpace(string(out))
	major, _, _ := strings.Cut(v, ".")
	n, aerr := strconv.Atoi(major)
	if err != nil || aerr != nil {
		return v, 0
	}
	return v, n
}

// appMissing says the first-run guide should offer `ai-usage app install`:
// on a Mac that runs the menu bar app, for a person, where it is not
// installed.
func appMissing(ctx context.Context) bool {
	app := menuBarApp()
	if app == "" || os.Geteuid() == 0 || selfupdate.Dev(version) {
		return false
	}
	if _, err := os.Stat(app); err == nil {
		return false
	}
	_, major := macOS(ctx)
	return major >= appMacOS
}

// cmdApp installs the menu bar app of this release, as install.sh does,
// whatever installed ai-usage. Runs keep it at this binary's release from
// then on.
func cmdApp(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 || args[0] != "install" {
		return usageError("app takes install")
	}
	switch {
	case runtime.GOOS != "darwin":
		return errors.New("the menu bar app runs on macOS only")
	case os.Geteuid() == 0:
		return errors.New("the menu bar app is per-user; run ai-usage app install as the user who uses it")
	case selfupdate.Dev(version):
		return errors.New("this is a development build (" + version + "); install a release to add the menu bar app")
	}
	if v, major := macOS(ctx); major < appMacOS {
		return fmt.Errorf("the menu bar app needs macOS %d or later; this Mac has %s", appMacOS, v)
	}
	d, err := state.DefaultDir()
	if err != nil {
		return err
	}
	st, err := d.LoadState()
	unread := err != nil || st.LastRunAt.IsZero()
	u := newUpdater()
	installed, err := installApp(ctx, d, u, stderr)
	if err != nil {
		return err
	}
	if installed {
		fmt.Fprintf(stdout, "installed the menu bar app of %s to %s\n", version, u.App)
		quitApp(ctx)
	} else {
		fmt.Fprintf(stdout, "the menu bar app of %s is installed\n", version)
	}
	// Over SSH with no one logged in at the screen, there is nowhere to
	// open it.
	if _, err := appCommand(ctx, "launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())); err != nil {
		fmt.Fprintf(stdout, "log in at the screen and open %s once; from then on it starts at login\n", u.App)
	} else if _, err := appCommand(ctx, "open", u.App); err != nil {
		fmt.Fprintf(stdout, "could not open %s; open it in Finder\n", u.App)
	} else {
		fmt.Fprintln(stdout, "the app is in the menu bar; it starts at login and updates with ai-usage")
	}
	if unread {
		fmt.Fprintln(stdout, "run ai-usage first, so the app finds a report")
	}
	return nil
}

// installApp puts the app of this release in place under the run lock, which
// keeps a scheduled run from updating it at the same time, when there is a
// state folder for runs to lock.
func installApp(ctx context.Context, d state.Dir, u *selfupdate.Updater, stderr io.Writer) (bool, error) {
	if _, err := os.Stat(string(d)); err == nil {
		unlock, err := waitLock(ctx, d, stderr)
		if err != nil {
			return false, err
		}
		defer unlock()
	}
	return u.InstallApp(ctx, version)
}

// quitApp quits a copy of the app that runs, which keeps its old code, and
// waits up to 10 seconds for it to go, since the new one quits when it finds
// another running.
func quitApp(ctx context.Context) {
	uid := strconv.Itoa(os.Getuid())
	if _, err := appCommand(ctx, "pkill", "-x", "-U", uid, "AIUsageBar"); err != nil {
		return
	}
	for range 10 {
		if _, err := appCommand(ctx, "pgrep", "-x", "-U", uid, "AIUsageBar"); err != nil || !sleepCtx(ctx, time.Second) {
			return
		}
	}
}
