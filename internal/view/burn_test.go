package view

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

// legacyDoc is another device's snapshot as a release before v0.4.0 sends
// it, with no burn, on version.
func legacyDoc(t *testing.T, key *team.Key, device, host, version string) snapshot.Doc {
	t.Helper()
	st := emptyState()
	addAccount(st, "codex", "bob", true, nil, 100)
	d := otherDoc(t, key, device, host, now.Add(-5*time.Minute), st).Legacy()
	d.CollectorVersion = version
	return d
}

// TestTeamEstimatedReason: a device on a release before v0.4.0 has its burn
// estimated from input and output, and the team says which devices hold
// back weighted days and what to do, or why the relay has none.
func TestTeamEstimatedReason(t *testing.T) {
	for _, c := range []struct {
		name     string
		docs     func(k *team.Key) []snapshot.Doc
		set      func(f *fixture)
		heldBack []string
		want     string
	}{
		{"one that updates itself", func(k *team.Key) []snapshot.Doc {
			return []snapshot.Doc{legacyDoc(t, k, "d-abox-device", "abox", "v0.3.4")}
		}, nil, []string{"kim@abox"}, "kim@abox runs v0.3.4: it should update within 15 minutes"},
		{"one to reinstall", func(k *team.Key) []snapshot.Doc {
			return []snapshot.Doc{legacyDoc(t, k, "d-abox-device", "abox", "v0.2.4")}
		}, nil, []string{"kim@abox"}, "kim@abox runs v0.2.4: reinstall with install.sh"},
		{"several", func(k *team.Key) []snapshot.Doc {
			return []snapshot.Doc{
				legacyDoc(t, k, "d-abox-device", "abox", "v0.3.4"),
				legacyDoc(t, k, "d-bbox-device", "bbox", "v0.2.0"),
				legacyDoc(t, k, "d-cbox-device", "cbox", "v0.3.0"),
				legacyDoc(t, k, "d-dbox-device", "dbox", "v0.3.1"),
			}
		}, nil, []string{"kim@abox", "kim@bbox", "kim@cbox", "kim@dbox"}, "4 devices run releases before v0.4.0: reinstall kim@bbox with install.sh; the other 3 should update within 15 minutes"},
		// Only the old one is named to reinstall, though the others are busier.
		{"several, one to reinstall", func(k *team.Key) []snapshot.Doc {
			busy := func(device, host string) snapshot.Doc {
				st := emptyState()
				addAccount(st, "codex", "bob", true, nil, 5000)
				d := otherDoc(t, k, device, host, now.Add(-5*time.Minute), st).Legacy()
				d.CollectorVersion = "v0.3.4"
				return d
			}
			return []snapshot.Doc{busy("d-abox-device", "abox"), busy("d-bbox-device", "bbox"), legacyDoc(t, k, "d-cbox-device", "cbox", "v0.2.0")}
		}, nil, []string{"kim@abox", "kim@bbox", "kim@cbox"}, "3 devices run releases before v0.4.0: reinstall kim@cbox with install.sh; the other 2 should update within 15 minutes"},
		{"several, each its own way", func(k *team.Key) []snapshot.Doc {
			gone := legacyDoc(t, k, "d-abox-device", "abox", "v0.3.4")
			gone.CollectedAt = now.Add(-9 * 24 * time.Hour)
			failing := legacyDoc(t, k, "d-bbox-device", "bbox", "v0.3.4")
			failing.LastError = k.Seal(snapshot.UpdateLine + "download failed")
			away := legacyDoc(t, k, "d-cbox-device", "cbox", "v0.3.4")
			away.CollectedAt = now.Add(-2 * 24 * time.Hour)
			return []snapshot.Doc{gone, failing, away, legacyDoc(t, k, "d-dbox-device", "dbox", "v0.2.4")}
		}, nil, []string{"kim@bbox", "kim@cbox", "kim@dbox", "kim@abox"},
			"4 devices run releases before v0.4.0: reinstall kim@dbox with install.sh; forget kim@abox with ai-usage team forget-device abox-dev; run ai-usage update on kim@bbox, which has not updated; kim@cbox should update when it next runs"},
		{"several, the busiest first", func(k *team.Key) []snapshot.Doc {
			busy := emptyState()
			addAccount(busy, "codex", "bob", true, nil, 5000)
			d := otherDoc(t, k, "d-cbox-device", "cbox", now.Add(-5*time.Minute), busy).Legacy()
			d.CollectorVersion = "v0.3.0"
			return []snapshot.Doc{legacyDoc(t, k, "d-abox-device", "abox", "v0.3.4"), d}
		}, nil, []string{"kim@cbox", "kim@abox"}, "2 devices run releases before v0.4.0: they should update within 15 minutes (kim@cbox, kim@abox)"},
		{"one gone silent", func(k *team.Key) []snapshot.Doc {
			d := legacyDoc(t, k, "d-abox1234-device", "abox", "v0.3.4")
			d.CollectedAt = now.Add(-9 * 24 * time.Hour)
			return []snapshot.Doc{d}
		}, nil, []string{"kim@abox"}, "kim@abox has not reported since Aug 23: forget it with ai-usage team forget-device abox1234"},
		// A laptop off for a weekend comes back, and updates itself.
		{"one away", func(k *team.Key) []snapshot.Doc {
			d := legacyDoc(t, k, "d-abox1234-device", "abox", "v0.3.4")
			d.CollectedAt = now.Add(-2 * 24 * time.Hour)
			return []snapshot.Doc{d}
		}, nil, []string{"kim@abox"}, "kim@abox has not reported since Aug 30: it should update when it next runs"},
		{"one built from source", func(k *team.Key) []snapshot.Doc {
			return []snapshot.Doc{legacyDoc(t, k, "d-abox-device", "abox", "dev")}
		}, nil, []string{"kim@abox"}, "kim@abox runs a build from source before v0.4.0: build it again"},
		{"one whose update fails", func(k *team.Key) []snapshot.Doc {
			d := legacyDoc(t, k, "d-abox-device", "abox", "v0.3.4")
			d.LastError = k.Seal(snapshot.UpdateLine + "download failed")
			return []snapshot.Doc{d}
		}, nil, []string{"kim@abox"}, "kim@abox runs v0.3.4 and has not updated; run ai-usage update on it"},
		// As a device Homebrew installed waits for someone to run brew.
		{"two that have not updated", func(k *team.Key) []snapshot.Doc {
			var docs []snapshot.Doc
			for _, host := range []string{"abox", "bbox"} {
				d := legacyDoc(t, k, "d-"+host+"-device", host, "v0.3.4")
				d.LastError = k.Seal(snapshot.UpdateLine + "download failed")
				docs = append(docs, d)
			}
			return docs
		}, nil, []string{"kim@abox", "kim@bbox"}, "2 devices run releases before v0.4.0: run ai-usage update on kim@abox, kim@bbox, which have not updated"},
		{"refused", func(k *team.Key) []snapshot.Doc {
			return []snapshot.Doc{legacyDoc(t, k, "d-abox-device", "abox", "v0.3.4")}
		}, func(f *fixture) { f.in.State.Relay.NoExtUntil = now.Add(2 * time.Hour) }, []string{"kim@abox"},
			"the relay did not accept weighted days; trying again Sep 1 14:00"},
		{"stale", func(k *team.Key) []snapshot.Doc {
			return []snapshot.Doc{legacyDoc(t, k, "d-abox-device", "abox", "v0.3.4")}
		}, func(f *fixture) { f.in.Team.PulledAt = now.Add(-5 * time.Hour) }, []string{"kim@abox"}, "the team was last read 5h ago"},
		{"sent none yet", func(k *team.Key) []snapshot.Doc {
			return []snapshot.Doc{legacyDoc(t, k, "d-abox-device", "abox", "v1.2.0")}
		}, nil, nil, "kim@abox sent no weighted days yet: its next run sends them"},
	} {
		f := newFixture(t, emptyState())
		f.in.Loc = time.UTC
		f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: c.docs(f.key)}
		if c.set != nil {
			c.set(f)
		}
		r := Build(f.in)
		if !r.Team.Estimated || r.Team.EstimatedReason == nil || *r.Team.EstimatedReason != c.want {
			t.Errorf("%s: estimated %v, reason %v, want %q", c.name, r.Team.Estimated, deref(r.Team.EstimatedReason), c.want)
		}
		if !slices.Equal(r.Team.HeldBackBy, c.heldBack) {
			t.Errorf("%s: held back by %v, want %v", c.name, r.Team.HeldBackBy, c.heldBack)
		}
		for _, d := range r.Team.Devices {
			if d.BurnEstimated == d.This {
				t.Errorf("%s: %s burn estimated %v", c.name, d.Label, d.BurnEstimated)
			}
		}
		for _, row := range r.Team.Matrix.Rows {
			if row.BurnEstimated == (row.DeviceID == "d-this-device") {
				t.Errorf("%s: row %s burn estimated %v", c.name, row.Device, row.BurnEstimated)
			}
		}
		if bob := findTeamAccount(t, r, "codex", "bob"); !bob.BurnEstimated || bob.Burn.Quarter == 0 {
			t.Errorf("%s: bob %+v", c.name, bob)
		}
		// At 80 columns the legend keeps what to do first.
		if c.name == "several, one to reinstall" {
			if l := Render(r, Options{Width: 80, Loc: time.UTC}).Legend; !strings.Contains(l, ": reinstall kim@cbox with install.sh") {
				t.Errorf("%s: legend at 80 columns %q", c.name, l)
			}
		}
	}

	// A team whose every device sends burn has nothing estimated, even with a
	// device held back: this device's own burn is always exact.
	st := emptyState()
	addAccount(st, "codex", "bob", true, nil, 100)
	f := newFixture(t, st)
	other := emptyState()
	addAccount(other, "codex", "bob", true, nil, 100)
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(),
		Docs:  []snapshot.Doc{otherDoc(t, f.key, "d-abox-device", "abox", now, other)},
		Known: map[string]collect.Known{"d-gone-device": {Version: "v0.3.4", Last: now.Add(-20 * 24 * time.Hour)}}}
	r := Build(f.in)
	if r.Team.Estimated || r.Team.EstimatedReason != nil || len(r.Team.HeldBackBy) != 0 {
		t.Errorf("exact team: estimated %v, reason %v, held back by %v", r.Team.Estimated, deref(r.Team.EstimatedReason), r.Team.HeldBackBy)
	}
	if bob := findTeamAccount(t, r, "codex", "bob"); bob.BurnEstimated || bob.Burn.Quarter == 0 {
		t.Errorf("exact bob: %+v", bob)
	}
	// A device the cache knows but whose snapshot is gone is named by its
	// id, and each device that knows it forgets it.
	f.in.Loc = time.UTC
	f.in.Team.Docs = append(f.in.Team.Docs, legacyDoc(t, f.key, "d-bbox-device", "bbox", "v1.2.0"))
	r = Build(f.in)
	if want := "d-gone-device has not reported since Aug 12: forget it with ai-usage team forget-device gone-dev on each device"; deref(r.Team.EstimatedReason) != want {
		t.Errorf("a known device: %q, want %q", deref(r.Team.EstimatedReason), want)
	}
	// Several to forget are each named with their id.
	silent := legacyDoc(t, f.key, "d-cbox1234-device", "cbox", "v0.3.4")
	silent.CollectedAt = now.Add(-9 * 24 * time.Hour)
	f.in.Team.Docs = append(f.in.Team.Docs, silent)
	r = Build(f.in)
	if want := "2 devices run releases before v0.4.0: forget kim@cbox (cbox1234), gone-dev with ai-usage team forget-device ID on each device"; deref(r.Team.EstimatedReason) != want {
		t.Errorf("several to forget: %q, want %q", deref(r.Team.EstimatedReason), want)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// TestHermesSplitsBurnByBurn: a Hermes account on a device divides its days
// and hours by the tokens that went through each login, and its burn by the
// burn, and what it spent since a window began the same way.
func TestHermesSplitsBurnByBurn(t *testing.T) {
	a := devAccount{
		provider: "hermes", label: "own", estimated: true,
		days: []int64{100, 200}, burnDays: []int64{1000, 3000},
		hours: []int64{10, 0, 20}, burnHours: []int64{100, 0, 200},
		recent: []snapshot.Recent{{Window: "5h", Tokens: 30, Burn: 300}},
	}
	byDays := func(days, _ []int64) Usage { return usageOf(days, 0) }
	parts := a.split([]login{
		{Link{Provider: "codex", Label: "bots"}, 1, 3},
		{Link{Provider: "claude", Label: "ann@acme.dev"}, 1, 1},
		// Nothing went through this one: it gets no part.
		{Link{Provider: "codex", Label: "sam"}, 0, 5},
	}, byDays)
	if len(parts) != 2 {
		t.Fatalf("parts = %+v", parts)
	}
	for i, want := range []struct {
		days, burn, hours, burnHours []int64
		recent                       snapshot.Recent
	}{
		{[]int64{50, 100}, []int64{750, 2250}, []int64{5, 0, 10}, []int64{75, 0, 150}, snapshot.Recent{Window: "5h", Tokens: 15, Burn: 225}},
		{[]int64{50, 100}, []int64{250, 750}, []int64{5, 0, 10}, []int64{25, 0, 50}, snapshot.Recent{Window: "5h", Tokens: 15, Burn: 75}},
	} {
		p := parts[i]
		if !slices.Equal(p.days, want.days) || !slices.Equal(p.burnDays, want.burn) || len(p.recent) != 1 || p.recent[0] != want.recent || !p.estimated {
			t.Errorf("part %d = %+v", i, p)
		}
		if !slices.Equal(p.hours, want.hours) || !slices.Equal(p.burnHours, want.burnHours) {
			t.Errorf("part %d hours = %v, burn hours %v", i, p.hours, p.burnHours)
		}
		if p.burn != usageOf(want.burn, 0) {
			t.Errorf("part %d burn = %+v", i, p.burn)
		}
	}
	// Logins that burned nothing split the burn alike.
	parts = a.split([]login{{Link{Provider: "codex", Label: "bots"}, 1, 0}, {Link{Provider: "codex", Label: "sam"}, 3, 0}}, byDays)
	if !slices.Equal(parts[0].burnDays, []int64{500, 1500}) || !slices.Equal(parts[0].days, []int64{25, 50}) {
		t.Errorf("no burn: %+v", parts[0])
	}
}

// TestTeamMergesLabelsThatDifferInCase: an email in two spellings on two
// devices is one account and one column, named as first seen.
func TestTeamMergesLabelsThatDifferInCase(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "Ann@Acme.dev", true, nil, 100)
	f := newFixture(t, st)
	other := emptyState()
	addAccount(other, "claude", "ann@acme.dev", true, nil, 50)
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))
	ann := findTeamAccount(t, r, "claude", "Ann@Acme.dev")
	if len(ann.Devices) != 2 || ann.Tokens != (snapshot.Tokens{Input: 150, Output: 75}) {
		t.Errorf("ann = %+v", ann)
	}
	if n := len(r.Team.Providers[0].Accounts); n != 1 {
		t.Errorf("claude has %d accounts", n)
	}
	var labels []string
	for _, c := range r.Team.Matrix.Columns {
		labels = append(labels, c.Label)
	}
	if !slices.Equal(labels, []string{"Ann@Acme.dev"}) {
		t.Errorf("columns = %v", labels)
	}
}

