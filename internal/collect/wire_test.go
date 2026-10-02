package collect

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/relay"
)

func TestExtendedWire(t *testing.T) {
	const fp, self = "a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2", "d-annbook-0001"
	now := t0
	doc := func(device, version string, age time.Duration) snapshot.Doc {
		return snapshot.Doc{Device: device, CollectorVersion: version, CollectedAt: now.Add(-age)}
	}
	cache := func(docs ...snapshot.Doc) TeamCache {
		return TeamCache{Team: fp, PulledAt: now.Add(-10 * time.Minute), Docs: docs}
	}
	current := cache(doc(self, "v0.4.0", 0), doc("d-srv1-a4c2e917", "v0.4.1", time.Hour), doc("d-sambook-0002", "v1.0.0", 0))
	for _, c := range []struct {
		name       string
		cache      TeamCache
		noExtUntil time.Time
		want       Gate
	}{
		{"all on v0.4.0 or later", current, time.Time{}, Gate{Open: true}},
		{"a device on v0.3.4", cache(doc(self, "v0.4.0", 0), doc("d-srv1-a4c2e917", "v0.3.4", 0)), time.Time{},
			Gate{HeldBack: []string{"d-srv1-a4c2e917"}, Reason: GateOld}},
		{"a device on v0.2.0, silent for 80 days", cache(doc("d-sambook-0002", "v0.2.0", 80*24*time.Hour)), time.Time{},
			Gate{HeldBack: []string{"d-sambook-0002"}, Reason: GateOld}},
		{"this device on an old release", cache(doc(self, "v0.3.4", 0)), time.Time{}, Gate{Open: true}},
		{"a development build", cache(doc("d-srv1-a4c2e917", "v9.9.9-dev", 0), doc("d-sambook-0002", "devel", 0), doc("d-mbp-0003", "v0.4.0-local+dirty", 0)), time.Time{}, Gate{Open: true}},
		// One from before v0.4.0 reports plain "dev", or the version it was
		// built with, as v0.0.0-local, which the docs then said to use.
		{"a development build of an old release", cache(doc("d-sambook-0002", "dev", 0), doc("d-srv1-a4c2e917", "v0.0.0-local", 0), doc("d-mbp-0003", "v0.3.4+dirty", 0)), time.Time{},
			Gate{HeldBack: []string{"d-mbp-0003", "d-sambook-0002", "d-srv1-a4c2e917"}, Reason: GateOld}},
		{"a read 4 hours old", TeamCache{Team: fp, PulledAt: now.Add(-4 * time.Hour)}, time.Time{}, Gate{Reason: GateStale}},
		{"a read from the future", TeamCache{Team: fp, PulledAt: now.Add(time.Minute)}, time.Time{}, Gate{Reason: GateStale}},
		{"no read yet", TeamCache{}, time.Time{}, Gate{Reason: GateTeam}},
		{"the relay refused the fields", current, now.Add(time.Hour), Gate{Reason: GateRejected, Until: now.Add(time.Hour)}},
		{"the refusal is over", current, now, Gate{Open: true}},
		{"another team", TeamCache{Team: strings.Repeat("b3", 16), PulledAt: now}, time.Time{}, Gate{Reason: GateTeam}},
	} {
		got := ExtendedWire(c.cache, fp, self, c.noExtUntil, now)
		if got.Open != c.want.Open || got.Reason != c.want.Reason || !got.Until.Equal(c.want.Until) || !slices.Equal(got.HeldBack, c.want.HeldBack) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}

	// Documents the read left out do not close it.
	unread := current
	unread.ReadError = readError(relay.PullResult{Unverified: 2, Undecodable: 1})
	if g := ExtendedWire(unread, fp, self, time.Time{}, now); !g.Open {
		t.Errorf("a read with documents left out: %+v", g)
	}

	// A device in Known and in the documents is held back once.
	both := cache(doc("d-srv1-a4c2e917", "v0.3.4", 0))
	both.Known = map[string]Known{"d-srv1-a4c2e917": {Version: "v0.3.4"}, "d-leoair-0003": {Version: "v0.3.0"}, self: {Version: "v0.2.0"}}
	if g := ExtendedWire(both, fp, self, time.Time{}, now); !slices.Equal(g.HeldBack, []string{"d-leoair-0003", "d-srv1-a4c2e917"}) {
		t.Errorf("held back = %v", g.HeldBack)
	}
}

