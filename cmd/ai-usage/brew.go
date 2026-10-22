package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/state"
)

// brewTimeout bounds the release lookup and what brew does for `ai-usage
// update`, and afterBrewTimeout the binary brew installed finishing it, which
// waits up to lockWait for the run lock and then checks. With the half minute
// each is given to stop, both stay under the 15 minutes the menu bar app
// waits.
const (
	brewTimeout      = 8 * time.Minute
	afterBrewTimeout = 4 * time.Minute
)

// brewLink is what replaces a copy of ai-usage the installer put in
// Homebrew's bin folder, which keeps brew from linking its own, with
// Homebrew's.
const brewLink = "brew link --overwrite " + fsutil.HomebrewTap

// brewOpt is the path Homebrew keeps under prefix for the binary of the
// latest ai-usage keg.
func brewOpt(prefix string) string {
	return filepath.Join(prefix, "opt", "ai-usage", "bin", "ai-usage")
}

// brewCommand is what upgrades this copy, for the report: none when Homebrew
// did not install it, or for root, since Homebrew installs are per-user.
func brewCommand() string {
	if brewPrefix() == "" || os.Geteuid() == 0 {
		return ""
	}
	return fsutil.HomebrewUpgrade
}

// brewCopy is the prefix of a copy of ai-usage this user installed with
// Homebrew, when this copy is not one: that copy runs the schedule. Homebrew
// installs are per-user, so another user's, in a prefix several share, runs
// only that user's schedule.
func brewCopy() string {
	if brewPrefix() != "" {
		return ""
	}
	for _, p := range brewPrefixes() {
		if p == "" {
			continue
		}
		opt := filepath.Join(p, "opt", "ai-usage")
		if fi, err := os.Lstat(opt); err == nil && fi.Mode()&os.ModeSymlink != 0 && fsutil.Mine(opt) {
			return p
		}
	}
	return ""
}

// scriptCopy is a copy of ai-usage the installer put in the person's own
// folders or, before v0.8.0, in Homebrew's bin folder, beside this one
// Homebrew installed, or "". A link there, such as Homebrew's own, is not
// one.
func scriptCopy() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	paths := []string{filepath.Join(home, ".local", "bin", "ai-usage"), filepath.Join(home, "bin", "ai-usage")}
	if p := brewPrefix(); p != "" {
		paths = append(paths, filepath.Join(p, "bin", "ai-usage"))
	}
	for _, p := range paths {
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// brewUpdate is `ai-usage update` for a copy Homebrew installed under
// prefix: brew upgrades it, and the binary brew installed finishes the
// update, since brew removes the keg this process runs from. No run lock is
// held while brew runs: brew has its own, and the process that would hold
// ours is the one brew replaces.
func brewUpdate(ctx context.Context, prefix string, stdout, stderr io.Writer) error {
	opt := brewOpt(prefix)
	bctx, cancel := context.WithTimeout(ctx, brewTimeout)
	defer cancel()
	// A lookup that fails leaves brew to say what is newest.
	latest, _ := newUpdater().Latest(bctx)
	got, err := brewUpgrade(bctx, prefix, opt, stderr)
	if err != nil {
		return err
	}
	if selfupdate.Newer(latest, got) {
		// brew read the tap before the release reached it, or was told not
		// to read it, as HOMEBREW_NO_AUTO_UPDATE does.
		if err = brew(bctx, prefix, stderr, "update", "--quiet"); err == nil {
			var again string
			if again, err = brewUpgrade(bctx, prefix, opt, stderr); err == nil {
				got = again
			}
		}
	}
	// A release brew installed is finished even when it is not the latest,
	// or the second try failed.
	if got != version {
		if ferr := finishBrew(ctx, opt, stdout, stderr); ferr != nil {
			return ferr
		}
	}
	switch {
	case err != nil:
		return err
	case selfupdate.Newer(latest, got):
		fmt.Fprintf(stdout, "Homebrew's formula is at %s; GitHub has %s, which reaches Homebrew soon\n", got, latest)
		return exitCode(1)
	case got == version:
		// Someone may have run brew upgrade themselves, so the menu bar app
		// can still be at an earlier release.
		res, err := checkHere(ctx, stderr, false)
		fmt.Fprintf(stdout, "up to date (%s)\n", version)
		if res.AppDownloaded && err == nil {
			fmt.Fprintf(stdout, "installed the menu bar app of %s\n", version)
		}
		return err
	}
	return nil
}

// finishBrew has the binary brew installed at opt finish the update.
func finishBrew(ctx context.Context, opt string, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, afterBrewTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, opt, "update", "--after-brew", version)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// Stopped, it still saves what it did and lets go of the run lock.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.Exited() {
		// It said why.
		return exitCode(exit.ExitCode())
	}
	return err
}

