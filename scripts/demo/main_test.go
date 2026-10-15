package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neoromantic/ai-usage/internal/view"
)

// The team shows what the pictures are for: a Claude week over at the last
// day's pace, a tight Codex week, and one device whose burn is estimated
// and that keeps the team from sending its quota cycles.
func TestTeamReport(t *testing.T) {
	r := teamReport()
	var kinds []string
	for _, a := range r.Attention {
		kinds = append(kinds, a.Kind)
	}
	for _, k := range []string{view.AttentionOver, view.AttentionTight} {
		if !slices.Contains(kinds, k) {
			t.Errorf("attention %v has no %s", kinds, k)
		}
	}
	if w := teamWindow(r, "claude", "mira@studio.dev", "7d"); w == nil || w.State != view.StateOver || w.Forecast.Basis != view.BasisRecent {
		t.Errorf("mira's Claude week = %+v, want over at the recent pace", w)
	}
	if w := teamWindow(r, "codex", "mira@studio.dev", "7d"); w == nil || w.State != view.StateTight {
		t.Errorf("mira's Codex week = %+v, want tight", w)
	}
	if !r.Team.Estimated || r.Team.EstimatedReason == nil || !strings.Contains(*r.Team.EstimatedReason, "leo-air runs v0.3.4") {
		t.Errorf("estimated %v, reason %v; want leo-air on v0.3.4", r.Team.Estimated, r.Team.EstimatedReason)
	}
	if got := r.Team.CyclesHeldBackBy; len(got) != 1 || got[0] != "leo-air" {
		t.Errorf("cycles held back by %v, want leo-air", got)
	}
	for _, d := range r.Team.Devices {
		if d.BurnEstimated != (d.Label == "leo-air") {
			t.Errorf("%s burn estimated = %v", d.Label, d.BurnEstimated)
		}
		if d.Burn.Week <= 0 {
			t.Errorf("%s burned nothing in 7 days", d.Label)
		}
	}
}

// The solo page shows how full each subscription got before its resets,
// one of them read only partly, and the weeks Claude ran out.
func TestSoloUtilization(t *testing.T) {
	r := solo()
	for _, p := range r.Team.Providers {
		for _, a := range p.Accounts {
			if len(a.Utilization) == 0 {
				t.Errorf("%s %s has no utilization", p.Provider, a.Label)
			}
		}
	}
	u := teamUtilization(r, "claude", "mira@studio.dev")
	if len(u) != 2 || u[0].Window != "7d" || u[0].Out == 0 || !slices.ContainsFunc(u[0].Cycles, func(c view.Cycle) bool { return c.Partial }) {
		t.Errorf("mira's Claude = %+v, want a week that ran out and one read partly", u)
	}
}

// teamUtilization is an account's utilization in the team.
func teamUtilization(r view.Report, provider, label string) []view.Utilization {
	for _, p := range r.Team.Providers {
		for _, a := range p.Accounts {
			if p.Provider == provider && a.Label == label {
				return a.Utilization
			}
		}
	}
	return nil
}

// sgr is a terminal's bold or reverse, which the plain page keeps.
var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

// Every demo page fits 80 columns, and lists no project or device that
// burned nothing.
func TestPages(t *testing.T) {
	for name, r := range map[string]view.Report{"solo": solo(), "team": teamReport(), "homebrew": homebrew()} {
		for _, p := range r.Projects {
			if p.Burn.Week <= 0 && p.Burn.Quarter <= 0 {
				t.Errorf("%s: project %s burned nothing", name, p.Path)
			}
		}
		for _, devices := range []bool{false, true} {
			text := view.Text(r, view.Options{Width: 80, DeviceStatus: devices})
			for l := range strings.SplitSeq(sgr.ReplaceAllString(text, ""), "\n") {
				if n := utf8.RuneCountInString(l); n > 80 {
					t.Errorf("%s: %d columns: %q", name, n, l)
				}
			}
		}
	}
}

// The Homebrew page says how to update with brew.
func TestHomebrew(t *testing.T) {
	u := homebrew().Collector.Update
	if u.Method != view.MethodHomebrew || u.Command == nil || *u.Command != "brew upgrade neoromantic/tap/ai-usage" || u.Latest == nil || *u.Latest != "v0.5.1" {
		t.Errorf("update = %+v", u)
	}
}

// teamWindow is the named window of an account in the team, or nil.
func teamWindow(r view.Report, provider, label, name string) *view.Window {
	for _, p := range r.Team.Providers {
		for _, a := range p.Accounts {
			if p.Provider != provider || a.Label != label || a.Quota == nil {
				continue
			}
			for i := range a.Quota.Windows {
				if a.Quota.Windows[i].Name == name {
					return &a.Quota.Windows[i]
				}
			}
		}
	}
	return nil
}

// The install shot installs the release the demo's devices run.
func TestShotsInstallTheDemosRelease(t *testing.T) {
	b, err := os.ReadFile("shots.ts")
	if err != nil {
		t.Fatal(err)
	}
	if want := `const release = "` + latest + `";`; !strings.Contains(string(b), want) {
		t.Errorf("shots.ts does not say %s", want)
	}
}