// Known keeps a device whose document expired, so that it holds the gate
// closed when it comes back, and drops one that was forgotten.
func TestKnownOutlivesExpiredDocuments(t *testing.T) {
	const fp, self, old = "a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2", "d-annbook-0001", "d-leoair-0003"
	now := t0
	day := 24 * time.Hour
	gate := func(prev map[string]Known) (map[string]Known, Gate) {
		known := knownAfter(prev, []snapshot.Doc{{Device: self, CollectorVersion: "v0.4.0", CollectedAt: now}}, nil, self, now)
		c := TeamCache{Team: fp, PulledAt: now, Known: known}
		return known, ExtendedWire(c, fp, self, time.Time{}, now)
	}
	for _, c := range []struct {
		name               string
		first, last, since time.Duration
		kept               bool
	}{
		// Written for 10 days, so the relay kept it for 10 more.
		{"expired", 20 * day, 10 * day, 0, true},
		{"forgotten", 20 * day, 2 * time.Hour, 0, false},
		{"forgotten within the shortest the relay keeps a document", 3 * day, 6 * day, 0, false},
		{"expired after the shortest", 3 * day, 7 * day, 0, true},
		{"expired long ago", 120 * day, 91 * day, 0, false},
		// Silent 8 days before this device first read it, and forgotten
		// a day later: the relay, which stored it 60 days ago, would have
		// kept it for 51 days after its last write.
		{"silent, then forgotten", day, 9 * day, 60 * day, false},
		{"silent, then expired", day, 9 * day, 12 * day, true},
	} {
		prev := map[string]Known{old: {Version: "v0.3.4", First: now.Add(-c.first), Last: now.Add(-c.last)}}
		if c.since > 0 {
			k := prev[old]
			k.Since = now.Add(-c.since)
			prev[old] = k
		}
		known, g := gate(prev)
		if _, ok := known[old]; ok != c.kept {
			t.Errorf("%s: kept = %v, want %v", c.name, ok, c.kept)
		}
		if g.Open == c.kept {
			t.Errorf("%s: gate %+v", c.name, g)
		}
	}
}

func TestKnownAfter(t *testing.T) {
	const self = "d-annbook-0001"
	now := t0
	docs := []snapshot.Doc{
		{Device: self, CollectorVersion: "v0.4.0", CollectedAt: now},
		{Device: "d-srv1-a4c2e917", CollectorVersion: "v0.4.1", CollectedAt: now.Add(-time.Hour)},
		{Device: "d-sambook-0002", CollectorVersion: "v0.4.0", CollectedAt: now.Add(-3 * time.Hour)},
	}
	prev := map[string]Known{
		"d-srv1-a4c2e917": {Version: "v0.3.4", First: now.Add(-30 * 24 * time.Hour), Last: now.Add(-2 * time.Hour)},
		// A relay that served an older document keeps the newer Last.
		"d-sambook-0002": {Version: "v0.4.0", First: now.Add(-24 * time.Hour), Last: now.Add(-time.Hour)},
		self:             {Version: "v0.3.4", First: now.Add(-24 * time.Hour), Last: now.Add(-100 * 24 * time.Hour)},
	}
	got := knownAfter(prev, docs, nil, self, now)
	want := map[string]Known{
		"d-srv1-a4c2e917": {Version: "v0.4.1", First: now.Add(-30 * 24 * time.Hour), Last: now.Add(-time.Hour)},
		"d-sambook-0002":  {Version: "v0.4.0", First: now.Add(-24 * time.Hour), Last: now.Add(-time.Hour)},
	}
	if len(got) != len(want) {
		t.Fatalf("known = %+v", got)
	}
	for id, k := range want {
		if g := got[id]; g.Version != k.Version || !g.First.Equal(k.First) || !g.Last.Equal(k.Last) {
			t.Errorf("%s: %+v, want %+v", id, g, k)
		}
	}
	if k := knownAfter(nil, docs[1:2], nil, self, now)["d-srv1-a4c2e917"]; !k.First.Equal(now) {
		t.Errorf("a device first found: %+v", k)
	}
	since := now.Add(-40 * 24 * time.Hour)
	if k := knownAfter(nil, docs[1:2], map[string]time.Time{"d-srv1-a4c2e917": since}, self, now)["d-srv1-a4c2e917"]; !k.Since.Equal(since) {
		t.Errorf("when the relay first stored it: %+v", k)
	}
	if k := knownAfter(nil, docs[:1], nil, self, now); k != nil {
		t.Errorf("only this device: %+v", k)
	}
}