// TestTeamReplacedBy: a silent device whose name and OS user a device that
// reported after it has is reinstalled as that one; a silent device with
// another user, or name, is not, and nor is the one that reports.
func TestTeamReplacedBy(t *testing.T) {
	f := newFixture(t, emptyState())
	doc := func(id, host, user string, at time.Time) snapshot.Doc {
		t.Helper()
		body, err := json.Marshal(collect.BuildDoc(emptyState(), nil, f.key, state.Config{Device: id}, host, user, "v1.2.0", at))
		if err != nil {
			t.Fatal(err)
		}
		d, err := snapshot.Decode(body)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	r := withTeam(t, f,
		doc("d-old1-device", "annbook", "ann", now.Add(-72*time.Hour)),
		doc("d-old2-device", "annbook", "ann", now.Add(-48*time.Hour)),
		doc("d-new-device", "annbook", "ann", now.Add(-5*time.Minute)),
		doc("d-user-device", "annbook", "sam", now.Add(-72*time.Hour)),
		doc("d-else-device", "srv1", "ann", now.Add(-72*time.Hour)),
	)
	want := map[string]string{"d-old1-device": "ann@annbook", "d-old2-device": "ann@annbook"}
	for _, d := range r.Team.Devices {
		if got := deref(d.ReplacedBy); got != deref(strPtrOr(want[d.Device])) {
			t.Errorf("%s replaced by %s", d.Device, got)
		}
	}
}

func strPtrOr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// TestLimitations: a release before v0.2.3 reads no Claude quota, one
// before v0.2.5 does not report its update errors, and a development build
// is taken to do both.
func TestLimitations(t *testing.T) {
	for v, want := range map[string][]string{
		"v0.2.0":      {LimitNoClaudeQuota, LimitNoUpdateErrors},
		"v0.2.3":      {LimitNoUpdateErrors},
		"v0.2.4":      {LimitNoUpdateErrors},
		"v0.2.5":      {},
		"v1.4.2":      {},
		"v0.0.0-dev":  {},
		"not-a-build": {},
	} {
		if got := limitations(v); got == nil || !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", v, got, want)
		}
	}
	f := newFixture(t, emptyState())
	d := otherDoc(t, f.key, "d-other-device", "otherbox", now, emptyState())
	d.CollectorVersion = "v0.2.2"
	r := withTeam(t, f, d)
	if got := r.Team.Devices[1].Limitations; !slices.Equal(got, []string{LimitNoClaudeQuota, LimitNoUpdateErrors}) {
		t.Errorf("otherbox: %v", got)
	}
	if got := r.Team.Devices[0].Limitations; got == nil || len(got) != 0 {
		t.Errorf("this device: %v", got)
	}
}

