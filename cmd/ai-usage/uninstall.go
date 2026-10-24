package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/relay"
)

// appAgent names the launch agent the menu bar app writes to open at login
// when macOS does not let it register as a login item, and the domain of
// its settings.
const appAgent = "io.github.neoromantic.ai-usage.bar"

// selfExe is os.Executable. Tests replace it, so uninstall removes a copy,
// not the test binary.
var selfExe = os.Executable

// cmdUninstall takes ai-usage off this device, without a question, so a
// front end can run it: it removes the schedule, the menu bar app, and this
// binary, unless Homebrew installed it, and keeps the state folder, which
// holds the team key, unless purge. With forget, or purge, it first asks
// the relay to drop this device from its team. It does every step it can,
// and fails at the end when one failed.
func cmdUninstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flags("uninstall")
	forget := fs.Bool("forget", false, "")
	purge := fs.Bool("purge", false, "")
	if err := parse(fs, args); err != nil {
		return err
	}
	d, err := state.DefaultDir()
	if err != nil {
		return err
	}
	var failed []string
	fail := func(what string, err error) {
		failed = append(failed, what+": "+err.Error())
	}
	_, statErr := os.Stat(string(d))
	have := statErr == nil
	unlock := func() {}
	if have {
		// A scheduled run finishes first, and none starts meanwhile.
		if unlock, err = waitLock(ctx, d, stderr); err != nil {
			return err
		}
	}
	defer func() { unlock() }()

	s := newScheduler()
	if err := s.Remove(ctx); err != nil {
		fail("schedule", err)
	} else {
		fmt.Fprintf(stdout, "removed the schedule from %s\n", s.Name())
	}
	if app, err := removeApp(ctx); err != nil {
		fail("menu bar app", err)
	} else if app != "" {
		fmt.Fprintf(stdout, "removed the menu bar app at %s\n", app)
	}
	if have && (*forget || *purge) {
		if fp, err := leaveTeam(ctx, d); err != nil {
			fail("relay", err)
		} else if fp != "" {
			fmt.Fprintf(stdout, "removed this device from team %s on the relay\n", fp)
		}
	}
	removeBinary(stdout, fail)
	switch {
	case *purge && have:
		unlock()
		unlock = func() {}
		if err := os.RemoveAll(string(d)); err != nil {
			fail("state folder", err)
		} else {
			fmt.Fprintf(stdout, "removed the state folder %s\n", d)
		}
	case have:
		fmt.Fprintf(stdout, "kept the state folder %s, with the team key and the history; `ai-usage uninstall --purge` removes it\n", d)
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// removeApp quits and removes the macOS menu bar app, the launch agent it
// may have written, and its settings, and returns where the app was; ""
// when there was none, and on other systems.
func removeApp(ctx context.Context) (string, error) {
	app := menuBarApp()
	home, err := os.UserHomeDir()
	if app == "" || err != nil {
		return "", nil
	}
	agent := filepath.Join(home, "Library", "LaunchAgents", appAgent+".plist")
	_, appErr := os.Stat(app)
	_, agentErr := os.Stat(agent)
	if appErr != nil && agentErr != nil {
		return "", nil
	}
	quitApp(ctx)
	// Neither is an error: the agent is loaded only until the next login,
	// and the settings may be gone.
	_, _ = appCommand(ctx, "launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+appAgent)
	if err := os.Remove(agent); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.RemoveAll(app); err != nil {
		return "", err
	}
	_, _ = appCommand(ctx, "defaults", "delete", appAgent)
	return app, nil
}

// leaveTeam asks the relay to drop this device from its team, as joining
// another team does, and returns the team's fingerprint; "" when the device
// has no team key or no relay.
func leaveTeam(ctx context.Context, d state.Dir) (string, error) {
	key, err := team.Load(d.KeyFile())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	cfg, err := d.LoadConfig()
	if err != nil {
		return "", err
	}
	endpoint := relayURL(cfg)
	if endpoint == "" {
		return "", nil
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := (&relay.Client{BaseURL: endpoint, Key: key}).Remove(rctx, cfg.Device); err != nil {
		return "", fmt.Errorf("%w; a teammate can run `ai-usage team forget-device %s`", err, cfg.Device)
	}
	return key.Fingerprint(), nil
}

// removeBinary removes this binary when the installer put it in place, and
// the installer's copy beside one Homebrew installed. Homebrew's own copy
// is Homebrew's to remove, and a build from source is left where it is.
func removeBinary(stdout io.Writer, fail func(string, error)) {
	exe, err := selfExe()
	if err != nil {
		fail("binary", err)
		return
	}
	exe = fsutil.RealPath(exe)
	brew := "brew uninstall " + fsutil.HomebrewTap
	rm := func(p string) {
		switch err := os.Remove(p); {
		case errors.Is(err, os.ErrNotExist):
			return
		case err != nil:
			fail("binary", fmt.Errorf("%w; remove it by hand", err))
			return
		}
		fmt.Fprintf(stdout, "removed %s\n", p)
	}
	switch {
	case selfupdate.Dev(version):
		fmt.Fprintf(stdout, "left this development build at %s\n", exe)
	case runtime.GOOS == "windows":
		fmt.Fprintf(stdout, "Windows does not delete a program while it runs; remove %s\n", filepath.Dir(exe))
	case brewPrefix() != "":
		if p := scriptCopy(); p != "" {
			rm(p)
		}
		fmt.Fprintf(stdout, "Homebrew installed %s; remove it with %s\n", brewOpt(brewPrefix()), brew)
	default:
		rm(exe)
		if p := brewCopy(); p != "" {
			fmt.Fprintf(stdout, "Homebrew installed %s too; remove it with %s\n", brewOpt(p), brew)
		}
	}
}
