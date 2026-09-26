package view

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

func TestAccountOfThisDevice(t *testing.T) {
	st := emptyState()
	start := now.Add(-2 * 24 * time.Hour)
	addAccount(st, "claude", "ann@acme.io", true, &state.Quota{At: now, Source: "cache", Windows: []snapshot.Window{
		{Name: "5h", Percent: 10, Minutes: 300, ResetsAt: tp(now.Add(4 * time.Hour))},
		week7(50, start.Add(week)),
	}}, 0)
	st.Accounts[state.Key("claude", "ann@acme.io")].Plan = "max"
	spend(st, "claude", "ann@acme.io", "/work/web", 100, now.Add(-time.Hour), now.Add(-26*time.Hour), now.Add(-10*24*time.Hour), now.Add(-40*24*time.Hour))
	r := Build(newFixture(t, st).in)

	a := findAccount(t, r, "claude", "ann@acme.io")
	if a.Name != "ann" || a.State != StateOver || a.Plan == nil || *a.Plan != "max" || !a.Current {
		t.Fatalf("account = %+v", a)
	}
	if want := (Usage{Today: 100, Week: 200, Month: 300, Quarter: 400}); a.Usage != want {
		t.Fatalf("usage = %+v, want %+v", a.Usage, want)
	}
	if len(a.Days) != 41 || a.Days[0] != 100 || a.Days[1] != 100 || a.Days[40] != 100 {
		t.Fatalf("days = %v", a.Days)
	}
	ws := a.Quota.Windows
	if len(ws) != 2 || ws[0].Main || !ws[1].Main || ws[1].State != StateOver || ws[1].Forecast.Percent != 175 || ws[0].State != StateOK {
		t.Fatalf("windows = %+v", ws)
	}
	if len(r.Projects) != 1 || r.Projects[0].Path != "/work/web" || r.Projects[0].Usage.Week != 200 || !reflect.DeepEqual(r.Projects[0].Providers, []string{"claude"}) {
		t.Fatalf("projects = %+v", r.Projects)
	}
}

func TestUsageShiftsByTheDaysSinceADeviceReported(t *testing.T) {
	st := emptyState()
	addAccount(st, "codex", "bob", true, nil, 0)
	f := newFixture(t, st)
	other := emptyState()
	addAccount(other, "codex", "bob", true, nil, 0)
	// The other device reported 2 days ago: its today is the report's day
	// before yesterday.
	at := now.Add(-48 * time.Hour)
	spend(other, "codex", "bob", "/w", 10, at.Add(-time.Hour), at.Add(-24*time.Hour), at.Add(-6*24*time.Hour))
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", at, other))
	bob := findTeamAccount(t, r, "codex", "bob")
	if want := (Usage{Today: 0, Week: 20, Month: 30, Quarter: 30}); bob.Usage != want {
		t.Fatalf("usage = %+v, want %+v", bob.Usage, want)
	}
	for _, d := range r.Team.Devices {
		if d.Device == "d-other-device" && d.Usage.Week != 20 {
			t.Fatalf("device usage = %+v", d.Usage)
		}
	}
}

func TestTeamAddsTokensAndTakesNewestReadingOfEachWindow(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann", true, &state.Quota{At: now.Add(-2 * time.Hour), Windows: []snapshot.Window{
		win("5h", 40, now.Add(time.Hour)), win("7d", 10, now.Add(72*time.Hour)),
	}}, 1000)
	f := newFixture(t, st)

	other := emptyState()
	other.Sources["codex"] = state.Source{Status: "error", Error: "app-server exited without answering"}
	addAccount(other, "claude", "ann", true, &state.Quota{At: now.Add(-10 * time.Minute), Windows: []snapshot.Window{win("5h", 70, now.Add(time.Hour))}}, 500)
	addAccount(other, "codex", "bob", true, nil, 20)
	third := emptyState()
	addAccount(third, "claude", "ann", false, &state.Quota{At: now.Add(-time.Hour), Windows: []snapshot.Window{win("5h", 99, now.Add(time.Hour))}}, 1)

	f.in.Team = collect.TeamCache{PulledAt: now.Add(-time.Minute), Team: f.key.Fingerprint(), Docs: []snapshot.Doc{
		otherDoc(t, f.key, "d-other-device", "otherbox", now.Add(-5*time.Minute), other),
		otherDoc(t, f.key, "d-third-device", "aaabox", now.Add(-time.Hour), third),
		// The cache's copy of this device is older than the fresh doc and is replaced by it.
		otherDoc(t, f.key, "d-this-device", "thisbox", now.Add(-time.Hour), emptyState()),
	}}
	r := Build(f.in)

	if r.Team.PulledAt == nil || !r.Team.PulledAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("pulled at = %v", r.Team.PulledAt)
	}
	var devs []string
	for _, d := range r.Team.Devices {
		devs = append(devs, d.Device)
	}
	if want := []string{"d-this-device", "d-third-device", "d-other-device"}; !reflect.DeepEqual(devs, want) {
		t.Fatalf("devices = %v", devs)
	}
	if d := r.Team.Devices[2]; d.AgeSeconds != 300 || d.CollectorVersion != "v1.2.0" || d.OSUser != "kim" || !d.Old || d.Error == nil || *d.Error != "codex: app-server exited without answering" {
		t.Fatalf("other device = %+v", d)
	}
	if r.Team.Latest == nil || *r.Team.Latest != "v1.2.3" || r.Team.Devices[0].Old {
		t.Fatalf("latest = %v, this device = %+v", r.Team.Latest, r.Team.Devices[0])
	}

	ann := findTeamAccount(t, r, "claude", "ann")
	if ann.Tokens != (snapshot.Tokens{Input: 1501, Output: 750}) || ann.Sessions != 3 || len(ann.Devices) != 3 || !ann.Current || !ann.Subscription {
		t.Fatalf("ann = %+v", ann)
	}
	// Each window is its newest reading, never a sum or an average: the 5h
	// from otherbox, the 7d from this device, which alone read it.
	ws := ann.Quota.Windows
	if len(ws) != 2 || ws[0].Percent != 70 || !ws[0].ObservedAt.Equal(now.Add(-10*time.Minute)) || ws[1].Percent != 10 || !ws[1].ObservedAt.Equal(now.Add(-2*time.Hour)) || !ws[1].Main {
		t.Fatalf("ann windows = %+v", ws)
	}
	if ann.Quota.Device != "otherbox (kim)" || !ann.Quota.ObservedAt.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("ann quota = %+v", ann.Quota)
	}
	var provs []string
	for _, p := range r.Team.Providers {
		provs = append(provs, p.Provider)
	}
	if !reflect.DeepEqual(provs, []string{"claude", "codex"}) {
		t.Fatalf("providers = %v", provs)
	}
}

func TestTeamCacheOfAnotherTeamIsIgnored(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann", true, nil, 10)
	f := newFixture(t, st)
	oldKey := mustKey(t)
	f.in.Team = collect.TeamCache{PulledAt: now, Team: oldKey.Fingerprint(), Docs: []snapshot.Doc{
		otherDoc(t, oldKey, "d-other-device", "otherbox", now, st),
	}}
	r := Build(f.in)
	if r.Team.PulledAt != nil || len(r.Team.Devices) != 1 || !r.Team.Devices[0].This || len(r.Team.Matrix.Rows) != 1 {
		t.Fatalf("team = %+v", r.Team)
	}
}

func TestUnreadableLabels(t *testing.T) {
	st := emptyState()
	f := newFixture(t, st)
	stranger := mustKey(t)
	other := emptyState()
	other.LastError = "boom"
	addAccount(other, "grok", "gina", true, nil, 5)
	doc := otherDoc(t, stranger, "d-other-device", "otherbox", now, other)
	doc.Team = f.key.Fingerprint()
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: []snapshot.Doc{doc}}
	r := Build(f.in)
	d := r.Team.Devices[1]
	if d.Label != "(unreadable)" || d.OSUser != "(unreadable)" || d.LastError == nil || *d.LastError != "(unreadable)" {
		t.Fatalf("device = %+v", d)
	}
	if len(r.Team.Providers) != 1 || r.Team.Providers[0].Accounts[0].Label != "(unreadable)" {
		t.Fatalf("providers = %+v", r.Team.Providers)
	}
}

// TestTeamDeviceUpdate: another device's failed release check comes apart
// from its last run's error, a last_error from an older collector stays
// whole, and an old device says since when this device's reads have found
// it on its release.
func TestTeamDeviceUpdate(t *testing.T) {
	st := emptyState()
	st.Update.Error = "update check: HTTP 502 from github.com"
	f := newFixture(t, st)
	f.seal(now)
	other := emptyState()
	other.LastError = "codex: not logged in"
	other.Update.Error = "update check: HTTP 429 from github.com: Too Many Requests"
	// A collector from before the update line sealed last_error as it was.
	older := otherDoc(t, f.key, "d-older-device", "olderbox", now, emptyState())
	older.LastError = f.key.Seal("claude: 2 malformed lines; update: none")
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: []snapshot.Doc{
		otherDoc(t, f.key, "d-other-device", "otherbox", now, other), older,
	}, Behind: map[string]collect.Behind{
		"d-other-device": {Version: "v1.2.0", Since: now.Add(-30 * time.Hour)},
		"d-older-device": {Version: "v1.1.0", Since: now.Add(-30 * time.Hour)},
	}}
	devs := map[string]TeamDevice{}
	for _, d := range Build(f.in).Team.Devices {
		devs[d.Device] = d
	}
	this, o, old := devs["d-this-device"], devs["d-other-device"], devs["d-older-device"]
	if this.UpdateError != nil || this.LastError != nil || this.BehindSince != nil {
		t.Errorf("this device: update error %v, last error %v, behind since %v", this.UpdateError, this.LastError, this.BehindSince)
	}
	if o.LastError == nil || *o.LastError != other.LastError || o.UpdateError == nil || *o.UpdateError != other.Update.Error {
		t.Errorf("other device: last error %v, update error %v", o.LastError, o.UpdateError)
	}
	if !o.Old || o.BehindSince == nil || !o.BehindSince.Equal(now.Add(-30*time.Hour)) {
		t.Errorf("other device: old %v since %v", o.Old, o.BehindSince)
	}
	// Found behind on another release, it has run this one for no time
	// that is known.
	if old.LastError == nil || *old.LastError != "claude: 2 malformed lines; update: none" || old.UpdateError != nil || !old.Old || old.BehindSince != nil {
		t.Errorf("older device: last error %v, update error %v, old %v since %v", old.LastError, old.UpdateError, old.Old, old.BehindSince)
	}
}

