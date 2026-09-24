package view

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// recent are the periods shorter than 90 days.
var recent = Today.bit() | Week.bit() | Month.bit()

func teamDevice(t *testing.T, r Report, label string) TeamDevice {
	t.Helper()
	for _, d := range r.Team.Devices {
		if d.Label == label {
			return d
		}
	}
	t.Fatalf("no device %q", label)
	return TeamDevice{}
}

func perDevice(t *testing.T, a TeamAccount, id string) DeviceUsage {
	t.Helper()
	for _, d := range a.PerDevice {
		if d.DeviceID == id {
			return d
		}
	}
	t.Fatalf("%s has no device %q", a.Label, id)
	return DeviceUsage{}
}

// column is the matrix column of the subscription with label.
func column(t *testing.T, mx Matrix, label string) int {
	t.Helper()
	for i, c := range mx.Columns {
		if c.Label == label {
			return i
		}
	}
	t.Fatalf("no column %q", label)
	return -1
}

func row(t *testing.T, mx Matrix, device string) Row {
	t.Helper()
	for _, r := range mx.Rows {
		if r.Device == device {
			return r
		}
	}
	t.Fatalf("no row %q", device)
	return Row{}
}

// An account from a collector older than v0.2.0 has its tokens over 90
// days and nothing shorter: a period is 0 when the account was last active
// before it began, and not known otherwise, never 0 for want of days.
func TestOlderCollector(t *testing.T) {
	r := olderTeam(t)

	old := teamDevice(t, r, "MacBook-Old")
	if want := (Usage{Quarter: 620_000_000, Unknown: recent}); old.Usage != want {
		t.Fatalf("old device usage = %+v", old.Usage)
	}
	if u := teamDevice(t, r, "srv1").Usage; u.Unknown != 0 || u.Week != 60_000_000 {
		t.Fatalf("srv1 usage = %+v", u)
	}

	ann := findTeamAccount(t, r, "claude", "ann@acme.dev")
	if u := perDevice(t, ann, "d-macbook-old").Usage; u != (Usage{Quarter: 400_000_000, Unknown: recent}) {
		t.Fatalf("ann on the old device = %+v", u)
	}
	// The account adds what is known: at least annbook's.
	if u := ann.Usage; u != (Usage{Today: 4_000_000, Week: 8_000_000, Month: 12_000_000, Quarter: 412_000_000, Unknown: recent}) {
		t.Fatalf("ann usage = %+v", u)
	}
	// lee was last active on the old device 40 days ago: every period is
	// known.
	lee := findTeamAccount(t, r, "codex", "lee@corp.test")
	if u := perDevice(t, lee, "d-macbook-old").Usage; u != (Usage{Quarter: 120_000_000}) {
		t.Fatalf("lee on the old device = %+v", u)
	}

	// The old device used ann and kim since their windows began, so it
	// counts. It is the busiest of kim, its only user, though its tokens
	// are not known; of ann, annbook is, whose are.
	for _, c := range []struct {
		provider, label, busiest string
		users                    int
	}{
		{"claude", "ann@acme.dev", "annbook", 2},
		{"claude", "kim@corp.test", "MacBook-Old", 1},
		{"codex", "lee@corp.test", "srv1", 1},
	} {
		a := findTeamAccount(t, r, c.provider, c.label)
		if a.Users != c.users || a.Busiest == nil || *a.Busiest != c.busiest {
			t.Errorf("%s users = %d busiest %v", c.label, a.Users, a.Busiest)
		}
	}

	mx := r.Team.Matrix
	annCol, kimCol, leeCol := column(t, mx, "ann@acme.dev"), column(t, mx, "kim@corp.test"), column(t, mx, "lee@corp.test")
	if c := mx.Columns[annCol]; !c.WindowUnknown || c.WindowTokens != 8_000_000 || c.Usage.Week != 8_000_000 || c.Usage.Unknown != recent {
		t.Fatalf("ann column = %+v", c)
	}
	if c := mx.Columns[leeCol]; c.WindowUnknown || c.WindowTokens != 30_000_000 {
		t.Fatalf("lee column = %+v", c)
	}
	oldRow, here, srv := row(t, mx, "MacBook-Old"), row(t, mx, "annbook"), row(t, mx, "srv1")
	if oldRow.Usage != old.Usage {
		t.Fatalf("old row = %+v", oldRow.Usage)
	}
	for _, i := range []int{annCol, kimCol} {
		if c := oldRow.Cells[i]; !c.WindowUnknown || c.Share != nil || c.WindowTokens != 0 {
			t.Fatalf("old cell %d = %+v", i, c)
		}
	}
	// ann's window cannot be split while the old device's part is not
	// known: annbook spent some, so its share is not known either.
	if c := here.Cells[annCol]; c.WindowUnknown || c.WindowTokens != 8_000_000 || c.Share != nil {
		t.Fatalf("annbook ann cell = %+v", c)
	}
	// A device that spent none has none of it.
	if c := here.Cells[kimCol]; c.Share == nil || *c.Share != 0 {
		t.Fatalf("annbook kim cell = %+v", c)
	}
	if c := oldRow.Cells[leeCol]; c.WindowUnknown || c.Share == nil || *c.Share != 0 || c.Usage != (Usage{Quarter: 120_000_000}) {
		t.Fatalf("old lee cell = %+v", c)
	}
	if c := srv.Cells[leeCol]; c.Share == nil || *c.Share != 40 {
		t.Fatalf("srv1 lee cell = %+v", c)
	}

	// JSON says which periods are not known, and says nothing of those
	// that are.
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"90d":620000000,"unknown":["today","7d","30d"]`, `"window_unknown":true`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON lacks %s", want)
		}
	}
	if k, _ := json.Marshal(teamDevice(t, r, "srv1").Usage); strings.Contains(string(k), "unknown") {
		t.Errorf("known usage in JSON: %s", k)
	}
	// And reads it back: the page is the same.
	var back Report
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	for _, o := range []Options{{Width: 120, Loc: time.UTC}, {Width: 120, Loc: time.UTC, Period: Month}, {Width: 120, Loc: time.UTC, Share: true}} {
		if got, want := Text(back, o), Text(r, o); got != want {
			t.Fatalf("%+v: the page differs after JSON:\n%s\nwant:\n%s", o, got, want)
		}
	}
}

func TestOlderUsage(t *testing.T) {
	// now is 12:00 UTC: today began 12 hours ago.
	for _, c := range []struct {
		name  string
		last  *time.Time
		shift int
		want  Usage
	}{
		{"active today", tp(now.Add(-2 * time.Hour)), 0, Usage{Quarter: 100, Unknown: recent}},
		{"no activity known", nil, 0, Usage{Quarter: 100, Unknown: recent}},
		{"active 3 days ago", tp(now.Add(-3 * 24 * time.Hour)), 0, Usage{Quarter: 100, Unknown: Week.bit() | Month.bit()}},
		{"active 40 days ago", tp(now.Add(-40 * 24 * time.Hour)), 0, Usage{Quarter: 100}},
		{"before the 90 days", tp(now.Add(-100 * 24 * time.Hour)), 0, Usage{}},
		// The device's 90 days began a day before the report's, so they
		// are not the report's either.
		{"collected yesterday", tp(now.Add(-30 * time.Hour)), 1, Usage{Unknown: Week.bit() | Month.bit() | Quarter.bit()}},
	} {
		if got := olderUsage(100, c.last, c.shift, now); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// Hermes on an older collector splits its tokens over 90 days among the
// logins it spent through, as it splits its days on a newer one.
func TestOlderCollectorSplitsHermesByLogin(t *testing.T) {
	start := now.Add(-3 * 24 * time.Hour)
	q := func(pct float64) *state.Quota {
		return &state.Quota{At: now, Source: "harness", Windows: []snapshot.Window{week7(pct, start.Add(week))}}
	}
	f := newFixture(t, emptyState())
	other := emptyState()
	addAccount(other, "codex", "bots@acme.dev", false, q(40), 0)
	addAccount(other, "codex", "sam@mail.test", true, q(10), 0)
	addHermes(other, "openai-codex", "codex", "bots@acme.dev", 0)
	for _, s := range []struct {
		project, to string
		n           int64
		at          time.Time
	}{{"/bots", "bots@acme.dev", 4_000_000, now.Add(-2 * time.Hour)}, {"/sam", "sam@mail.test", 1_000_000, now.Add(-40 * 24 * time.Hour)}} {
		spend(other, "hermes", "openai-codex", s.project, s.n, s.at)
		other.Sessions[state.Key("hermes", "openai-codex", s.project, s.at.String())].Via = map[string]snapshot.Tokens{state.Key("codex", s.to): {Input: s.n}}
	}
	r := withTeam(t, f, olderDoc(t, f.key, "d-other-device", "otherbox", now, other))

	mx := r.Team.Matrix
	oldRow := row(t, mx, "otherbox")
	for label, want := range map[string]int64{"bots@acme.dev": 4_000_000, "sam@mail.test": 1_000_000} {
		i := column(t, mx, label)
		// Hermes was last active 2 hours ago, through either login as far as
		// the snapshot says.
		if c := oldRow.Cells[i]; c.Usage != (Usage{Quarter: want, Unknown: recent}) || !c.WindowUnknown || c.Share != nil {
			t.Fatalf("%s cell = %+v", label, c)
		}
		if a := findTeamAccount(t, r, "codex", label); a.Users != 1 || a.Busiest == nil || *a.Busiest != "otherbox" {
			t.Fatalf("%s users = %d %v", label, a.Users, a.Busiest)
		}
	}
}