func TestAccountBurn(t *testing.T) {
	start := t0.Add(-2 * time.Hour)
	legacy := snapshot.Account{Provider: "claude", Tokens: snapshot.Tokens{Input: 100, Output: 900, CacheRead: 10000, CacheWrite: 1000},
		Days: []int64{1000, 0, 500}, Recent: []snapshot.Recent{{Window: "5h", Start: start, Tokens: 200}}}
	check := func(name string, a snapshot.Account, provider, label string, days []int64, recent map[string]int64, total int64, estimated bool) {
		t.Helper()
		d, r, n, e := AccountBurn(a, provider, label, t0)
		if !slices.Equal(d, days) || !maps.Equal(r, recent) || n != total || e != estimated {
			t.Errorf("%s: %v %v %d %v, want %v %v %d %v", name, d, r, n, e, days, recent, total, estimated)
		}
	}
	// 100 + 900·5 + 10000·0.1 + 1000·1.25 = 6850 for 1000 in+out.
	check("a release before v0.4.0", legacy, "claude", "ann@acme.dev", []int64{6850, 0, 3425}, map[string]int64{"5h": 1370}, 6850, true)
	// Hermes on a Codex login weighs output 8: 100 + 7200 + 1000 + 1000.
	check("hermes billed to codex", legacy, "hermes", "openai-codex", []int64{9300, 0, 4650}, map[string]int64{"5h": 1860}, 9300, true)

	exact := legacy
	exact.BurnDays = []int64{7000, 0, 3000}
	exact.Recent = []snapshot.Recent{{Window: "5h", Start: start, Tokens: 200, Burn: 1500}}
	check("v0.4.0", exact, "claude", "ann@acme.dev", []int64{7000, 0, 3000}, map[string]int64{"5h": 1500}, 10000, false)
	// Burn days cut short of the days: the rest weigh as the tokens do,
	// and the total is the tokens'.
	exact.BurnDays = []int64{7000}
	check("cut burn days", exact, "claude", "ann@acme.dev", []int64{7000, 0, 3425}, map[string]int64{"5h": 1500}, 6850, false)

	// Only cache: all of it on the day it was last active.
	last := t0.Add(-24 * time.Hour)
	cache := snapshot.Account{Provider: "claude", Tokens: snapshot.Tokens{CacheRead: 10000}, LastActiveAt: &last,
		Recent: []snapshot.Recent{{Window: "7d", Start: t0.Add(-72 * time.Hour)}, {Window: "5h", Start: start}}}
	check("only cache", cache, "claude", "ann@acme.dev", []int64{0, 1000}, map[string]int64{"7d": 1000}, 1000, true)
	cache.LastActiveAt = nil
	check("only cache, never active", cache, "claude", "ann@acme.dev", []int64{1000}, map[string]int64{}, 1000, true)
	check("nothing", snapshot.Account{Provider: "codex"}, "codex", "ann@acme.dev", nil, map[string]int64{}, 0, false)
}