func TestShortNames(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "sam@mail.test", true, nil, 10)
	addAccount(st, "claude", "lee@corp.test", false, nil, 10)
	addAccount(st, "codex", "sam@a.io", true, nil, 10)
	addAccount(st, "codex", "sam@b.io", false, nil, 10)
	addAccount(st, "codex", "unknown", false, nil, 10)
	addAccount(st, "grok", "a4c2e917-5e4f-4a3b-8c2d-1e0f9a8b7c6d", true, nil, 10)
	r := Build(newFixture(t, st).in)
	for _, c := range []struct{ provider, label, name string }{
		{"claude", "sam@mail.test", "sam"},
		{"claude", "lee@corp.test", "lee"},
		// The same name within a provider: both keep the full label.
		{"codex", "sam@a.io", "sam@a.io"},
		{"codex", "sam@b.io", "sam@b.io"},
		{"codex", "unknown", "unknown"},
		{"grok", "a4c2e917-5e4f-4a3b-8c2d-1e0f9a8b7c6d", "a4c2e917"},
	} {
		if a := findTeamAccount(t, r, c.provider, c.label); a.Name != c.name || a.Alias != nil {
			t.Errorf("%s %s: name %q alias %v, want %q", c.provider, c.label, a.Name, a.Alias, c.name)
		}
		if a := findAccount(t, r, c.provider, c.label); a.Name != c.name {
			t.Errorf("%s %s: local name %q, want %q", c.provider, c.label, a.Name, c.name)
		}
	}
	for in, want := range map[string]bool{"0f1e2d3c4b5a49688776655443322110": true, "openai-codex": false, "abcdefghijklmnopq": false, "a1b2c3": false} {
		if idLike(in) != want {
			t.Errorf("idLike(%q) = %v", in, !want)
		}
	}
}

func TestAliasesTravelAndTheNewestWins(t *testing.T) {
	st := emptyState()
	addAccount(st, "codex", "bots@corp.test", true, nil, 10)
	addAccount(st, "codex", "sam@mail.test", false, nil, 10)
	addAccount(st, "claude", "sam@mail.test", false, nil, 10)
	f := newFixture(t, st)
	f.in.Config.Aliases = map[string]state.Alias{
		state.Key("codex", "bots@corp.test"): {Name: "bots-old", At: now.Add(-2 * time.Hour)},
		state.Key("codex", "sam@mail.test"):  {Name: "sp", At: now.Add(-3 * time.Hour)},
	}
	f.seal(now)

	other := emptyState()
	addAccount(other, "codex", "bots@corp.test", true, nil, 10)
	addAccount(other, "codex", "sam@mail.test", true, nil, 10)
	cfg := state.Config{Device: "d-other-device", Aliases: map[string]state.Alias{
		// Newer than this device's: it wins.
		state.Key("codex", "bots@corp.test"): {Name: "build", At: now.Add(-time.Hour)},
		// A newer clearing removes the name.
		state.Key("codex", "sam@mail.test"): {At: now.Add(-time.Hour)},
	}}
	r := withTeam(t, f, docWith(t, f.key, cfg, "otherbox", now, other))
	if a := findTeamAccount(t, r, "codex", "bots@corp.test"); a.Name != "build" || a.Alias == nil || *a.Alias != "build" {
		t.Fatalf("bots = %q %v", a.Name, a.Alias)
	}
	if a := findTeamAccount(t, r, "codex", "sam@mail.test"); a.Name != "sam" || a.Alias != nil {
		t.Fatalf("cleared = %q %v", a.Name, a.Alias)
	}
	// An alias names one provider's account.
	if a := findTeamAccount(t, r, "claude", "sam@mail.test"); a.Name != "sam" {
		t.Fatalf("claude = %q", a.Name)
	}
	if a := findAccount(t, r, "codex", "bots@corp.test"); a.Name != "build" {
		t.Fatalf("local name = %q", a.Name)
	}
	var cols []string
	for _, c := range r.Team.Matrix.Columns {
		cols = append(cols, c.Name)
	}
	if !reflect.DeepEqual(cols, []string{"sam", "build", "sam"}) {
		t.Fatalf("columns = %v", cols)
	}
}

// A name another account of the provider goes by, set before this device
// read that account, does not tell the two apart: both go by the full label.
func TestAliasThatAnotherAccountGoesBy(t *testing.T) {
	st := emptyState()
	addAccount(st, "codex", "ann@a.io", true, nil, 10)
	addAccount(st, "codex", "ann@b.io", false, nil, 10)
	addAccount(st, "claude", "lee@corp.test", true, nil, 10)
	f := newFixture(t, st)
	f.in.Config.Aliases = map[string]state.Alias{
		state.Key("codex", "ann@a.io"):       {Name: "kim", At: now.Add(-time.Hour)},
		state.Key("claude", "lee@corp.test"): {Name: "Sam", At: now.Add(-time.Hour)},
	}
	f.seal(now)
	other := emptyState()
	addAccount(other, "codex", "kim@corp.test", true, nil, 10)
	addAccount(other, "claude", "sam@mail.test", true, nil, 10)
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))

	for _, c := range []struct{ provider, label, name string }{
		{"codex", "ann@a.io", "ann@a.io"},
		{"codex", "kim@corp.test", "kim@corp.test"},
		// The alias leaves ann@b.io the only ann.
		{"codex", "ann@b.io", "ann"},
		// Names differ only in case: the alias command refuses that too.
		{"claude", "lee@corp.test", "lee@corp.test"},
		{"claude", "sam@mail.test", "sam@mail.test"},
	} {
		if a := findTeamAccount(t, r, c.provider, c.label); a.Name != c.name {
			t.Errorf("%s %s: name %q, want %q", c.provider, c.label, a.Name, c.name)
		}
	}
	if a := findTeamAccount(t, r, "codex", "ann@a.io"); a.Alias == nil || *a.Alias != "kim" {
		t.Fatalf("alias = %v", a.Alias)
	}
	seen := map[string]bool{}
	for _, c := range r.Team.Matrix.Columns {
		k := c.Provider + ":" + strings.ToLower(c.Name)
		if seen[k] {
			t.Fatalf("two columns go by %s: %+v", k, r.Team.Matrix.Columns)
		}
		seen[k] = true
	}
}

// Two accounts the team gave one name go by their full labels on the page
// too: in SUBSCRIPTIONS, in ATTENTION, and in USAGE on a team of one.
func TestAliasThatTwoAccountsGoByOnThePage(t *testing.T) {
	out := func() *state.Quota {
		return &state.Quota{At: now, Source: "harness", Windows: []snapshot.Window{week7(100, now.Add(48*time.Hour))}}
	}
	st := emptyState()
	addAccount(st, "codex", "ann@acme.dev", true, out(), 10)
	f := newFixture(t, st)
	f.in.Config.Aliases = map[string]state.Alias{state.Key("codex", "ann@acme.dev"): {Name: "kim", At: now.Add(-time.Hour)}}
	f.seal(now)
	other := emptyState()
	addAccount(other, "codex", "sam@mail.test", true, out(), 10)
	cfg := state.Config{Device: "d-other-device", Aliases: map[string]state.Alias{
		state.Key("codex", "sam@mail.test"): {Name: "kim", At: now.Add(-2 * time.Hour)},
	}}
	two := withTeam(t, f, docWith(t, f.key, cfg, "otherbox", now, other))

	// On one device, the alias came before the account that goes by it.
	addAccount(st, "codex", "kim@corp.test", false, nil, 10)
	f.seal(now)
	one := withTeam(t, f)

	for _, c := range []struct {
		r      Report
		title  string
		labels []string
	}{
		{two, "ATTENTION", []string{"ann@acme.dev", "sam@mail.test"}},
		{two, "SUBSCRIPTIONS", []string{"ann@acme.dev", "sam@mail.test"}},
		{one, "USAGE", []string{"ann@acme.dev", "kim@corp.test"}},
	} {
		// A section goes on to the next title, the next line that starts
		// with a capital.
		var lines []string
		in := false
		for _, l := range Render(c.r, Options{Width: 160, Loc: sampleZone}).Body {
			l = sgr.ReplaceAllString(l, "")
			if l != "" && l[0] >= 'A' && l[0] <= 'Z' {
				in = strings.HasPrefix(l, c.title)
			}
			if in {
				lines = append(lines, l)
			}
		}
		text := strings.Join(lines, "\n")
		for _, l := range c.labels {
			if !strings.Contains(text, l) {
				t.Errorf("%s does not name %s:\n%s", c.title, l, text)
			}
		}
		if slices.Contains(strings.Fields(text), "kim") {
			t.Errorf("%s names an account kim:\n%s", c.title, text)
		}
	}
}

