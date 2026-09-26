package view

import (
	"reflect"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

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