// TestNoReadingSaysWhy: a subscription with no reading says why when that
// is known: every device that holds a Claude account runs a release that
// reads no Claude quota, first; then a source that read without failing but
// says what went wrong; else nothing.
func TestNoReadingSaysWhy(t *testing.T) {
	f := newFixture(t, emptyState())
	holder := func(id, host, version string, src *state.Source) snapshot.Doc {
		st := emptyState()
		addAccount(st, "claude", "ann@acme.dev", true, nil, 100)
		addAccount(st, "codex", "sam@mail.test", true, nil, 100)
		if src != nil {
			st.Sources["claude"], st.Sources["codex"] = *src, *src
		}
		d := otherDoc(t, f.key, id, host, now.Add(-time.Minute), st)
		d.CollectorVersion = version
		return d
	}
	failing := &state.Source{Status: "ok", Error: "quota read timed out"}
	for _, c := range []struct {
		name          string
		docs          []snapshot.Doc
		claude, codex string
	}{
		{"old releases", []snapshot.Doc{holder("d-abox-device", "abox", "v0.2.2", failing), holder("d-bbox-device", "bbox", "v0.2.0", nil)},
			"kim@abox runs v0.2.2: reinstall with install.sh", "quota read timed out on kim@abox"},
		{"one reads quota", []snapshot.Doc{holder("d-abox-device", "abox", "v0.2.2", nil), holder("d-bbox-device", "bbox", "v1.2.0", failing)},
			"quota read timed out on kim@bbox", "quota read timed out on kim@bbox"},
		{"nothing known", []snapshot.Doc{holder("d-abox-device", "abox", "v1.2.0", nil)}, "<nil>", "<nil>"},
		// A release before v0.4.0 failed the source for it.
		{"an old Claude Code", []snapshot.Doc{holder("d-abox-device", "abox", "v0.3.4", &state.Source{Status: "partial",
			Error: "~/.claude: claude /usage: Claude Code 2.1.42 does not cache the usage; update it to 2.1.208 or later (no cache)"})},
			"kim@abox runs Claude Code 2.1.42: update Claude Code to 2.1.208 or newer", "<nil>"},
		// With another problem, which stays its error.
		{"an old Claude Code and more", []snapshot.Doc{holder("d-abox-device", "abox", "v0.3.4", &state.Source{Status: "partial",
			Error: "~/.sam/.claude: permission denied; ~/.claude: claude /usage: Claude Code 2.1.42 does not cache the usage; update it to 2.1.208 or later (no cache)"})},
			"kim@abox runs Claude Code 2.1.42: update Claude Code to 2.1.208 or newer", "<nil>"},
	} {
		r := withTeam(t, f, c.docs...)
		if got := deref(findTeamAccount(t, r, "claude", "ann@acme.dev").NoReading); got != c.claude {
			t.Errorf("%s, claude: %q, want %q", c.name, got, c.claude)
		}
		if got := deref(findTeamAccount(t, r, "codex", "sam@mail.test").NoReading); got != c.codex {
			t.Errorf("%s, codex: %q, want %q", c.name, got, c.codex)
		}
	}
	// Nor is it the device's error, though its last run's error says it.
	old := holder("d-abox-device", "abox", "v0.3.4", &state.Source{Status: "partial", Error: "claude /usage: Claude Code 2.1.42 does not cache the usage; update it to 2.1.208 or later"})
	old.LastError = f.key.Seal("claude: claude /usage: Claude Code 2.1.42 does not cache the usage; update it to 2.1.208 or later")
	old.LastSuccessAt = now.Add(-time.Hour)
	old.Sources = slices.DeleteFunc(old.Sources, func(s snapshot.Source) bool { return s.Provider != "claude" })
	if dv := withTeam(t, f, old).Team.Devices[1]; dv.Error != nil || dv.Sources[0].Status != "ok" {
		t.Errorf("an old Claude Code on a v0.3.4 device: error %q, sources %+v", deref(dv.Error), dv.Sources)
	}
	// What else failed with it still does: in the source, then in the run.
	old.Sources[0].Error = f.key.Seal("~/.sam/.claude: permission denied; claude /usage: Claude Code 2.1.42 does not cache the usage; update it to 2.1.208 or later")
	if dv := withTeam(t, f, old).Team.Devices[1]; deref(dv.Error) != "claude: ~/.sam/.claude: permission denied" || dv.Sources[0].Status != "partial" {
		t.Errorf("an old Claude Code and a home it could not read: error %q, sources %+v", deref(dv.Error), dv.Sources)
	}
	old.Sources[0].Error = f.key.Seal("claude /usage: Claude Code 2.1.42 does not cache the usage; update it to 2.1.208 or later")
	old.LastError = f.key.Seal("claude: claude /usage: Claude Code 2.1.42 does not cache the usage; update it to 2.1.208 or later; samples: disk full")
	if dv := withTeam(t, f, old).Team.Devices[1]; deref(dv.Error) != "samples: disk full" {
		t.Errorf("an old Claude Code and a run that failed: error %q", deref(dv.Error))
	}
	// A reading, however old, is no missing one.
	st := emptyState()
	addAccount(st, "codex", "sam@mail.test", true, codexQuota(now.Add(-48*time.Hour), 30), 100)
	st.Sources["codex"] = *failing
	d := otherDoc(t, f.key, "d-abox-device", "abox", now, st)
	if got := findTeamAccount(t, withTeam(t, f, d), "codex", "sam@mail.test").NoReading; got != nil {
		t.Errorf("with a reading: %q", *got)
	}
}

