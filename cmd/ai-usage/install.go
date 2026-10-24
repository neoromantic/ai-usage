package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/neoromantic/ai-usage/internal/schedule"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/view"
)

// install is how ai-usage is installed on this device, as `status --json`
// says it, before the first run too: it is read without collecting, and
// without writing anything, so a front end such as an installer can ask
// before ai-usage has a team key or a device id.
type install struct {
	// Version is this binary's, Binary where it is, by the path Homebrew
	// keeps across upgrades when Homebrew installed it, and Method how it
	// updates, as collector.update.method says.
	Version string `json:"version"`
	Binary  string `json:"binary"`
	Method  string `json:"method"`
	// Home is the state folder this binary uses, and Collected whether a
	// run has collected into it.
	Home      string          `json:"home"`
	Collected bool            `json:"collected"`
	Schedule  installSchedule `json:"schedule"`
}

// installSchedule is the system scheduler's entry for the collector.
type installSchedule struct {
	// State is "active" when the scheduler runs this binary, or the copy
	// Homebrew installed beside it, with this state folder; "absent",
	// "other", "disabled", "refused", "stale", or "unloaded", as `ai-usage
	// schedule status` says them; "foreground" while `ai-usage schedule run`
	// collects into this state folder; "unknown" when the scheduler could
	// not be asked, with Error saying why.
	State string `json:"state"`
	// Program and Home are what the entry runs, whichever binary and state
	// folder that is; nil without an entry, and on Windows, whose task is not
	// read back.
	Program *string `json:"program"`
	Home    *string `json:"home"`
	Error   *string `json:"error"`
}

// scheduleStates names each schedule.State in installSchedule.State.
var scheduleStates = map[schedule.State]string{
	schedule.Absent:   "absent",
	schedule.Other:    "other",
	schedule.Disabled: "disabled",
	schedule.Active:   "active",
	schedule.Refused:  "refused",
	schedule.Stale:    "stale",
	schedule.Unloaded: "unloaded",
}

// installOf is how ai-usage is installed, with the state folder d.
func installOf(ctx context.Context, d state.Dir, collected bool) install {
	in := install{Version: version, Method: view.MethodSelf, Collected: collected}
	if brewPrefix() != "" {
		in.Method = view.MethodHomebrew
	}
	in.Binary, _ = executable()
	in.Home, _ = filepath.Abs(string(d))
	sch := &in.Schedule
	s := newScheduler()
	if e, err := s.Entry(ctx); err == nil && e.Program != "" {
		sch.Program, sch.Home = textPtr(e.Program), textPtr(e.Home)
	}
	exe, home, err := job(d)
	if err == nil {
		var got schedule.State
		if got, err = s.Lookup(ctx, exe, home); err == nil {
			sch.State = scheduleStates[got]
		}
	}
	switch {
	case foreground(d):
		sch.State, sch.Error = "foreground", nil
	case err != nil:
		sch.State, sch.Error = "unknown", textPtr(err.Error())
	}
	return in
}

// foreground says `ai-usage schedule run` collects into d. Unlike
// d.Foreground, it makes no lock file where none is.
func foreground(d state.Dir) bool {
	if _, err := os.Stat(d.Path("schedule.lock")); err != nil {
		return false
	}
	return d.Foreground()
}

// text is what `ai-usage status` prints before the first run.
func (in install) text() string {
	var b strings.Builder
	how := "updates itself"
	if in.Method == view.MethodHomebrew {
		how = "Homebrew updates it"
	}
	fmt.Fprintf(&b, "nothing collected yet; run `ai-usage` first\n")
	fmt.Fprintf(&b, "version    %s, %s\n", in.Version, how)
	fmt.Fprintf(&b, "binary     %s\n", in.Binary)
	fmt.Fprintf(&b, "state      %s\n", in.Home)
	sch := in.Schedule
	switch {
	case sch.Error != nil:
		fmt.Fprintf(&b, "schedule   %s: %s\n", sch.State, *sch.Error)
	case sch.Program != nil:
		fmt.Fprintf(&b, "schedule   %s: %s --home %s\n", sch.State, *sch.Program, *sch.Home)
	default:
		fmt.Fprintf(&b, "schedule   %s\n", sch.State)
	}
	return b.String()
}

func textPtr(s string) *string { return &s }