// A name the alias command would refuse, which only another build can seal,
// is not shown. Of two names set at once, the smaller device id's wins, as
// the alias command picks, and an email matches in any case.
func TestAliasesTheReportRefuses(t *testing.T) {
	st := emptyState()
	addAccount(st, "codex", "bots@corp.test", true, nil, 10)
	addAccount(st, "claude", "sam@mail.test", true, nil, 10)
	f := newFixture(t, st)
	at := now.Add(-time.Hour)
	doc := func(device string, aliases map[string]state.Alias) snapshot.Doc {
		return docWith(t, f.key, state.Config{Device: device, Aliases: aliases}, device, now, emptyState())
	}
	r := withTeam(t, f,
		doc("d-device-2", map[string]state.Alias{state.Key("codex", "bots@corp.test"): {Name: "second", At: at}}),
		doc("d-device-1", map[string]state.Alias{state.Key("codex", "bots@corp.test"): {Name: "first", At: at}}),
		doc("d-device-0", map[string]state.Alias{
			state.Key("codex", "bots@corp.test"): {Name: "bad name", At: now},
			state.Key("claude", "Sam@Mail.test"): {Name: "sammy", At: at},
		}),
	)
	if a := findTeamAccount(t, r, "codex", "bots@corp.test"); a.Name != "first" {
		t.Fatalf("bots = %q", a.Name)
	}
	if a := findTeamAccount(t, r, "claude", "sam@mail.test"); a.Name != "sammy" {
		t.Fatalf("sam = %q", a.Name)
	}
}

// Users counts the devices that spent on the account since its main window
// began, Hermes through it included, and names the busiest.
func TestUsersSinceTheWindowBegan(t *testing.T) {
	start := now.Add(-3 * 24 * time.Hour)
	st := emptyState()
	addAccount(st, "codex", "bots", true, weekQuota(now, 40), 0)
	spend(st, "codex", "bots", "/w", 50, now.Add(-2*time.Hour))
	f := newFixture(t, st)

	busy := emptyState()
	addAccount(busy, "codex", "bots", true, weekQuota(now.Add(-time.Hour), 40), 0)
	addHermes(busy, "openai-codex", "codex", "bots", 0)
	spendVia(busy, "openai-codex", "/h", "codex", "bots", 300, now.Add(-5*time.Hour))
	// Before the window began only.
	before := emptyState()
	addAccount(before, "codex", "bots", true, nil, 0)
	spend(before, "codex", "bots", "/w", 1000, start.Add(-24*time.Hour))
	// Silent since before the window began.
	gone := emptyState()
	addAccount(gone, "codex", "bots", true, nil, 0)
	spend(gone, "codex", "bots", "/w", 1000, start.Add(-2*time.Hour))

	r := withTeam(t, f,
		otherDoc(t, f.key, "d-busy-device", "build", now, busy),
		otherDoc(t, f.key, "d-before-device", "early", now, before),
		otherDoc(t, f.key, "d-gone-device", "gone", start.Add(-time.Hour), gone),
	)
	bots := findTeamAccount(t, r, "codex", "bots")
	if bots.Users != 2 || bots.Busiest == nil || *bots.Busiest != "build" {
		t.Fatalf("users = %d busiest %v", bots.Users, bots.Busiest)
	}

	// With no reading, users are the devices of the last 7 days.
	st2 := emptyState()
	addAccount(st2, "claude", "lee", false, nil, 0)
	spend(st2, "claude", "lee", "/w", 5, now.Add(-6*24*time.Hour))
	f2 := newFixture(t, st2)
	old := emptyState()
	addAccount(old, "claude", "lee", false, nil, 0)
	spend(old, "claude", "lee", "/w", 50, now.Add(-8*24*time.Hour))
	r2 := withTeam(t, f2, otherDoc(t, f2.key, "d-old-device", "oldbox", now, old))
	if s := findTeamAccount(t, r2, "claude", "lee"); s.Users != 1 || s.Busiest == nil || *s.Busiest != "thisbox" {
		t.Fatalf("lee users = %d busiest %v", s.Users, s.Busiest)
	}
}

func TestMatrix(t *testing.T) {
	start := now.Add(-3 * 24 * time.Hour)
	st := emptyState()
	addAccount(st, "claude", "ann@a.io", true, weekQuota(now, 60), 0)
	addAccount(st, "codex", "bots@a.io", true, weekQuota(now, 40), 0)
	spend(st, "claude", "ann@a.io", "/w", 3_000_000, now.Add(-time.Hour))
	spend(st, "codex", "bots@a.io", "/w", 1_000_000, now.Add(-time.Hour), start.Add(-12*time.Hour))
	f := newFixture(t, st)

	build := emptyState()
	addAccount(build, "codex", "bots@a.io", true, weekQuota(now, 40), 0)
	addHermes(build, "openai-codex", "codex", "bots@a.io", 0)
	spendVia(build, "openai-codex", "/h", "codex", "bots@a.io", 3_000_000, now.Add(-2*time.Hour))
	// A Hermes key with no subscription.
	build.Accounts[state.Key("hermes", "openrouter")] = &state.Account{Provider: "hermes", Label: "openrouter", LastSeenAt: now}
	spend(build, "hermes", "openrouter", "/h", 500_000, now.Add(-3*time.Hour))

	r := withTeam(t, f, otherDoc(t, f.key, "d-build-device", "build", now, build))
	mx := r.Team.Matrix
	var cols []string
	for _, c := range mx.Columns {
		cols = append(cols, c.Provider+":"+c.Name)
	}
	if want := []string{"claude:ann", "codex:bots", "hermes:hermes"}; !reflect.DeepEqual(cols, want) {
		t.Fatalf("columns = %v", cols)
	}
	if c := mx.Columns[2]; !c.NoQuota || c.Label != "" || c.Usage.Week != 500_000 || c.Percent != nil {
		t.Fatalf("no quota column = %+v", c)
	}
	if c := mx.Columns[1]; c.Percent == nil || *c.Percent != 40 || c.State != StateTight || c.Usage.Week != 5_000_000 || c.WindowTokens != 4_000_000 {
		t.Fatalf("codex column = %+v", c)
	}
	// Rows by tokens in 7 days: build spent 3.5M, this device 5M.
	if len(mx.Rows) != 2 || mx.Rows[0].Device != "thisbox" || mx.Rows[1].Device != "build" {
		t.Fatalf("rows = %+v", mx.Rows)
	}
	this, other := mx.Rows[0], mx.Rows[1]
	if this.Usage.Week != 5_000_000 || other.Usage.Week != 3_500_000 {
		t.Fatalf("row usage = %+v %+v", this.Usage, other.Usage)
	}
	// Hermes through the bots login counts in the bots column: 3M of the
	// team's 5M in 7 days.
	if c := other.Cells[1]; c.Usage.Week != 3_000_000 || c.WindowTokens != 3_000_000 || !near(c.Share.Week, 60) || !near(c.Share.Today, 75) {
		t.Fatalf("build codex cell = %+v", c)
	}
	// This device spent 2M in 7 days, 1M of them since the window began.
	if c := this.Cells[1]; c.WindowTokens != 1_000_000 || !near(c.Share.Week, 40) || !near(c.Share.Today, 25) || c.Usage.Month != 2_000_000 {
		t.Fatalf("this codex cell = %+v", c)
	}
	if c := this.Cells[0]; !near(c.Share.Week, 100) {
		t.Fatalf("this claude cell = %+v", c)
	}
	if c := other.Cells[0]; !near(c.Share.Week, 0) {
		t.Fatalf("build claude cell = %+v", c)
	}
	if c := other.Cells[2]; c.Usage.Week != 500_000 || !near(c.Share.Week, 100) {
		t.Fatalf("no quota cell = %+v", c)
	}
	// Of the team's 8.5M in 7 days, this device spent 5M.
	if !near(this.Share.Week, 5/8.5*100) || !near(other.Share.Week, 3.5/8.5*100) {
		t.Fatalf("row shares = %+v %+v", this.Share, other.Share)
	}
	if bots := findTeamAccount(t, r, "codex", "bots@a.io"); bots.Users != 2 || *bots.Busiest != "build" {
		t.Fatalf("bots users = %d %v", bots.Users, bots.Busiest)
	}
	if h := findTeamAccount(t, r, "hermes", "openrouter"); h.Subscription {
		t.Fatalf("hermes is a subscription: %+v", h)
	}
}

func TestSubscriptionsGoWorstFirst(t *testing.T) {
	start := now.Add(-3 * 24 * time.Hour)
	q := func(pct float64) *state.Quota {
		return &state.Quota{At: now, Windows: []snapshot.Window{week7(pct, start.Add(week))}}
	}
	st := emptyState()
	addAccount(st, "codex", "a-none", false, nil, 10)
	addAccount(st, "codex", "b-under", false, q(10), 10)
	addAccount(st, "codex", "c-ok", false, q(30), 10)
	addAccount(st, "codex", "d-ok-fuller", false, q(35), 10)
	addAccount(st, "codex", "e-over", false, q(60), 10)
	addAccount(st, "codex", "f-out", false, q(100), 10)
	addAccount(st, "codex", "g-tight", false, q(38), 10)
	r := Build(newFixture(t, st).in)
	var got []string
	for _, a := range r.Team.Providers[0].Accounts {
		got = append(got, a.Label+":"+a.State)
	}
	want := []string{"f-out:out", "e-over:over", "g-tight:tight", "d-ok-fuller:ok", "c-ok:ok", "b-under:under", "a-none:unknown"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v", got)
	}
}