// TestNoReadingSaysWhyForEachHarness: what kept a device from reading a
// subscription it is logged in to becomes the subscription's reason in a few
// words of its own, the device, and what to do, whether the source failed
// or only noted it. What the harness printed, such as an address, is left
// out, and a problem that is not about the quota is no reason.
func TestNoReadingSaysWhyForEachHarness(t *testing.T) {
	f := newFixture(t, emptyState())
	for _, c := range []struct {
		provider, status, err, want string
	}{
		{"claude", "partial", "claude /usage: no new reading: could not read the usage (Claude Code 2.1.250, no cache)",
			"Claude Code could not read the usage on abox: run /usage in claude to see why"},
		{"claude", "ok", "claude /usage: no new reading: this Claude Code cannot read the usage; update it (Claude Code 2.1.150, no cache)",
			"this Claude Code cannot read the usage on abox: update it"},
		{"claude", "ok", "claude: ~/bin/claude is Claude Code 2.1.150 (Homebrew) but your sessions run 2.1.250: update Claude Code or put the newer one first on PATH",
			"~/bin/claude is Claude Code 2.1.150 (Homebrew) but your sessions run 2.1.250 on abox: update Claude Code or put the newer one first on PATH"},
		{"claude", "ok", "claude: update Claude Code to 2.1.208 or newer (~/bin/claude is 2.1.150, Homebrew)",
			"update Claude Code to 2.1.208 or newer (~/bin/claude is 2.1.150, Homebrew) on abox"},
		{"claude", "partial", "claude /usage: no answer in time (Claude Code 2.1.250, cache 3h old)", "claude /usage timed out on abox"},
		{"claude", "partial", "claude /usage: exit status 1 (Claude Code 2.1.250, no cache): connect ECONNREFUSED 192.0.2.7:443", "claude /usage failed on abox"},
		{"claude", "error", "claude binary not found; account unknown", "claude not found on abox: install Claude Code or put it on PATH"},
		{"codex", "error", "codex initialize: app-server exited without answering: error: unrecognized subcommand 'app-server'",
			"this codex cannot read its limits on abox: update codex"},
		{"codex", "error", "codex account/read: Method not found", "this codex cannot read its limits on abox: update codex"},
		{"codex", "error", "codex app-server: fork/exec /opt/codex/bin/codex: permission denied", "codex did not start on abox: reinstall it"},
		{"codex", "partial", "~/.codex-work: codex account/rateLimits/read: no answer in time", "codex timed out on abox"},
		{"codex", "error", "codex account/rateLimits/read: unexpected status 401 Unauthorized", "codex's login was refused on abox: run codex login"},
		{"codex", "error", "codex account/rateLimits/read: request to 203.0.113.9 failed", "codex could not read its limits on abox"},
		{"codex", "error", "codex binary not found; account unknown", "codex not found on abox: install it or put it on PATH"},
		{"codex", "partial", "read ~/.codex: permission denied; 3 malformed lines", ""},
		{"grok", "ok", "grok has logged no credit usage yet", "no credit usage logged yet on abox"},
		{"grok", "partial", "grok log names no signed-in user; account unknown", "grok names no signed-in user on abox: run grok and sign in"},
	} {
		st := emptyState()
		addAccount(st, c.provider, "ann@acme.dev", true, nil, 100)
		st.Sources[c.provider] = state.Source{Status: c.status, Error: c.err}
		r := withTeam(t, f, otherDoc(t, f.key, "d-abox-device", "abox", now.Add(-time.Minute), st))
		if got := deref(findTeamAccount(t, r, c.provider, "ann@acme.dev").NoReading); got != cmp.Or(c.want, "<nil>") {
			t.Errorf("%s %q: %q, want %q", c.provider, c.err, got, c.want)
		}
	}
	// A reason is bounded, and keeps what to do whole.
	long := strings.Repeat("x", 300)
	why := quotaReason(Source{Provider: "claude", Status: "ok", Error: strPtr("claude: " + long + " is Claude Code 2.1.150 but your sessions run 2.1.250: update Claude Code or put the newer one first on PATH")}, "abox")
	if len(why) > maxReason || !strings.HasSuffix(why, " on abox: update Claude Code or put the newer one first on PATH") {
		t.Errorf("a long reason: %q", why)
	}
}

