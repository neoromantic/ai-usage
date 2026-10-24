package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/neoromantic/ai-usage/internal/schedule"
	"github.com/neoromantic/ai-usage/internal/view"
)

type statusJSON struct {
	Collector *view.Collector `json:"collector"`
	Sources   []any           `json:"sources"`
	Install   install         `json:"install"`
}

func (d *device) status() (statusJSON, string) {
	d.t.Helper()
	out := d.ok("status", "--json")
	var sj statusJSON
	if err := json.Unmarshal([]byte(out), &sj); err != nil {
		d.t.Fatalf("status --json: %v\n%s", err, out)
	}
	return sj, out
}

// Before the first run, status says how ai-usage is installed, and what
// the scheduler runs, and creates nothing: no state folder, no team key, no
// device id. After it, it says the same beside the collector.
func TestStatusBeforeTheFirstRun(t *testing.T) {
	hermetic(t)
	brewBin, brewHome := "/opt/homebrew/opt/ai-usage/bin/ai-usage", "/Users/ann/Library/Application Support/ai-usage"
	cron := &fakeCrontab{tab: "0 3 * * * /usr/local/bin/backup\n" + schedule.Line(brewBin, brewHome, "/usr/bin:/bin") + "\n"}
	newScheduler = cron.scheduler
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)

	sj, raw := d.status()
	in := sj.Install
	if sj.Collector != nil || sj.Sources == nil || len(sj.Sources) != 0 || !strings.Contains(raw, `"collector": null`) {
		t.Fatalf("status --json = %s", raw)
	}
	if in.Version != "devel" || in.Method != "self" || in.Binary != exe || in.Home != d.dir || in.Collected {
		t.Fatalf("install = %+v", in)
	}
	if sch := in.Schedule; sch.State != "other" || sch.Program == nil || *sch.Program != brewBin || *sch.Home != brewHome || sch.Error != nil {
		t.Fatalf("schedule = %+v", sch)
	}
	if out := d.ok("status"); !strings.Contains(out, "nothing collected yet") || !strings.Contains(out, "schedule   other: "+brewBin+" --home "+brewHome) {
		t.Fatalf("status:\n%s", out)
	}
	if _, err := os.Stat(d.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status made the state folder: %v", err)
	}
	// A folder an earlier command made gets nothing more.
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	d.ok("status", "--json")
	if entries, _ := os.ReadDir(d.dir); len(entries) != 0 {
		t.Fatalf("status wrote %v", entries)
	}

	brewPrefix = func() string { return "/opt/homebrew" }
	if sj, _ := d.status(); sj.Install.Method != "homebrew" {
		t.Fatalf("a Homebrew copy: method %q", sj.Install.Method)
	}
	brewPrefix = func() string { return "" }

	d.ok("collect", "--offline")
	cron.tab = schedule.Line(exe, d.dir, "/usr/bin") + "\n"
	sj, _ = d.status()
	if sj.Collector == nil || len(sj.Sources) != 4 || !sj.Install.Collected || sj.Install.Schedule.State != "active" || *sj.Install.Schedule.Program != exe {
		t.Fatalf("after the first run: %+v", sj)
	}
	cron.tab = ""
	if sj, _ = d.status(); sj.Install.Schedule.State != "absent" || sj.Install.Schedule.Program != nil {
		t.Fatalf("no entry: %+v", sj.Install.Schedule)
	}
}