func TestAttention(t *testing.T) {
	start := now.Add(-4 * 24 * time.Hour) // 4/7 of the week gone
	weekly := func(pct float64) snapshot.Window { return week7(pct, start.Add(week)) }
	st := emptyState()
	addAccount(st, "codex", "sam@mail.test", true, &state.Quota{At: now, Windows: []snapshot.Window{weekly(100)}}, 10)
	addAccount(st, "claude", "sam@mail.test", true, &state.Quota{At: now.Add(-30 * time.Hour), Windows: []snapshot.Window{
		weekly(20), {Name: "7d Fable", Percent: 100, Minutes: 10080, ResetsAt: tp(start.Add(week))},
	}}, 10)
	addAccount(st, "codex", "kim@mail.test", false, &state.Quota{At: now, Windows: []snapshot.Window{weekly(80)}}, 10)
	addAccount(st, "codex", "bots@corp.test", false, &state.Quota{At: now, Windows: []snapshot.Window{weekly(20)}}, 10)
	addAccount(st, "codex", "fine@a.io", false, &state.Quota{At: now, Windows: []snapshot.Window{weekly(50)}}, 10)
	f := newFixture(t, st)
	f.in.State.Update.Latest = "v1.2.3"

	broken := emptyState()
	broken.Sources["codex"] = state.Source{Status: "error", Error: "app-server exited without answering"}
	// A silent device's error is the one it last reported.
	quiet := emptyState()
	quiet.Sources["codex"] = state.Source{Status: "partial", Error: "codex: not logged in"}
	r := withTeam(t, f,
		otherDoc(t, f.key, "d-broken-device", "Mac.localdomain", now.Add(-10*time.Minute), broken),
		otherDoc(t, f.key, "d-quiet-device", "leebook", now.Add(-50*time.Hour), quiet),
	)
	var got []string
	for _, a := range r.Attention {
		s := a.Kind + " " + a.Provider + " " + a.Name
		if a.Window != "" {
			s += " " + a.Window
		}
		if len(a.Devices) > 0 {
			s += " " + strings.Join(a.Devices, ",")
		}
		got = append(got, strings.TrimSpace(strings.Join(strings.Fields(s), " ")))
	}
	want := []string{
		"out claude sam 7d Fable",
		"out codex sam",
		"over codex kim",
		"error Mac.localdomain",
		"silent leebook",
		"old Mac.localdomain,leebook",
		"under codex bots",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A full window stays full until it resets, so its age is no news.
	fable := r.Attention[0]
	if fable.At == nil || !fable.At.Equal(start.Add(week)) || fable.ReadingAge != 0 || fable.Account != "sam@mail.test" {
		t.Fatalf("fable = %+v", fable)
	}
	over := r.Attention[2]
	if over.Percent == nil || *over.Percent != 140 || over.At == nil || over.ResetsAt == nil || !over.At.Before(*over.ResetsAt) || over.ReadingAge != 0 {
		t.Fatalf("over = %+v", over)
	}
	if e := r.Attention[3]; e.Message != "codex: app-server exited without answering" {
		t.Fatalf("error = %+v", e)
	}
	if s := r.Attention[4]; s.At == nil || !s.At.Equal(now.Add(-50*time.Hour)) || s.Message != "codex: not logged in" {
		t.Fatalf("silent = %+v", s)
	}
	if o := r.Attention[5]; o.Message != "v1.2.3" {
		t.Fatalf("old = %+v", o)
	}
	if u := r.Attention[6]; u.Percent == nil || *u.Percent != 35 || u.ResetsAt == nil {
		t.Fatalf("under = %+v", u)
	}
	// Nothing wrong, nothing to say.
	if r := Build(newFixture(t, emptyState()).in); len(r.Attention) != 0 || r.Attention == nil {
		t.Fatalf("attention = %#v", r.Attention)
	}
}

// An OVER window at exactly 100% runs out at its reset, and comes before one
// that runs out after that reset, although it has no time of its own to run
// out.
func TestAttentionOverAtResetOrder(t *testing.T) {
	st := emptyState()
	// At 140%, with a day of its week gone: it runs out in 4 days, 2 days
	// before its own reset.
	addAccount(st, "codex", "ann@acme.dev", false, &state.Quota{At: now, Windows: []snapshot.Window{week7(20, now.Add(6*24*time.Hour))}}, 10)
	// At 100%, with half its week gone: it runs out at its reset, in 3.5
	// days.
	reset := now.Add(week / 2)
	addAccount(st, "codex", "kim@mail.test", false, &state.Quota{At: now, Windows: []snapshot.Window{week7(50, reset)}}, 10)
	r := Build(newFixture(t, st).in)
	var got []string
	for _, a := range r.Attention {
		got = append(got, a.Kind+" "+a.Account)
	}
	if want := []string{"over kim@mail.test", "over ann@acme.dev"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attention = %v, want %v", got, want)
	}
	kim, ann := r.Attention[0], r.Attention[1]
	if kim.At != nil || kim.ResetsAt == nil || !kim.ResetsAt.Equal(reset) || *kim.Percent != 100 {
		t.Fatalf("kim = %+v", kim)
	}
	if ann.At == nil || !ann.At.Equal(now.Add(4*24*time.Hour)) || *ann.Percent != 140 {
		t.Fatalf("ann = %+v", ann)
	}
	lines := pageSection(Render(r, Options{Width: 120, Loc: time.UTC}), "ATTENTION")
	want := []string{
		" OVER  codex kim@mail.test  runs out ~Sat 00:00 at this week's pace, at its reset",
		" OVER  codex ann@acme.dev   runs out ~Sat 12:00 at this week's pace, 2d before reset",
	}
	if len(lines) != 3 || !reflect.DeepEqual(lines[1:], want) {
		t.Fatalf("ATTENTION =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// The header's failing relay and update check have their errors in
// ATTENTION, as this device's, although the run that met them read every
// source and counts as a success.
func TestHeaderFailuresAreInAttention(t *testing.T) {
	st := emptyState()
	st.Relay = state.Relay{LastPushAt: now.Add(-time.Hour), Pending: true, LastError: "relay: service unavailable (HTTP 503)", LastErrorAt: now}
	st.LastError, st.LastErrorAt = st.Relay.LastError, now
	st.Update = state.Update{CheckedAt: now, Error: "cannot write beside /opt/bin/ai-usage: permission denied"}
	f := newFixture(t, st)
	r := Build(f.in)
	got := attentionList(r)
	want := []string{
		"error thisbox relay: service unavailable (HTTP 503)",
		"error thisbox update: cannot write beside /opt/bin/ai-usage: permission denied",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	page := plainText(r, Options{Width: 120, Loc: time.UTC})
	for _, s := range []string{
		"● relay failing  ● update check failed\n",
		"\n ERROR  thisbox  relay: service unavailable (HTTP 503)\n",
		"\n ERROR  thisbox  update: cannot write beside /opt/bin/ai-usage: permission denied\n",
	} {
		if !strings.Contains(page, s) {
			t.Errorf("the page lacks %q:\n%s", s, page)
		}
	}
	// Each failure turns its dot red, the color of its ERROR.
	th := NewTheme(true)
	dot := lipgloss.NewStyle().Foreground(th.Out).Render("●")
	h := Render(r, Options{Width: 120, Loc: time.UTC, Color: true, Dark: true}).Header
	for _, s := range []string{"relay failing", "update check failed"} {
		if want := dot + " " + lipgloss.NewStyle().Foreground(th.Muted).Render(s); !strings.Contains(h, want) {
			t.Errorf("the dot before %q is not red: %q", s, h)
		}
	}

	// Without a relay, or on a build that does not update, the header shows
	// no failure, and there is no error.
	f.in.RelayURL, f.in.Version = "", "dev"
	if r := Build(f.in); len(r.Attention) != 0 {
		t.Fatalf("attention = %+v", r.Attention)
	}
}

// A run a bug stopped leaves no report, so its error is not the device's; it
// is in ATTENTION as this device's all the same, while the device is
// silent too. A run that failed and reported it is named once.
func TestFailedRunIsInAttention(t *testing.T) {
	bug := "collection stopped by a bug: runtime error: invalid memory address or nil pointer dereference"
	for _, c := range []struct {
		name string
		ran  time.Duration
		want []string
		line string
	}{
		{"a bug", 15 * time.Minute, []string{"error thisbox " + bug}, " ERROR  thisbox  " + bug},
		{"a bug every run", 30 * time.Hour, []string{"error thisbox " + bug, "silent thisbox "}, " ERROR   thisbox  " + bug},
	} {
		st := emptyState()
		f := newFixture(t, st)
		ran := now.Add(-c.ran)
		st.LastRunAt, st.LastSuccessAt = ran, ran
		st.LastError, st.LastErrorAt = bug, now.Add(-time.Minute)
		f.seal(ran)
		r := Build(f.in)
		got := attentionList(r)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: attention =\n%s\nwant\n%s", c.name, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
		page := plainText(r, Options{Width: 120, Loc: time.UTC})
		for _, s := range []string{"● last run failed 1m ago", "\n" + c.line + "\n"} {
			if !strings.Contains(page, s) {
				t.Errorf("%s: the page lacks %q:\n%s", c.name, s, page)
			}
		}
	}

	st := emptyState()
	st.Sources["codex"] = state.Source{Status: "error", Error: "app-server exited without answering"}
	f := newFixture(t, st)
	st.LastSuccessAt = now.Add(-time.Hour)
	st.LastError, st.LastErrorAt = "codex: app-server exited without answering", now
	f.seal(now)
	r := Build(f.in)
	if len(r.Attention) != 1 || r.Attention[0].Message != st.LastError {
		t.Fatalf("attention = %+v", r.Attention)
	}
}

// A device's own error starts with its harness's name, so it is not named
// twice; a last run that failed after the last success is an error too.
func TestDeviceErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		d    TeamDevice
		want string
	}{
		{"named once", TeamDevice{Sources: []Source{{Provider: "codex", Status: "partial", Error: strPtr("codex: not logged in")}}}, "codex: not logged in"},
		{"named", TeamDevice{Sources: []Source{{Provider: "claude", Status: "error", Error: strPtr("2 malformed lines")}}}, "claude: 2 malformed lines"},
		{"no text", TeamDevice{Sources: []Source{{Provider: "grok", Status: "partial"}}}, "grok: partial"},
		{"skipped is fine", TeamDevice{Sources: []Source{{Provider: "grok", Status: "skipped"}}}, ""},
		{"last run failed", TeamDevice{CollectedAt: now, LastSuccessAt: tp(now.Add(-time.Hour)), LastError: strPtr("boom")}, "boom"},
		{"an old error", TeamDevice{CollectedAt: now, LastSuccessAt: tp(now), LastError: strPtr("boom")}, ""},
	} {
		got := ""
		if e := deviceError(c.d); e != nil {
			got = *e
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// Another device's link is not on the wire: it is the one account of the
// linked provider on that doc with the reading the Hermes account borrowed.
func TestTeamLinkFromTheWire(t *testing.T) {
	f := newFixture(t, emptyState())
	other := emptyState()
	addAccount(other, "codex", "bob", true, codexQuota(now.Add(-time.Hour), 60), 100)
	addAccount(other, "codex", "carl", false, codexQuota(now.Add(-2*time.Hour), 60), 10)
	addHermes(other, "openai-codex", "codex", "bob", 40)
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))

	h := findTeamAccount(t, r, "hermes", "openai-codex")
	if h.Link == nil || *h.Link != (Link{"codex", "bob"}) {
		t.Fatalf("link = %+v", h.Link)
	}
	if h.Quota == nil || h.Quota.From != "codex" || len(h.Quota.Windows) != 1 || h.Quota.Windows[0].Percent != 60 {
		t.Fatalf("hermes quota = %+v", h.Quota)
	}
	bob := findTeamAccount(t, r, "codex", "bob")
	want := []LinkedUsage{{Provider: "hermes", Label: "openai-codex", Devices: []string{"otherbox (kim)"}, Sessions: 1, Tokens: snapshot.Tokens{Input: 40, Output: 20}}}
	if !reflect.DeepEqual(bob.LinkedUsage, want) {
		t.Fatalf("linked usage = %+v", bob.LinkedUsage)
	}
	if bob.Tokens != (snapshot.Tokens{Input: 100, Output: 50}) {
		t.Fatalf("hermes tokens added to codex: %+v", bob.Tokens)
	}
	if carl := findTeamAccount(t, r, "codex", "carl"); len(carl.LinkedUsage) != 0 || carl.LinkedUsage == nil {
		t.Fatalf("carl linked usage = %#v", carl.LinkedUsage)
	}
	// In the matrix, Hermes counts under the login it spends through.
	if c := r.Team.Matrix.Columns[column(t, r.Team.Matrix, "bob")]; c.Usage.Today != 210 {
		t.Fatalf("bob's column = %+v", c)
	}
}

// A borrowed reading that matches two accounts names neither. What Hermes
// spent through each is on the wire, so it still goes to the right one, in
// the matrix too.
func TestTeamLinkThatMatchesTwoAccountsHasNoLabel(t *testing.T) {
	f := newFixture(t, emptyState())
	other := emptyState()
	q := codexQuota(now.Add(-time.Hour), 60)
	addAccount(other, "codex", "bob", true, q, 100)
	addAccount(other, "codex", "carl", false, q, 10)
	addHermes(other, "openai-codex", "codex", "bob", 40)
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))

	h := findTeamAccount(t, r, "hermes", "openai-codex")
	if h.Link == nil || *h.Link != (Link{"codex", ""}) || h.Quota == nil || h.Quota.From != "codex" {
		t.Fatalf("link = %+v, quota = %+v", h.Link, h.Quota)
	}
	if bob := findTeamAccount(t, r, "codex", "bob"); len(bob.LinkedUsage) != 1 || bob.LinkedUsage[0].Tokens != (snapshot.Tokens{Input: 40, Output: 20}) {
		t.Fatalf("bob linked usage = %+v", bob.LinkedUsage)
	}
	if carl := findTeamAccount(t, r, "codex", "carl"); len(carl.LinkedUsage) != 0 {
		t.Fatalf("carl linked usage = %+v", carl.LinkedUsage)
	}
	if cols := r.Team.Matrix.Columns; len(cols) != 2 || cols[0].Label != "bob" || cols[0].Usage.Today != 150+60 || cols[1].Usage.Today != 15 {
		t.Fatalf("columns = %+v", cols)
	}
}

// This device knows its link even when the linked account has no reading.
func TestTeamLinkOfThisDevice(t *testing.T) {
	st := emptyState()
	addAccount(st, "codex", "ann", true, nil, 100)
	addHermes(st, "openai-codex", "codex", "ann", 30)
	f := newFixture(t, st)
	r := withTeam(t, f)

	local := findAccount(t, r, "hermes", "openai-codex")
	if local.Link == nil || *local.Link != (Link{"codex", "ann"}) || local.Quota != nil {
		t.Fatalf("local hermes = %+v", local)
	}
	ann := findAccount(t, r, "codex", "ann")
	if want := []LinkedUsage{{Provider: "hermes", Sessions: 1, Tokens: snapshot.Tokens{Input: 30, Output: 15}}}; !reflect.DeepEqual(ann.LinkedUsage, want) {
		t.Fatalf("local linked usage = %+v", ann.LinkedUsage)
	}
	h := findTeamAccount(t, r, "hermes", "openai-codex")
	if h.Link == nil || *h.Link != (Link{"codex", "ann"}) {
		t.Fatalf("team link = %+v", h.Link)
	}
	if got := findTeamAccount(t, r, "codex", "ann").LinkedUsage; len(got) != 1 || got[0].Label != "openai-codex" || got[0].Sessions != 1 {
		t.Fatalf("team linked usage = %+v", got)
	}
	if cols := r.Team.Matrix.Columns; len(cols) != 1 || cols[0].Usage.Today != 150+45 {
		t.Fatalf("columns = %+v", cols)
	}
}

func TestTeamLinkedUsageAddsUpAcrossDevices(t *testing.T) {
	f := newFixture(t, emptyState())
	var docs []snapshot.Doc
	for i, host := range []string{"abox", "bbox"} {
		st := emptyState()
		addAccount(st, "codex", "bob", true, codexQuota(now.Add(-time.Duration(i+1)*time.Hour), 50), 100)
		addHermes(st, "openai-codex", "codex", "bob", int64(10*(i+1)))
		docs = append(docs, otherDoc(t, f.key, "d-"+host+"-device", host, now, st))
	}
	r := withTeam(t, f, docs...)
	got := findTeamAccount(t, r, "codex", "bob").LinkedUsage
	want := []LinkedUsage{{Provider: "hermes", Label: "openai-codex", Devices: []string{"abox (kim)", "bbox (kim)"}, Sessions: 2, Tokens: snapshot.Tokens{Input: 30, Output: 15}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("linked usage = %+v", got)
	}
}

// A device whose Hermes homes bill one route through two logins credits
// each with what went through it, not with all of Hermes' tokens.
func TestTeamLinkedUsageSplitsByLogin(t *testing.T) {
	f := newFixture(t, emptyState())
	other := emptyState()
	addAccount(other, "codex", "bots", false, codexQuota(now.Add(-time.Hour), 40), 0)
	addAccount(other, "codex", "sam", true, codexQuota(now.Add(-2*time.Hour), 5), 0)
	addHermes(other, "openai-codex", "codex", "bots", 400)
	other.Sessions[state.Key("hermes", "own")] = &state.Session{
		Provider: "hermes", Project: "/work/own", Updated: now.Add(-time.Hour),
		By:  map[string]snapshot.Tokens{"openai-codex": {Input: 20}},
		Via: map[string]snapshot.Tokens{state.Key("codex", "sam"): {Input: 20}},
	}
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))
	for label, want := range map[string]snapshot.Tokens{"bots": {Input: 400, Output: 200}, "sam": {Input: 20}} {
		got := findTeamAccount(t, r, "codex", label).LinkedUsage
		if len(got) != 1 || got[0].Tokens != want || got[0].Sessions != 1 {
			t.Fatalf("%s linked usage = %+v", label, got)
		}
	}
}

// In the matrix and in USERS too, each login counts what went through it.
// The snapshot does not split Hermes' days by login, so they go by the
// same proportion.
func TestMatrixSplitsHermesByLogin(t *testing.T) {
	f := newFixture(t, emptyState())
	other := emptyState()
	addAccount(other, "codex", "bots@acme.dev", false, weekQuota(now, 40), 0)
	addAccount(other, "codex", "sam@mail.test", true, weekQuota(now, 10), 0)
	addHermes(other, "openai-codex", "codex", "bots@acme.dev", 0)
	spendVia(other, "openai-codex", "/bots", "codex", "bots@acme.dev", 4_000_000, now.Add(-2*time.Hour))
	spendVia(other, "openai-codex", "/sam", "codex", "sam@mail.test", 1_000_000, now.Add(-3*time.Hour))
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))

	if h := findTeamAccount(t, r, "hermes", "openai-codex"); h.Link == nil || h.Link.Label != "bots@acme.dev" {
		t.Fatalf("link = %+v", h.Link)
	}
	mx := r.Team.Matrix
	if len(mx.Rows) != 2 || mx.Rows[0].Device != "otherbox" || mx.Rows[0].Usage.Week != 5_000_000 {
		t.Fatalf("rows = %+v", mx.Rows)
	}
	for label, want := range map[string]struct {
		tokens int64
		share  float64
	}{"bots@acme.dev": {4_000_000, 100}, "sam@mail.test": {1_000_000, 100}} {
		col := column(t, mx, label)
		if c := mx.Columns[col]; c.Usage.Week != want.tokens || c.WindowTokens != want.tokens {
			t.Fatalf("%s column = %+v", label, c)
		}
		if c := mx.Rows[0].Cells[col]; c.Usage.Week != want.tokens || !near(c.Share.Week, want.share) {
			t.Fatalf("%s cell = %+v", label, c)
		}
	}
	if sam := findTeamAccount(t, r, "codex", "sam@mail.test"); sam.Users != 1 || sam.Busiest == nil || *sam.Busiest != "otherbox" {
		t.Fatalf("sam users = %d %v", sam.Users, sam.Busiest)
	}
	if got := shares(10, []int64{1, 1, 1}); !reflect.DeepEqual(got, []int64{3, 4, 3}) {
		t.Fatalf("shares = %v", got)
	}
}