// TestNoReadingInTheTeam: a subscription's reason comes from a device it is
// logged in to, this device first, and is shown in place of its bar and in
// ATTENTION; a device that holds the account but is logged in to another
// says nothing of it.
func TestNoReadingInTheTeam(t *testing.T) {
	here := emptyState()
	addAccount(here, "codex", "sam@mail.test", false, nil, 100)
	here.Sources["codex"] = state.Source{Status: "error", Error: "codex initialize: app-server exited without answering", Homes: []string{"/home/.codex"}}
	f := newFixture(t, here)
	teammate := func(err string) snapshot.Doc {
		st := emptyState()
		addAccount(st, "codex", "sam@mail.test", true, nil, 100)
		st.Sources["codex"] = state.Source{Status: "error", Error: err}
		return otherDoc(t, f.key, "d-abox-device", "abox", now.Add(-time.Minute), st)
	}
	r := withTeam(t, f, teammate("codex account/rateLimits/read: no answer in time"))
	if got := deref(findTeamAccount(t, r, "codex", "sam@mail.test").NoReading); got != "codex timed out on abox" {
		t.Errorf("logged in on a teammate only: %q", got)
	}
	f.in.State.Current[state.Key("codex", "/home/.codex")] = "sam@mail.test"
	f.seal(now)
	r = withTeam(t, f, teammate("codex account/rateLimits/read: no answer in time"))
	want := "this codex cannot read its limits on thisbox: update codex"
	if got := deref(findTeamAccount(t, r, "codex", "sam@mail.test").NoReading); got != want {
		t.Errorf("logged in here too: %q", got)
	}
	if got := attentionList(r); !slices.Contains(got, "no_reading  "+want) {
		t.Errorf("ATTENTION = %q", got)
	}
	// In place of the bar, what to do is kept first.
	row := bodyLine(t, Render(r, Options{Width: 120, Loc: time.UTC}), "SUBSCRIPTIONS", func(l string) bool { return strings.HasPrefix(l, "● sam@") })
	if want := "● sam@mail.test  update codex                 ?"; !strings.HasPrefix(sgr.ReplaceAllString(row, ""), want) {
		t.Errorf("SUBSCRIPTIONS row: %q, want %q", sgr.ReplaceAllString(row, ""), want)
	}
}