// brewUpgrade has brew upgrade ai-usage, and returns the release of the
// binary at opt then.
func brewUpgrade(ctx context.Context, prefix, opt string, stderr io.Writer) (string, error) {
	if err := brew(ctx, prefix, stderr, "upgrade", fsutil.HomebrewTap); err != nil {
		return "", err
	}
	v, err := selfupdate.Reports(ctx, opt)
	if err != nil {
		return "", fmt.Errorf("%s does not run after brew upgrade: %w", opt, err)
	}
	return v, nil
}

// brew runs the brew of prefix, never one found on PATH, with what it
// prints on stderr. Its error is the last line brew wrote to stderr.
func brew(ctx context.Context, prefix string, stderr io.Writer, args ...string) error {
	var mu sync.Mutex
	last := &lastLine{}
	cmd := exec.CommandContext(ctx, filepath.Join(prefix, "bin", "brew"), args...)
	cmd.Stdout = lockedWriter{&mu, stderr}
	cmd.Stderr = lockedWriter{&mu, io.MultiWriter(stderr, last)}
	// brew is stopped as a person would stop it, so it can let go of its
	// locks.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	err := cmd.Run()
	switch {
	case err == nil:
		return nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("brew %s failed: it took over %d minutes", args[0], int(brewTimeout.Minutes()))
	case last.String() != "":
		return fmt.Errorf("brew %s failed: %s", args[0], last)
	}
	return fmt.Errorf("brew %s failed: %w", args[0], err)
}

// afterBrew finishes `ai-usage update` in the binary brew just installed: it
// brings the menu bar app to this release, notes the check, and has launchd
// start the launch agent, whose binary brew replaced.
func afterBrew(ctx context.Context, stdout, stderr io.Writer) error {
	res, err := checkHere(ctx, stderr, true)
	fmt.Fprintf(stdout, "installed %s with Homebrew; the next run uses it\n", version)
	if res.AppDownloaded && err == nil {
		fmt.Fprintf(stdout, "installed the menu bar app of %s\n", version)
	}
	return err
}

// checkHere checks for a release under the run lock, which brings the menu
// bar app to this binary's release, and notes the check. repair has launchd
// start the launch agent, whose binary brew replaced.
func checkHere(ctx context.Context, stderr io.Writer, repair bool) (selfupdate.Result, error) {
	d, err := state.DefaultDir()
	if err != nil {
		return selfupdate.Result{}, err
	}
	unlock, err := waitLock(ctx, d, stderr)
	if err != nil {
		return selfupdate.Result{}, err
	}
	defer unlock()
	st, err := d.LoadState()
	if err != nil {
		return selfupdate.Result{}, err
	}
	res, err := newUpdater().Check(ctx)
	noteUpdate(st, clock().UTC(), res, err)
	if repair {
		repairAgent(ctx, d, st, true)
	}
	if serr := d.SaveState(st); serr != nil && err == nil && !errors.Is(serr, state.ErrNewer) {
		err = serr
	}
	return res, err
}

// lastLine keeps the last line written to it that says something, without
// brew's "Error: ".
type lastLine struct {
	part []byte
	line string
}

func (l *lastLine) Write(p []byte) (int, error) {
	l.part = append(l.part, p...)
	for {
		i := bytes.IndexByte(l.part, '\n')
		if i < 0 {
			break
		}
		l.keep(string(l.part[:i]))
		l.part = l.part[i+1:]
	}
	return len(p), nil
}

func (l *lastLine) keep(s string) {
	if s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "Error:")); s != "" {
		l.line = s
	}
}

func (l *lastLine) String() string {
	l.keep(string(l.part))
	l.part = nil
	return l.line
}

// lockedWriter writes to w under mu, so two pipes of one command can share w.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