// LAST counts what Hermes spent through a login: a login bot containers use
// may see nothing else.
func TestLastActivityThroughHermes(t *testing.T) {
	f := newFixture(t, emptyState())
	bot := emptyState()
	addAccount(bot, "codex", "lee@corp.test", true, codexQuota(now.Add(-time.Hour), 30), 0)
	addAccount(bot, "codex", "sam@mail.test", false, nil, 0)
	spend(bot, "codex", "sam@mail.test", "/w", 10, now.Add(-3*24*time.Hour))
	addHermes(bot, "openai-codex", "codex", "lee@corp.test", 3_000_000)
	// One session through sam, older than the one through lee.
	bot.Sessions[state.Key("hermes", "sam")] = &state.Session{
		Provider: "hermes", Project: "/work/sam", Updated: now.Add(-2 * time.Hour),
		By:  map[string]snapshot.Tokens{"openai-codex": {Input: 20}},
		Via: map[string]snapshot.Tokens{state.Key("codex", "sam@mail.test"): {Input: 20}},
	}
	r := withTeam(t, f, otherDoc(t, f.key, "d-bot-device", "srv1", now, bot))

	// Hermes' newest activity on the device, through whichever login.
	for _, label := range []string{"lee@corp.test", "sam@mail.test"} {
		if a := findTeamAccount(t, r, "codex", label); a.LastActiveAt == nil || !a.LastActiveAt.Equal(now.Add(-time.Hour)) {
			t.Fatalf("%s last active = %v", label, a.LastActiveAt)
		}
	}
	if d := findTeamAccount(t, r, "codex", "sam@mail.test").PerDevice[0]; d.LastActiveAt == nil || !d.LastActiveAt.Equal(now.Add(-3*24*time.Hour)) {
		t.Fatalf("sam's own last active = %v", d.LastActiveAt)
	}
}