// The unknown account's tokens say why no login was known for them, and
// what to do, as the device whose harness answered that nobody is logged in
// says; nothing when no device says so.
func TestUnknownAccountSaysWhy(t *testing.T) {
	f := newFixture(t, emptyState())
	f.in.Loc = time.UTC
	holder := func(id, host string, src *state.Source) snapshot.Doc {
		st := emptyState()
		addAccount(st, "claude", collect.UnknownAccount, false, nil, 100)
		addAccount(st, "codex", collect.UnknownAccount, false, nil, 100)
		if src != nil {
			st.Sources["claude"], st.Sources["codex"] = *src, *src
		}
		return otherDoc(t, f.key, id, host, now.Add(-time.Minute), st)
	}
	loggedOut := func(status string, errs ...string) *state.Source {
		return &state.Source{Status: status, Error: strings.Join(errs, "; ")}
	}
	for _, c := range []struct {
		name          string
		docs          []snapshot.Doc
		claude, codex string
	}{
		{"one home", []snapshot.Doc{holder("d-abox-device", "abox", loggedOut("error", "claude: not logged in", "codex: not logged in"))},
			"not logged in on kim@abox: run claude and log in", "not logged in on kim@abox: run codex login"},
		// The home is named where the device has several.
		{"several homes", []snapshot.Doc{holder("d-abox-device", "abox", loggedOut("partial",
			"~/.ann/.claude: permission denied", "~/.claude-work: claude: not logged in", "~/.codex-work: codex: not logged in"))},
			"not logged in on kim@abox (~/.claude-work): run claude and log in", "not logged in on kim@abox (~/.codex-work): run codex login"},
		// The first device to say so.
		{"two devices", []snapshot.Doc{holder("d-abox-device", "abox", nil), holder("d-bbox-device", "bbox", loggedOut("error", "claude: not logged in"))},
			"not logged in on kim@bbox: run claude and log in", "<nil>"},
		// Another problem is no reason to guess at.
		{"another problem", []snapshot.Doc{holder("d-abox-device", "abox", loggedOut("error", "claude initialize: no answer in time", "codex: app-server exited"))},
			"<nil>", "<nil>"},
		{"nothing known", []snapshot.Doc{holder("d-abox-device", "abox", nil)}, "<nil>", "<nil>"},
	} {
		r := withTeam(t, f, c.docs...)
		if got := deref(findTeamAccount(t, r, "claude", collect.UnknownAccount).NoReading); got != c.claude {
			t.Errorf("%s, claude: %q, want %q", c.name, got, c.claude)
		}
		if got := deref(findTeamAccount(t, r, "codex", collect.UnknownAccount).NoReading); got != c.codex {
			t.Errorf("%s, codex: %q, want %q", c.name, got, c.codex)
		}
	}
	// ATTENTION says it, as NO READING, and so does the JSON.
	r := withTeam(t, f, holder("d-abox-device", "abox", loggedOut("error", "claude: not logged in")))
	var got []string
	for _, a := range r.Attention {
		if a.Kind == AttentionNoReading {
			got = append(got, a.Subject+" | "+a.Text)
		}
	}
	if want := []string{"claude unknown | not logged in on kim@abox: run claude and log in"}; !slices.Equal(got, want) {
		t.Errorf("NO READING = %q, want %q", got, want)
	}
	lines := strings.Join(pageSection(Render(r, Options{Width: 80, Loc: time.UTC, Interactive: true}), "ATTENTION"), "\n")
	if !strings.Contains(lines, " on kim@abox: run claude and log in") {
		t.Errorf("ATTENTION at 80:\n%s", lines)
	}
	// A short line keeps where to log in, and what to do.
	r = withTeam(t, f, holder("d-abox-device", "abox", loggedOut("partial", "~/.ann/.claude: permission denied", "~/.claude-work: claude: not logged in")))
	for w, want := range map[int]string{
		100: "  not logged in on kim@abox (~/.claude-work): run claude and log in",
		80:  "  on kim@abox (~/.claude…: run claude and log in",
	} {
		lines := pageSection(Render(r, Options{Width: w, Loc: time.UTC, Interactive: true}), "ATTENTION")
		if i := slices.IndexFunc(lines, func(l string) bool { return strings.HasSuffix(l, want) }); i < 0 {
			t.Errorf("ATTENTION at %d:\n%s", w, strings.Join(lines, "\n"))
		}
	}
}

