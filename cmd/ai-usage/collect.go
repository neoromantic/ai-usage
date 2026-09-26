package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/tui"
	"github.com/neoromantic/ai-usage/relay"
)

func cmdCollect(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flags("collect")
	jsonOut := fs.Bool("json", false, "")
	quiet := fs.Bool("quiet", false, "")
	offline := fs.Bool("offline", false, "")
	home := fs.String("home", "", "")
	disp := displayFlags(fs, true)
	if err := disp.parse(fs, args); err != nil {
		return err
	}
	d, err := state.DefaultDir()
	if *home != "" {
		// The scheduler names the folder, since it does not see AI_USAGE_HOME.
		d, err = state.Dir(*home), nil
	}
	if err != nil {
		return err
	}
	guide := false
	res, err := collection{
		d:         d,
		offline:   *offline,
		scheduled: *quiet,
		waiting: func() {
			fmt.Fprintln(stderr, "ai-usage: another run, such as the scheduled one, is collecting now; waiting for its result")
		},
		locked: func(st *state.State) {
			// The guide waits for a report a person reads, even when the
			// scheduler collected first.
			if st.GuideDue && !*quiet && !*jsonOut {
				st.GuideDue, guide = false, true
			}
		},
	}.run(ctx)
	if err != nil {
		return err
	}
	if *quiet {
		return nil
	}
	// A run that waited shows the other run's result and has no After of
	// its own, so it takes the guide here.
	if res.Waited && res.State.GuideDue && !*jsonOut {
		guide = takeGuide(d)
	}
	if disp.interactive(stdout, *jsonOut, guide) {
		return tui.Run(ctx, viewConfig(d, res, disp, *offline, stdout), os.Stdin, stdout)
	}
	return printReport(stdout, reportAt(d, res, clock().UTC()), *jsonOut, guide, disp)
}

// takeGuide clears the guide's due mark under the run lock and reports
// whether this run should print it. While another run holds the lock, the
// guide is left for a later report.
func takeGuide(d state.Dir) bool {
	unlock, err := d.Lock()
	if err != nil {
		return false
	}
	defer unlock()
	st, err := d.LoadState()
	if err != nil || !st.GuideDue {
		return false
	}
	st.GuideDue = false
	return d.SaveState(st) == nil
}

type panicError struct {
	value any
	stack []byte
}

func (e *panicError) Error() string {
	return fmt.Sprintf("collection stopped by a bug: %v\n%s", e.value, e.stack)
}

// collection is a collection and its housekeeping, as `collect` and `r` in
// the interactive view run it. scheduled says the scheduler started it, with
// collect --quiet. waiting is called when a run that is not scheduled starts
// to wait for one that holds the run lock. locked, when set, runs under the
// run lock after housekeeping.
type collection struct {
	d                  state.Dir
	offline, scheduled bool
	waiting            func()
	locked             func(*state.State)
}

func (c collection) run(ctx context.Context) (res *collect.Result, err error) {
	// A failure before housekeeping still gets its release check, so a
	// release that breaks collection can be replaced by the one that fixes it.
	// A run stopped by a signal, or by the view closing, failed at nothing.
	housekept := false
	defer func() {
		if err != nil && !housekept && ctx.Err() == nil {
			rescue(ctx, c.d, err, c.scheduled)
		}
	}()
	// A panic becomes an error before the rescue reads it, so the run can
	// still record it and look for a release that fixes it.
	defer func() {
		if v := recover(); v != nil {
			res, err = nil, &panicError{v, debug.Stack()}
		}
	}()
	cfg, err := c.d.LoadConfig()
	if err != nil {
		return nil, err
	}
	opts := collect.Options{
		Dir:      c.d,
		Version:  version,
		Probe:    probeEnv(),
		Hostname: deviceName(cfg),
		OSUser:   osUser(),
		Now:      clock,
		After: func(ctx context.Context, cfg *state.Config, st *state.State) {
			housekeeping(ctx, c.d, cfg, st, c.scheduled)
			housekept = true
			if c.locked != nil {
				c.locked(st)
			}
		},
	}
	if endpoint := relayURL(cfg); endpoint != "" && !c.offline {
		// Run signs with the key it loads under the run lock.
		opts.Relay = &relay.Client{BaseURL: endpoint}
	}
	if c.scheduled {
		opts.PullEvery = time.Hour
	} else {
		// A run the person started while another, usually the scheduled
		// one, collects waits for it and shows its result rather than
		// collect twice. The scheduler runs with --quiet and just skips.
		opts.Wait = lockWait
		opts.Waiting = c.waiting
	}
	return collect.Run(ctx, opts)
}

// rescue runs when a collection failed before its housekeeping: it keeps a
// panic as the last error and still checks for a release. A state.json that
// does not load is left as it is, and the check is then not throttled.
func rescue(ctx context.Context, d state.Dir, cause error, scheduled bool) {
	var pe *panicError
	panicked := errors.As(cause, &pe)
	if !panicked && selfupdate.Dev(version) {
		return
	}
	// A run that holds the lock does its own housekeeping.
	unlock, err := d.Lock()
	if err != nil {
		return
	}
	defer unlock()
	now := clock().UTC()
	st, err := d.LoadState()
	loaded := err == nil
	if !loaded {
		st = &state.State{}
	}
	changed := false
	if panicked {
		msg, _, _ := strings.Cut(pe.Error(), "\n")
		if len(msg) > 600 {
			msg = strings.ToValidUTF8(msg[:600], "")
		}
		st.LastError, st.LastErrorAt = msg, now
		changed = true
	}
	if updateIfDue(ctx, st, now, scheduled) {
		changed = true
	}
	if loaded && changed {
		_ = d.SaveState(st)
	}
}

// lockWait is how long a command the person started waits for another run,
// such as a scheduled one, to release the run lock. A scheduled run on a Mac
// takes up to about a minute at background priority.
const lockWait = 3 * time.Minute

// waitLock takes the run lock for a command the person started that changes
// what a collection also writes or acts on: the team key and cache, the
// binary, or the scheduler entry.
func waitLock(ctx context.Context, d state.Dir, stderr io.Writer) (func(), error) {
	return d.LockWait(ctx, lockWait, func() {
		fmt.Fprintln(stderr, "ai-usage: waiting for another run, such as the scheduled one, to finish")
	})
}