func TestTeamPlanIsTheNewestAndPerDeviceIsByTokens(t *testing.T) {
	f := newFixture(t, emptyState())
	var docs []snapshot.Doc
	for _, d := range []struct {
		host   string
		at     time.Duration
		plan   string
		tokens int64
	}{{"cbox", -3 * time.Hour, "plus", 30}, {"abox", -2 * time.Hour, "pro", 10}, {"bbox", -time.Hour, "", 30}} {
		st := emptyState()
		addAccount(st, "codex", "bob", true, nil, d.tokens)
		st.Accounts[state.Key("codex", "bob")].Plan = d.plan
		docs = append(docs, otherDoc(t, f.key, "d-"+d.host+"-device", d.host, now.Add(d.at), st))
	}
	r := withTeam(t, f, docs...)
	bob := findTeamAccount(t, r, "codex", "bob")
	if bob.Plan == nil || *bob.Plan != "pro" {
		t.Fatalf("plan = %v", bob.Plan)
	}
	var order []string
	for _, d := range bob.PerDevice {
		order = append(order, d.Device)
	}
	if want := []string{"bbox (kim)", "cbox (kim)", "abox (kim)"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("per device = %v", order)
	}
	if d := bob.PerDevice[0]; d.DeviceID != "d-bbox-device" || !d.Current || d.Sessions != 1 || d.Tokens.Input != 30 || d.LastActiveAt == nil || d.Usage.Today != 45 {
		t.Fatalf("per device entry = %+v", d)
	}
	if bob.Current {
		t.Fatal("bob is current on this device")
	}
}

func TestWindowThatHasResetIsUnknown(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann", true, &state.Quota{At: now.Add(-8 * time.Hour), Source: "cache", Windows: []snapshot.Window{
		win("5h", 96, now.Add(-3*time.Hour)),
		win("7d", 20, now.Add(72*time.Hour)),
	}}, 0)
	addAccount(st, "codex", "bob", false, &state.Quota{At: now.Add(-3 * time.Hour), Source: "harness", Windows: []snapshot.Window{
		win("5h", 100, now.Add(-time.Hour)),
		win("7d", 95, now),
	}}, 0)
	r := Build(newFixture(t, st).in)

	ann := findAccount(t, r, "claude", "ann")
	if ann.State != StateUnder || !ann.Quota.Stale {
		t.Fatalf("ann = %s %+v", ann.State, ann.Quota)
	}
	if w := ann.Quota.Windows[0]; !w.Reset || w.State != StateUnknown || w.Percent != 96 || w.ResetsAt == nil || w.Forecast != nil {
		t.Fatalf("reset window = %+v", w)
	}
	// Every window has reset: nothing is known, nothing is invented.
	bob := findAccount(t, r, "codex", "bob")
	if bob.State != StateUnknown || len(bob.Quota.Windows) != 2 {
		t.Fatalf("bob = %s %+v", bob.State, bob.Quota)
	}
	// Past half of its week, ann will leave most of it unused.
	if len(r.Attention) != 1 || r.Attention[0].Kind != AttentionUnder || r.Attention[0].Account != "ann" || r.Attention[0].ReadingAge != 8*3600 {
		t.Fatalf("attention = %+v", r.Attention)
	}
}

// A request refused for a full window reads that window alone. The window
// stays full until it resets, however old the reading, and the 5-hour and
// weekly windows the refusal says nothing of are not known.
func TestRefusalReading(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann", true, &state.Quota{At: now.Add(-10 * time.Hour), Source: collect.RejectionSource, Windows: []snapshot.Window{
		{Name: "7d Opus", Percent: 100, Minutes: 10080, ResetsAt: tp(now.Add(72 * time.Hour))},
	}}, 0)
	addAccount(st, "claude", "kim", false, &state.Quota{At: now.Add(-10 * time.Hour), Source: collect.RejectionSource, Windows: []snapshot.Window{
		{Name: "7d", Percent: 100, Minutes: 10080, ResetsAt: tp(now.Add(48 * time.Hour))},
	}}, 0)
	addAccount(st, "codex", "bob", false, &state.Quota{At: now.Add(-time.Hour), Source: "harness", Windows: []snapshot.Window{
		{Name: "5h", Percent: 30, Minutes: 300, ResetsAt: tp(now.Add(2 * time.Hour))},
	}}, 0)
	r := Build(newFixture(t, st).in)

	unread := func(w Window, name string, main bool) bool {
		return w.Name == name && w.Unread && w.Main == main && w.State == StateUnknown && w.Forecast == nil && w.Percent == 0
	}
	for _, q := range []*Quota{findAccount(t, r, "claude", "ann").Quota, findTeamAccount(t, r, "claude", "ann").Quota} {
		if q.Stale || len(q.Windows) != 3 {
			t.Fatalf("ann's quota = %+v", q)
		}
		if w := q.Windows[0]; !unread(w, "5h", false) {
			t.Fatalf("5-hour window = %+v", w)
		}
		if w := q.Windows[1]; !unread(w, "7d", true) {
			t.Fatalf("weekly window = %+v", w)
		}
		if w := q.Windows[2]; w.Name != "7d Opus" || w.Stale || w.State != StateOut || w.Main {
			t.Fatalf("refused window = %+v", w)
		}
	}
	if a := findTeamAccount(t, r, "claude", "ann"); a.State != StateOut {
		t.Fatalf("ann = %s", a.State)
	}
	// A refused weekly window is the main one, full and not stale, and the
	// 5-hour window beside it is not known.
	for _, q := range []*Quota{findAccount(t, r, "claude", "kim").Quota, findTeamAccount(t, r, "claude", "kim").Quota} {
		if q.Stale || len(q.Windows) != 2 || !unread(q.Windows[0], "5h", false) {
			t.Fatalf("kim's quota = %+v", q)
		}
		if w := q.Windows[1]; w.Name != "7d" || w.Unread || !w.Main || w.Stale || w.State != StateOut {
			t.Fatalf("kim's weekly window = %+v", w)
		}
	}
	out := map[string]string{}
	for _, a := range r.Attention {
		if a.Kind != AttentionOut || a.ReadingAge != 0 {
			t.Fatalf("attention = %+v", r.Attention)
		}
		out[a.Account] = a.Window
	}
	// Nobody knows how full ann's weekly window is, so the matrix does not
	// say it is empty.
	if c := r.Team.Matrix.Columns[column(t, r.Team.Matrix, "ann")]; c.Percent != nil {
		t.Fatalf("ann's column = %+v", c)
	}
	if len(r.Attention) != 2 || out["ann"] != "7d Opus" || out["kim"] != "" {
		t.Fatalf("attention = %+v", r.Attention)
	}
	// Only Claude always has a 5-hour and a weekly window.
	if ws := findAccount(t, r, "codex", "bob").Quota.Windows; len(ws) != 1 || !ws[0].Main {
		t.Fatalf("bob's windows = %+v", ws)
	}
}