// TestGap: the longest time in the last 48 hours a device did not report,
// until now, when that is over an hour.
func TestGap(t *testing.T) {
	m := func(n int) time.Time { return now.Add(time.Duration(n) * time.Minute) }
	// This device read the team every 20 minutes over the last 12 hours.
	var reads []time.Time
	for n := -720; n <= 0; n += 20 {
		reads = append(reads, m(n))
	}
	for _, c := range []struct {
		name      string
		seen      []time.Time
		reads     []time.Time
		collected time.Time
		own       bool
		want      *Gap
	}{
		{"none seen", nil, reads, m(-5), false, nil},
		{"a gap", []time.Time{m(-600), m(-585), m(-120)}, reads, m(-5), false, &Gap{Seconds: 445 * 60, At: m(-585)}},
		{"until now", []time.Time{m(-240)}, reads, m(-180), false, &Gap{Seconds: 180 * 60, At: m(-180)}},
		{"within an hour", []time.Time{m(-100), m(-45)}, reads, m(-5), false, nil},
		{"older than 48h", []time.Time{m(-60 * 60), m(-60)}, reads, m(-5), false, nil},
		// This device read nothing for 9 hours, so the others' reports
		// then are unknown, not missing.
		{"no reads between", []time.Time{m(-600), m(-585), m(-45)}, []time.Time{m(-590), m(-40), m(0)}, m(-5), false, nil},
		{"no reads at all", []time.Time{m(-600), m(-585)}, nil, m(-5), false, nil},
		// This device knows when it ran.
		{"own gap", []time.Time{m(-600), m(-585), m(-45)}, nil, m(-5), true, &Gap{Seconds: 540 * 60, At: m(-585)}},
	} {
		got := gapOf(c.seen, c.reads, c.collected, now, c.own)
		if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	f := newFixture(t, emptyState())
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(),
		Docs:  []snapshot.Doc{otherDoc(t, f.key, "d-other-device", "otherbox", m(-5), emptyState())},
		Seen:  map[string][]time.Time{"d-other-device": {m(-600), m(-585), m(-120)}},
		Reads: reads}
	if g := Build(f.in).Team.Devices[1].Gap; g == nil || g.Seconds != 445*60 {
		t.Errorf("otherbox gap %+v", g)
	}
}

