package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/tui"
	"github.com/neoromantic/ai-usage/internal/view"
	"github.com/neoromantic/ai-usage/relay"
)

// stdinTTY says whether standard input is a terminal. Tests pin it.
var stdinTTY = func() bool {
	_, ok := terminal(os.Stdin)
	return ok
}

// interactive says whether the report opens in the interactive view rather
// than printing: only when standard input and output are both terminals,
// and not for JSON, --plain, a dumb terminal, or the report that carries
// the guide, which is the installer's first run.
func (d *display) interactive(stdout io.Writer, jsonOut, guide bool) bool {
	_, out := terminal(stdout)
	return openView(stdinTTY(), out, jsonOut, d.plain, guide, os.Getenv("TERM"))
}

func openView(in, out, jsonOut, plain, guide bool, term string) bool {
	return in && out && !jsonOut && !plain && !guide && term != "dumb"
}

// showView opens the interactive view on res until the person quits. It
// reloads the report from disk when a run saves new state, and `r` runs the
// collection the bare `ai-usage` runs, offline when this run is.
func showView(ctx context.Context, d state.Dir, res *collect.Result, endpoint string, disp *display, offline bool, stdout io.Writer) error {
	o := disp.options(stdout)
	// The view follows the terminal's width unless --width fixes it.
	o.Width = disp.width
	return tui.Run(ctx, tui.Config{
		Report:  reportAt(d, res, endpoint, clock().UTC()),
		Options: o,
		Load: func(now time.Time) (view.Report, error) {
			res, err := loadResult(d)
			if err != nil {
				return view.Report{}, err
			}
			return reportAt(d, res, relayURL(res.Config), now.UTC()), nil
		},
		Refresh: collectNow(d, offline),
		Stopping: func() {
			fmt.Fprintln(os.Stderr, "ai-usage: stopping the collection r started")
		},
		Watch: []string{d.Path("state.json"), d.Path("team-cache.json")},
		Now:   clock,
	}, os.Stdin, stdout)
}

// reportAt is the report of a run result as of now.
func reportAt(d state.Dir, res *collect.Result, endpoint string, now time.Time) view.Report {
	return view.Build(view.Input{
		Version:  version,
		RelayURL: endpoint,
		Config:   res.Config,
		State:    liveSchedule(d, res.State),
		Key:      res.Key,
		Doc:      res.Doc,
		Team:     res.Team,
		Hostname: deviceName(res.Config),
		OSUser:   osUser(),
		Now:      now,
	})
}

// collectNow is the bare run's collection, for `r` in the interactive view:
// housekeeping, the relay unless offline, and a wait for a run that holds
// the lock, such as the scheduled one, whose result it then shows rather
// than collect twice. It says nothing on the terminal the view is drawn on.
func collectNow(d state.Dir, offline bool) func(context.Context) error {
	return func(ctx context.Context) (err error) {
		housekept := false
		defer func() {
			// A collection stopped because the view closed failed at nothing.
			if err != nil && !housekept && ctx.Err() == nil {
				rescue(ctx, d, err)
			}
		}()
		cfg, err := d.LoadConfig()
		if err != nil {
			return err
		}
		opts := collect.Options{
			Dir:      d,
			Version:  version,
			Probe:    probeEnv(),
			Hostname: deviceName(cfg),
			OSUser:   osUser(),
			Now:      clock,
			After: func(ctx context.Context, cfg *state.Config, st *state.State) {
				housekeeping(ctx, d, cfg, st)
				housekept = true
			},
			Wait:    lockWait,
			Waiting: func() {},
		}
		if endpoint := relayURL(cfg); endpoint != "" && !offline {
			opts.Relay = &relay.Client{BaseURL: endpoint}
		}
		_, err = runCollect(ctx, opts)
		return err
	}
}