func TestProjectsSortByPeriod(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann", true, nil, 0)
	addAccount(st, "codex", "ann", true, nil, 0)
	spend(st, "claude", "ann", "/p/recent", 10, now.Add(-time.Hour))
	spend(st, "codex", "ann", "/p/recent", 30, now.Add(-2*time.Hour))
	spend(st, "claude", "ann", "/p/month", 100, now.Add(-20*24*time.Hour))
	spend(st, "codex", "ann", "/p/quarter", 1000, now.Add(-60*24*time.Hour))
	r := Build(newFixture(t, st).in)
	var got []string
	for _, p := range r.Projects {
		got = append(got, p.Path)
	}
	if want := []string{"/p/recent", "/p/quarter", "/p/month"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("projects by 7d = %v", got)
	}
	if p := r.Projects[0]; !reflect.DeepEqual(p.Providers, []string{"codex", "claude"}) || p.Sessions != 2 || p.Usage.Today != 40 {
		t.Fatalf("recent = %+v", p)
	}
	sortProjects(r.Projects, Month)
	if r.Projects[0].Path != "/p/month" {
		t.Fatalf("by 30d first = %s", r.Projects[0].Path)
	}
}

// A server reads dozens of Hermes homes; the status line names two and
// counts the rest.
func TestStatusFoldsManyHomes(t *testing.T) {
	st := emptyState()
	st.Sources["hermes"] = state.Source{Status: "ok", Homes: []string{"/srv/a/.hermes", "/srv/b/.hermes", "/srv/c/.hermes", "/srv/d/.hermes", "/srv/e/.hermes"}}
	st.Sources["codex"] = state.Source{Status: "ok", Homes: []string{"/srv/a/.codex", "/srv/b/.codex", "/srv/c/.codex"}}
	f := newFixture(t, st)
	status := StatusText(Build(f.in), "", Options{Width: 100, Loc: time.UTC})
	for _, want := range []string{
		"hermes  /srv/a/.hermes, /srv/b/.hermes, +3 more (ai-usage home) · no accounts yet\n",
		"codex   /srv/a/.codex, /srv/b/.codex, /srv/c/.codex · no accounts yet\n",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}
}

// A home the Claude app keeps for a session ends in .claude, but the user's
// home folder is not above it.
func TestClaudeAppHomeIsNotTheUsersHome(t *testing.T) {
	st := emptyState()
	st.Sources["claude"] = state.Source{Status: "ok", Homes: []string{"/Users/ann/Library/Application Support/Claude/local-agent-mode-sessions/a/b/local_c1/.claude"}}
	st.Sources["codex"] = state.Source{Status: "ok", Homes: []string{"/Users/ann/.codex"}}
	status := StatusText(Build(newFixture(t, st).in), "", Options{Width: 100, Loc: time.UTC})
	if want := "codex   ~/.codex · no accounts yet\n"; !strings.Contains(status, want) {
		t.Fatalf("status lacks %q:\n%s", want, status)
	}
}

// TestStatusUpdateLine: a release installed by hand that is newer than the
// last update check saw is the newest release, not an older one.
func TestStatusUpdateLine(t *testing.T) {
	for _, tc := range []struct{ latest, want string }{
		{"v1.2.3", "v1.2.3 is the newest release"},
		{"v1.2.0", "v1.2.3 is the newest release"},
		{"v1.4.0", "newest release v1.4.0"},
	} {
		st := emptyState()
		st.Update = state.Update{Latest: tc.latest}
		f := newFixture(t, st)
		status := StatusText(Build(f.in), "", Options{Width: 100, Loc: time.UTC})
		if !strings.Contains(status, tc.want) {
			t.Fatalf("latest %s: status lacks %q:\n%s", tc.latest, tc.want, status)
		}
	}
}

func TestCollectorSection(t *testing.T) {
	st := emptyState()
	st.LastError, st.LastErrorAt = "claude: 2 malformed lines", now.Add(-time.Hour)
	st.Relay = state.Relay{LastPushAt: now.Add(-20 * time.Minute), LastPullAt: now.Add(-20 * time.Minute), Pending: true, LastError: "relay unreachable: refused"}
	st.Schedule = state.Schedule{Registered: false, Error: "crontab: permission denied"}
	st.Update = state.Update{Latest: "v1.3.0", Installed: "v1.3.0"}
	st.Sources["grok"] = state.Source{Status: "skipped"}
	st.Sources["claude"] = state.Source{Status: "partial", Error: "2 malformed lines"}
	f := newFixture(t, st)
	r := Build(f.in)
	c := r.Collector
	if c.Team != f.key.Fingerprint() || c.Device != "d-this-device" || c.Version != "v1.2.3" || !c.Relay.Pending {
		t.Fatalf("collector = %+v", c)
	}
	if c.Update.Staged == nil || *c.Update.Staged != "v1.3.0" {
		t.Fatalf("staged = %v", c.Update.Staged)
	}
	// This device is on an older release than the one its check saw, and a
	// harness of it and its relay fail.
	if len(r.Attention) != 3 || r.Attention[0].Kind != AttentionError || r.Attention[0].Message != "claude: 2 malformed lines" ||
		r.Attention[1].Kind != AttentionError || r.Attention[1].Message != "relay unreachable: refused" || r.Attention[2].Kind != AttentionOld {
		t.Fatalf("attention = %+v", r.Attention)
	}
	status := StatusText(r, "/home/.config/ai-usage", Options{Width: 80, Loc: time.UTC})
	for _, want := range []string{
		"\n✕ 3 problems: relay failing, not scheduled, claude partial\n",
		"\ndirectory       ~/.config/ai-usage\n",
		"\nlast error      11:00 (1h ago): claude: 2 malformed lines\n",
		"\nrelay         ✕ https://relay.example\n                pushed 11:40 (20m ago) · pulled 11:40 (20m ago)\n" +
			"                the newest snapshot is not sent yet\n                relay unreachable: refused\n",
		"\nschedule      ✕ not registered: crontab: permission denied\n                register: ai-usage schedule install\n",
		"\nupdate        ↑ v1.3.0 is installed and runs next time\n                checked never\n",
		"\nsources       ◐ claude  no accounts yet\n                        2 malformed lines\n",
		"\n              · grok    not installed\n",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}

	// After `schedule remove`, status still says how to register again.
	saved := f.in.State.Schedule.Error
	f.in.State.Schedule.Error = "removed by `ai-usage schedule remove`"
	if status := StatusText(Build(f.in), "", Options{Width: 80, Loc: time.UTC}); !strings.Contains(status, "register: ai-usage schedule install") {
		t.Fatalf("status after schedule remove lacks the register hint:\n%s", status)
	}
	f.in.State.Schedule.Error = saved

	// Once the new version runs, the staged note goes away.
	f.in.Version = "v1.3.0"
	if r := Build(f.in); r.Collector.Update.Staged != nil {
		t.Fatalf("staged after it ran: %v", r.Collector.Update.Staged)
	}

	f.in.RelayURL = ""
	r = Build(f.in)
	if status := StatusText(r, "", Options{Loc: time.UTC}); !strings.Contains(status, "\nrelay         · not configured; the team view shows this device only\n                set one: ai-usage relay set URL\n") {
		t.Fatalf("unconfigured relay not shown:\n%s", status)
	}
}

// keys lists a JSON object's field names, and those of every element of each
// array of objects, as dotted paths.
func keys(prefix string, v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			out[prefix+k] = true
			keys(prefix+k+".", child, out)
		}
	case []any:
		for _, e := range x {
			keys(prefix, e, out)
		}
	}
}