// TestOutside: a window that rose 5 points or more over the last day while
// the team burned under a hundredth of what a point usually takes was
// filled by something outside the team.
func TestOutside(t *testing.T) {
	reset := now.Add(48 * time.Hour)
	h := func(n int) time.Time { return now.Add(time.Duration(n) * time.Hour) }
	pt := func(at time.Time, pct float64, burn int64) collect.Point {
		return collect.Point{At: at, Window: "7d", ResetsAt: &reset, Percent: pct, TeamBurn: burn}
	}
	// Ten points took 1M each, before the last day.
	usual := []collect.Point{pt(h(-30), 10, 0), pt(h(-28), 20, 10_000_000), pt(h(-26), 30, 20_000_000)}
	for _, c := range []struct {
		name string
		ps   []collect.Point
		want *Outside
	}{
		{"rose with no burn", append(slices.Clone(usual), pt(h(-20), 30, 20_000_000), pt(h(-2), 45, 20_000_500)), &Outside{Points: 15, Since: h(-20)}},
		{"rose under 5", append(slices.Clone(usual), pt(h(-20), 30, 20_000_000), pt(h(-2), 34, 20_000_000)), nil},
		{"the team burned it", append(slices.Clone(usual), pt(h(-20), 30, 20_000_000), pt(h(-2), 45, 23_000_000)), nil},
		{"no usual burn, none burned", []collect.Point{pt(h(-20), 10, 5), pt(h(-2), 20, 5)}, &Outside{Points: 10, Since: h(-20)}},
		{"no usual burn, some burned", []collect.Point{pt(h(-20), 10, 5), pt(h(-2), 20, 6)}, nil},
		{"burned after a fall", []collect.Point{pt(h(-20), 10, 5), pt(h(-10), 15, 3), pt(h(-2), 20, 4)}, nil},
		{"older than a day", []collect.Point{pt(h(-30), 10, 5), pt(h(-2), 20, 5)}, nil},
	} {
		got := outside(c.ps, now)
		if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	// A new period starts over: a reset between readings is not a rise.
	next := reset.Add(7 * 24 * time.Hour)
	ps := []collect.Point{pt(h(-20), 90, 5), {At: h(-2), Window: "7d", ResetsAt: &next, Percent: 2, TeamBurn: 5}, {At: h(-1), Window: "7d", ResetsAt: &next, Percent: 3, TeamBurn: 5}}
	if got := outside(ps, now); got != nil {
		t.Errorf("across a reset: %+v", got)
	}

	// On the report, from the team cache's history of the account.
	f := newFixture(t, emptyState())
	other := emptyState()
	addAccount(other, "codex", "sam@mail.test", true, codexQuota(now.Add(-time.Hour), 45), 100)
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(),
		Docs:    []snapshot.Doc{otherDoc(t, f.key, "d-other-device", "otherbox", now, other)},
		History: map[string][]collect.Point{collect.HistoryKey("codex", "sam@mail.test"): {pt(h(-20), 30, 7), pt(h(-2), 45, 7)}}}
	if o := findTeamAccount(t, Build(f.in), "codex", "sam@mail.test").Outside; o == nil || o.Points != 15 {
		t.Errorf("sam outside %+v", o)
	}
}

// A teammate's last error says when it happened, from v0.4.0 on, and
// ATTENTION and its note say how long ago.
func TestTeamDeviceLastErrorAt(t *testing.T) {
	f := newFixture(t, emptyState())
	at := now.Add(-3 * time.Hour)
	d := otherDoc(t, f.key, "d-abox-device", "abox", now.Add(-time.Minute), emptyState())
	d.LastError, d.LastErrorAt, d.LastSuccessAt = f.key.Seal("codex: rate limit read timed out"), &at, now.Add(-5*time.Hour)
	r := withTeam(t, f, d)
	dv := r.Team.Devices[1]
	if dv.LastErrorAt == nil || !dv.LastErrorAt.Equal(at) || deref(dv.Error) != "codex: rate limit read timed out" {
		t.Fatalf("device = %+v", dv)
	}
	var found bool
	for _, a := range r.Attention {
		if a.Kind == AttentionError && a.At != nil && a.At.Equal(at) {
			found = true
		}
	}
	if !found {
		t.Fatalf("attention = %+v", r.Attention)
	}
	lines := strings.Join(pageSection(Render(r, Options{Width: 120, Loc: time.UTC, Interactive: true}), "ATTENTION"), "\n")
	if !strings.Contains(lines, "codex: rate limit read timed out · 3h ago") {
		t.Errorf("ATTENTION:\n%s", lines)
	}
	// A release before v0.4.0 does not say when.
	if r := withTeam(t, f, d.Legacy()); r.Team.Devices[1].LastErrorAt != nil {
		t.Errorf("legacy: %v", r.Team.Devices[1].LastErrorAt)
	}
}
