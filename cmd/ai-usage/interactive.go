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

// tuiConfig is the interactive view of r on stdout. It is written with the
// escapes the static report would be, so --color and NO_COLOR mean the
// same in both. It starts on the background COLORFGBG says, and does not
// ask the terminal before it opens: Bubble Tea asks once it reads the
// terminal, so the keys typed meanwhile reach the view, and the answer
// then decides.
func (d *display) tuiConfig(stdout io.Writer, r view.Report) tui.Config {
	o := d.options(stdout, false)
	// The view follows the terminal's width unless --width fixes it.
	o.Width = d.width
	return tui.Config{Report: r, Options: o, Profile: d.profile(stdout)}
}

// viewConfig is the interactive view of res on stdout. It reloads the
// report from disk when a run saves new state, and `r` runs the collection
// the bare `ai-usage` runs, offline when this run is.
func viewConfig(d state.Dir, res *collect.Result, disp *display, offline bool, stdout io.Writer) tui.Config {
	c := disp.tuiConfig(stdout, reportAt(d, res, clock().UTC()))
	c.Load = func(now time.Time) (view.Report, error) {
		res, err := loadResult(d)
		if err != nil {
			return view.Report{}, err
		}
		return reportAt(d, res, now.UTC()), nil
	}
	// Unlike the bare run, `r` says nothing on the terminal the view is drawn
	// on, not even that it waits for another run.
	c.Refresh = func(ctx context.Context) error {
		_, err := collection{d: d, offline: offline}.run(ctx)
		return err
	}
	c.Stopping = func() {
		fmt.Fprintln(os.Stderr, "ai-usage: stopping the collection r started")
	}
	c.Watch = []string{d.StateFile(), d.TeamCacheFile()}
	c.Now = clock
	return c
}