func TestJSONFieldNamesAreStable(t *testing.T) {
	start := now.Add(-5 * 24 * time.Hour)
	st := emptyState()
	st.LastError, st.LastErrorAt = "x", now
	st.Relay.LastError = "y"
	st.Update = state.Update{CheckedAt: now, Latest: "v9.0.0", Installed: "v9.0.0", Error: "z"}
	st.Schedule.Error = "w"
	addAccount(st, "claude", "ann", true, &state.Quota{At: now, Source: "cache", Windows: []snapshot.Window{
		{Name: "5h", Percent: 30, ResetsAt: tp(now.Add(4 * time.Hour)), Minutes: 300}, week7(90, start.Add(week)),
	}}, 10)
	st.Accounts[state.Key("claude", "ann")].Plan = "max"
	addAccount(st, "codex", "bob", true, codexQuota(now.Add(-7*time.Hour), 80), 10)
	addHermes(st, "openai-codex", "codex", "bob", 5)
	f := newFixture(t, st)
	other := emptyState()
	other.Sources["codex"] = state.Source{Status: "error", Error: "e"}
	addAccount(other, "codex", "bob", true, codexQuota(now.Add(-7*time.Hour), 40), 10)
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: []snapshot.Doc{
		otherDoc(t, f.key, "d-other-device", "o", now.Add(-48*time.Hour), other),
	}}
	f.in.Config.Aliases = map[string]state.Alias{state.Key("codex", "bob"): {Name: "b", At: now}}
	f.seal(now)
	r := Build(f.in)
	if r.SchemaVersion != 4 {
		t.Fatalf("schema version %d", r.SchemaVersion)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	keys("", v, got)
	var list []string
	for k := range got {
		list = append(list, k)
	}
	sort.Strings(list)
	want := strings.Fields(jsonFields)
	if !reflect.DeepEqual(list, want) {
		gotSet, wantSet := map[string]bool{}, map[string]bool{}
		for _, k := range list {
			gotSet[k] = true
		}
		for _, k := range want {
			wantSet[k] = true
		}
		for _, k := range want {
			if !gotSet[k] {
				t.Errorf("missing field %s", k)
			}
		}
		for _, k := range list {
			if !wantSet[k] {
				t.Errorf("new field %s: add it here and to docs/json-schema.md; renaming or removing a field needs a new schema version", k)
			}
		}
	}

	// Empty lists are arrays and absent values are null, never missing.
	empty := Build(newFixture(t, emptyState()).in)
	b, _ = json.Marshal(empty)
	for _, want := range []string{`"attention":[]`, `"providers":[{`, `"accounts":[]`, `"homes":["/home/.claude"]`, `"last_error":null`, `"pulled_at":null`, `"projects":[]`, `"columns":[]`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("empty report lacks %s: %s", want, b)
		}
	}
}

// jsonFields are every field of the report, as dotted paths.
const jsonFields = `
attention attention.account attention.at attention.devices attention.kind attention.message attention.name
attention.percent attention.provider attention.reading_age_seconds attention.resets_at attention.window
collector collector.device collector.device_label collector.last_error collector.last_error_at
collector.last_run_at collector.last_success_at collector.os_user collector.relay
collector.relay.last_error collector.relay.last_pull_at collector.relay.last_push_at collector.relay.pending
collector.relay.url collector.schedule collector.schedule.error collector.schedule.foreground collector.schedule.registered
collector.team collector.update collector.update.checked_at collector.update.error
collector.update.latest collector.update.staged collector.version
generated_at
projects projects.last_active_at projects.path projects.providers projects.sessions projects.tokens
projects.tokens.cache_read projects.tokens.cache_write projects.tokens.input projects.tokens.output
projects.usage projects.usage.30d projects.usage.7d projects.usage.90d projects.usage.today
providers providers.accounts providers.accounts.current providers.accounts.days
providers.accounts.home providers.accounts.label providers.accounts.last_active_at
providers.accounts.link providers.accounts.link.label providers.accounts.link.provider
providers.accounts.linked_usage providers.accounts.linked_usage.provider providers.accounts.linked_usage.sessions
providers.accounts.linked_usage.tokens providers.accounts.linked_usage.tokens.cache_read providers.accounts.linked_usage.tokens.cache_write
providers.accounts.linked_usage.tokens.input providers.accounts.linked_usage.tokens.output providers.accounts.name providers.accounts.plan
providers.accounts.projects providers.accounts.projects.last_active_at providers.accounts.projects.path providers.accounts.projects.sessions
providers.accounts.projects.tokens providers.accounts.projects.tokens.cache_read providers.accounts.projects.tokens.cache_write
providers.accounts.projects.tokens.input providers.accounts.projects.tokens.output
providers.accounts.projects.usage providers.accounts.projects.usage.30d providers.accounts.projects.usage.7d
providers.accounts.projects.usage.90d providers.accounts.projects.usage.today
providers.accounts.quota providers.accounts.quota.age_seconds providers.accounts.quota.from providers.accounts.quota.observed_at
providers.accounts.quota.source providers.accounts.quota.stale providers.accounts.quota.windows
providers.accounts.quota.windows.forecast providers.accounts.quota.windows.forecast.elapsed
providers.accounts.quota.windows.forecast.percent providers.accounts.quota.windows.forecast.runs_out_at
providers.accounts.quota.windows.main providers.accounts.quota.windows.minutes providers.accounts.quota.windows.name
providers.accounts.quota.windows.observed_at providers.accounts.quota.windows.percent providers.accounts.quota.windows.reset
providers.accounts.quota.windows.resets_at providers.accounts.quota.windows.stale providers.accounts.quota.windows.state
providers.accounts.sessions providers.accounts.state providers.accounts.tokens providers.accounts.tokens.cache_read
providers.accounts.tokens.cache_write providers.accounts.tokens.input providers.accounts.tokens.output
providers.accounts.usage providers.accounts.usage.30d providers.accounts.usage.7d providers.accounts.usage.90d providers.accounts.usage.today
providers.error providers.homes providers.provider providers.status
schema_version
team team.devices team.devices.age_seconds team.devices.behind_since team.devices.collected_at team.devices.collector_version
team.devices.device team.devices.error team.devices.label team.devices.last_error team.devices.last_success_at
team.devices.old team.devices.os_user team.devices.silent team.devices.sources team.devices.sources.error team.devices.sources.provider
team.devices.sources.status team.devices.this_device team.devices.update_error team.devices.usage team.devices.usage.30d team.devices.usage.7d
team.devices.usage.90d team.devices.usage.today
team.latest_version
team.matrix team.matrix.columns team.matrix.columns.label team.matrix.columns.name team.matrix.columns.no_quota
team.matrix.columns.percent team.matrix.columns.provider team.matrix.columns.state team.matrix.columns.usage
team.matrix.columns.usage.30d team.matrix.columns.usage.7d team.matrix.columns.usage.90d team.matrix.columns.usage.today
team.matrix.columns.window_tokens
team.matrix.rows team.matrix.rows.cells team.matrix.rows.cells.share team.matrix.rows.cells.share.30d
team.matrix.rows.cells.share.7d team.matrix.rows.cells.share.90d team.matrix.rows.cells.share.today team.matrix.rows.cells.usage
team.matrix.rows.cells.usage.30d team.matrix.rows.cells.usage.7d team.matrix.rows.cells.usage.90d team.matrix.rows.cells.usage.today
team.matrix.rows.cells.window_tokens team.matrix.rows.device team.matrix.rows.device_id team.matrix.rows.usage
team.matrix.rows.usage.30d team.matrix.rows.usage.7d team.matrix.rows.usage.90d team.matrix.rows.usage.today
team.matrix.rows.share team.matrix.rows.share.30d team.matrix.rows.share.7d team.matrix.rows.share.90d team.matrix.rows.share.today
team.providers team.providers.accounts team.providers.accounts.alias team.providers.accounts.busiest team.providers.accounts.current
team.providers.accounts.devices team.providers.accounts.label team.providers.accounts.last_active_at
team.providers.accounts.link team.providers.accounts.link.label team.providers.accounts.link.provider
team.providers.accounts.linked_usage team.providers.accounts.linked_usage.devices team.providers.accounts.linked_usage.label
team.providers.accounts.linked_usage.provider team.providers.accounts.linked_usage.sessions team.providers.accounts.linked_usage.tokens
team.providers.accounts.linked_usage.tokens.cache_read team.providers.accounts.linked_usage.tokens.cache_write
team.providers.accounts.linked_usage.tokens.input team.providers.accounts.linked_usage.tokens.output
team.providers.accounts.name
team.providers.accounts.per_device team.providers.accounts.per_device.current team.providers.accounts.per_device.device
team.providers.accounts.per_device.device_id team.providers.accounts.per_device.last_active_at team.providers.accounts.per_device.sessions
team.providers.accounts.per_device.tokens team.providers.accounts.per_device.tokens.cache_read team.providers.accounts.per_device.tokens.cache_write
team.providers.accounts.per_device.tokens.input team.providers.accounts.per_device.tokens.output
team.providers.accounts.per_device.usage team.providers.accounts.per_device.usage.30d team.providers.accounts.per_device.usage.7d
team.providers.accounts.per_device.usage.90d team.providers.accounts.per_device.usage.today
team.providers.accounts.plan team.providers.accounts.quota
team.providers.accounts.quota.age_seconds team.providers.accounts.quota.device team.providers.accounts.quota.from team.providers.accounts.quota.observed_at
team.providers.accounts.quota.stale team.providers.accounts.quota.windows
team.providers.accounts.quota.windows.forecast team.providers.accounts.quota.windows.forecast.elapsed
team.providers.accounts.quota.windows.forecast.percent team.providers.accounts.quota.windows.forecast.runs_out_at
team.providers.accounts.quota.windows.main team.providers.accounts.quota.windows.minutes team.providers.accounts.quota.windows.name
team.providers.accounts.quota.windows.observed_at team.providers.accounts.quota.windows.percent team.providers.accounts.quota.windows.reset
team.providers.accounts.quota.windows.resets_at team.providers.accounts.quota.windows.stale team.providers.accounts.quota.windows.state
team.providers.accounts.sessions team.providers.accounts.state team.providers.accounts.subscription team.providers.accounts.tokens
team.providers.accounts.tokens.cache_read team.providers.accounts.tokens.cache_write team.providers.accounts.tokens.input
team.providers.accounts.tokens.output
team.providers.accounts.usage team.providers.accounts.usage.30d team.providers.accounts.usage.7d team.providers.accounts.usage.90d
team.providers.accounts.usage.today team.providers.accounts.users
team.providers.provider team.pulled_at
`
