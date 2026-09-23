package view

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

var now = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func mustKey(t *testing.T) *team.Key {
	t.Helper()
	k, err := team.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func tp(t time.Time) *time.Time { return &t }

// text is the default view at 80 columns, clocks in UTC.
func text(r Report) string { return Text(r, Options{Width: 80, Loc: time.UTC}) }

func win(name string, pct float64, reset time.Time) snapshot.Window {
	return snapshot.Window{Name: name, Percent: pct, ResetsAt: &reset}
}

func emptyState() *state.State {
	return &state.State{
		Sources:  map[string]state.Source{},
		Current:  map[string]string{},
		Accounts: map[string]*state.Account{},
		Sessions: map[string]*state.Session{},
	}
}

// addAccount puts an account with an optional quota and one session in st.
func addAccount(st *state.State, provider, label string, current bool, q *state.Quota, tokens int64) {
	st.Accounts[state.Key(provider, label)] = &state.Account{Provider: provider, Label: label, Quota: q, LastSeenAt: now}
	if current {
		st.Current[state.Key(provider, "/home/."+provider)] = label
	}
	if tokens > 0 {
		st.Sessions[state.Key(provider, label+"-s")] = &state.Session{
			Provider: provider, Project: "/work/" + label, Updated: now.Add(-time.Hour),
			By: map[string]snapshot.Tokens{label: {Input: tokens, Output: tokens / 2}},
		}
	}
}

type fixture struct {
	key *team.Key
	in  Input
}

func newFixture(t *testing.T, st *state.State) *fixture {
	t.Helper()
	key := mustKey(t)
	cfg := state.Config{Device: "d-this-device"}
	st.LastRunAt, st.LastSuccessAt = now, now
	for _, p := range collect.Providers {
		if _, ok := st.Sources[p]; !ok {
			st.Sources[p] = state.Source{Status: "ok", Homes: []string{"/home/." + p}}
		}
	}
	doc := collect.BuildDoc(st, key, cfg.Device, "thisbox", "sam", "v1.2.3", now)
	return &fixture{key: key, in: Input{
		Version: "v1.2.3", RelayURL: "https://relay.example", Config: cfg, State: st, Key: key,
		Doc: doc, Hostname: "thisbox", OSUser: "sam", Now: now,
	}}
}

func findAccount(t *testing.T, r Report, provider, label string) Account {
	t.Helper()
	for _, p := range r.Providers {
		if p.Provider != provider {
			continue
		}
		for _, a := range p.Accounts {
			if a.Label == label {
				return a
			}
		}
	}
	t.Fatalf("no %s account %q", provider, label)
	return Account{}
}

func TestHeadlineIsTheFullestWindow(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann", true, &state.Quota{At: now.Add(-5 * time.Minute), Source: "cache", Windows: []snapshot.Window{
		win("5h", 40, now.Add(2*time.Hour)),
		win("7d", 80, now.Add(72*time.Hour)),
		win("7d Opus", 12.5, now.Add(72*time.Hour)),
	}}, 1000)
	f := newFixture(t, st)
	r := Build(f.in)
	a := findAccount(t, r, "claude", "ann")
	if a.HeadlinePercent == nil || *a.HeadlinePercent != 80 || a.Level != "warning" {
		t.Fatalf("headline = %v %q", a.HeadlinePercent, a.Level)
	}
	var levels []string
	for _, w := range a.Quota.Windows {
		levels = append(levels, w.Name+":"+w.Level)
	}
	if want := []string{"5h:ok", "7d:warning", "7d Opus:ok"}; !reflect.DeepEqual(levels, want) {
		t.Fatalf("levels = %v", levels)
	}
	if a.Quota.Stale || a.Quota.AgeSeconds != 300 || a.Quota.Source != "cache" {
		t.Fatalf("quota = %+v", a.Quota)
	}

	out := text(r)
	for _, want := range []string{
		"ACCOUNTS  1 · 1 warning\n",
		"● ann      ████▊░  80% !    40%     2h  80%     3d    5m\n",
		"  └ also 7d Opus 12%, resets in 3d\n",
		"claude ● ann                                1    1.0K     500        0        0\n",
		"    /work/ann                               1    1.0K     500        0        0\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text lacks %q:\n%s", want, out)
		}
	}
}

func TestLevels(t *testing.T) {
	for _, c := range []struct {
		pct  float64
		want string
	}{{0, "ok"}, {74.9, "ok"}, {75, "warning"}, {89.9, "warning"}, {90, "critical"}, {140, "critical"}} {
		if got := level(c.pct); got != c.want {
			t.Errorf("level(%v) = %q, want %q", c.pct, got, c.want)
		}
	}
	if h, lvl := headline(nil, now); h != nil || lvl != "unknown" {
		t.Fatalf("headline(nil) = %v %q", h, lvl)
	}
	r := Report{GeneratedAt: now, Team: Team{Providers: []TeamProvider{{Provider: "codex", Accounts: []TeamAccount{{
		Label: "x", Level: "critical", HeadlinePercent: ptrF(95), Quota: &Quota{ObservedAt: now, Windows: []Window{{Name: "5h", Percent: 95, Level: "critical"}}},
	}}}}}}
	if out := text(r); !strings.Contains(out, "  x        █████▋  95% !!   95%      —    —          now\n") {
		t.Fatalf("critical mark missing:\n%s", out)
	}
}

func ptrF(f float64) *float64 { return &f }

func TestUnknownQuota(t *testing.T) {
	st := emptyState()
	addAccount(st, "codex", "bob", true, nil, 50)
	// A quota with no windows is no reading either.
	addAccount(st, "codex", "eve", false, &state.Quota{At: now}, 10)
	r := Build(newFixture(t, st).in)
	for _, label := range []string{"bob", "eve"} {
		a := findAccount(t, r, "codex", label)
		if a.HeadlinePercent != nil || a.Level != "unknown" {
			t.Fatalf("%s headline = %v %q", label, a.HeadlinePercent, a.Level)
		}
	}
	if findAccount(t, r, "codex", "bob").Quota != nil {
		t.Fatal("quota invented for bob")
	}
	out := text(r)
	for _, want := range []string{
		"● bob      ······ unknown     —           —            —\n  └ no reading yet\n",
		"○ eve      ······ unknown     —           —            —\n  └ no reading yet\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text lacks %q:\n%s", want, out)
		}
	}
}

func TestStaleAfterSixHours(t *testing.T) {
	for _, c := range []struct {
		age   time.Duration
		stale bool
	}{{StaleAfter, false}, {StaleAfter + time.Second, true}, {time.Minute, false}} {
		st := emptyState()
		addAccount(st, "claude", "ann", true, &state.Quota{At: now.Add(-c.age), Windows: []snapshot.Window{win("7d", 10, now.Add(48*time.Hour))}}, 0)
		r := Build(newFixture(t, st).in)
		q := findAccount(t, r, "claude", "ann").Quota
		if q.Stale != c.stale || q.AgeSeconds != int64(c.age.Seconds()) {
			t.Fatalf("age %v: stale=%v age=%d", c.age, q.Stale, q.AgeSeconds)
		}
		if got := strings.Contains(text(r), "~\n"); got != c.stale {
			t.Fatalf("age %v: text stale mark = %v", c.age, got)
		}
	}
}

func sampleWith(at, quotaAt time.Time, provider, label string, ws ...snapshot.Window) state.Sample {
	return state.Sample{At: at, Accounts: []state.SampleAccount{{Provider: provider, Label: label, QuotaAt: tp(quotaAt), Windows: ws}}}
}

func TestPaceFillsBeforeReset(t *testing.T) {
	reset := now.Add(10 * time.Hour)
	st := emptyState()
	addAccount(st, "claude", "ann", true, &state.Quota{At: now, Windows: []snapshot.Window{win("5h", 30, reset)}}, 0)
	f := newFixture(t, st)
	f.in.Samples = []state.Sample{
		sampleWith(now.Add(-time.Hour), now.Add(-time.Hour), "claude", "ann", win("5h", 20, reset)),
		sampleWith(now, now, "claude", "ann", win("5h", 30, reset)),
	}
	r := Build(f.in)
	w := findAccount(t, r, "claude", "ann").Quota.Windows[0]
	if w.Pace == nil || w.Pace.PerHour != 10 || w.Pace.FillsAt == nil || !w.Pace.FillsAt.Equal(now.Add(7*time.Hour)) {
		t.Fatalf("pace = %+v", w.Pace)
	}
	out := text(r)
	for _, want := range []string{
		"ACCOUNTS  1 · 1 fills before reset\n",
		"● ann      █▊░░░░  30%      30%▲   10h    —          now\n",
		"  └ ▲ 5h full in 7h (Tue 19:00) at this pace, 3h before it resets\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text lacks %q:\n%s", want, out)
		}
	}
}

func TestPaceThatDoesNotFillBeforeReset(t *testing.T) {
	reset := now.Add(2 * time.Hour)
	cur := win("5h", 30, reset)
	p := windowPace([]state.Sample{sampleWith(now.Add(-time.Hour), now.Add(-time.Hour), "claude", "ann", win("5h", 20, reset))}, "claude", "ann", cur, now)
	if p == nil || p.PerHour != 10 || p.FillsAt != nil {
		t.Fatalf("pace = %+v", p)
	}
}

func TestNoPace(t *testing.T) {
	reset := now.Add(10 * time.Hour)
	cur := win("5h", 30, reset)
	cases := []struct {
		name    string
		samples []state.Sample
		curAt   time.Time
	}{
		{"no samples", nil, now},
		{"resets differ", []state.Sample{sampleWith(now.Add(-time.Hour), now.Add(-time.Hour), "claude", "ann", win("5h", 20, reset.Add(-5*time.Hour)))}, now},
		{"readings under 10 minutes apart", []state.Sample{sampleWith(now.Add(-9*time.Minute), now.Add(-9*time.Minute), "claude", "ann", win("5h", 20, reset))}, now},
		{"cached value repeated", []state.Sample{
			sampleWith(now.Add(-time.Hour), now, "claude", "ann", win("5h", 30, reset)),
			sampleWith(now.Add(-45*time.Minute), now, "claude", "ann", win("5h", 30, reset)),
		}, now},
		{"another account", []state.Sample{sampleWith(now.Add(-time.Hour), now.Add(-time.Hour), "claude", "bob", win("5h", 20, reset))}, now},
		{"another provider", []state.Sample{sampleWith(now.Add(-time.Hour), now.Add(-time.Hour), "codex", "ann", win("5h", 20, reset))}, now},
		{"another window", []state.Sample{sampleWith(now.Add(-time.Hour), now.Add(-time.Hour), "claude", "ann", win("7d", 20, reset))}, now},
		{"older than the lookback", []state.Sample{sampleWith(now.Add(-7*time.Hour), now.Add(-7*time.Hour), "claude", "ann", win("5h", 20, reset))}, now},
		{"newer than the reading", []state.Sample{sampleWith(now.Add(time.Hour), now.Add(time.Hour), "claude", "ann", win("5h", 40, reset))}, now},
	}
	for _, c := range cases {
		if p := windowPace(c.samples, "claude", "ann", cur, c.curAt); p != nil {
			t.Errorf("%s: pace = %+v", c.name, p)
		}
	}
}

func TestPaceCountsOneObservationOnce(t *testing.T) {
	reset := now.Add(10 * time.Hour)
	// Same instant in another location: still one reading.
	east := time.FixedZone("east", 3*3600)
	samples := []state.Sample{
		sampleWith(now.Add(-time.Hour), now.Add(-time.Hour), "claude", "ann", win("5h", 20, reset)),
		sampleWith(now.Add(-30*time.Minute), now.Add(-time.Hour).In(east), "claude", "ann", win("5h", 99, reset)),
	}
	p := windowPace(samples, "claude", "ann", win("5h", 30, reset), now)
	if p == nil || p.PerHour != 10 {
		t.Fatalf("pace = %+v", p)
	}
}

func TestNoPaceForAWindowThatHasReset(t *testing.T) {
	past := now.Add(-time.Hour)
	st := emptyState()
	addAccount(st, "claude", "ann", true, &state.Quota{At: now.Add(-2 * time.Hour), Windows: []snapshot.Window{win("5h", 30, past)}}, 0)
	f := newFixture(t, st)
	f.in.Samples = []state.Sample{sampleWith(now.Add(-3*time.Hour), now.Add(-3*time.Hour), "claude", "ann", win("5h", 10, past))}
	r := Build(f.in)
	w := findAccount(t, r, "claude", "ann").Quota.Windows[0]
	if w.Pace != nil {
		t.Fatalf("pace = %+v", w.Pace)
	}
	if out := text(r); !strings.Contains(out, "● ann      ······ unknown     ?  reset    —           2h\n  └ every window reset since the reading 2h ago\n") || strings.Contains(out, "▲") {
		t.Fatalf("text:\n%s", out)
	}
}

// A window whose reset time has passed no longer says how full the account
// is: the idle overnight 5h window must not keep the account critical.
func TestWindowThatHasResetIsNotTheHeadline(t *testing.T) {
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
	if ann.HeadlinePercent == nil || *ann.HeadlinePercent != 20 || ann.Level != "ok" {
		t.Fatalf("ann headline = %v %q", ann.HeadlinePercent, ann.Level)
	}
	if w := ann.Quota.Windows[0]; w.Level != "unknown" || w.Percent != 96 || w.ResetsAt == nil {
		t.Fatalf("reset window = %+v", w)
	}
	// Every window has reset: no headline, and no percent invented.
	bob := findAccount(t, r, "codex", "bob")
	if bob.HeadlinePercent != nil || bob.Level != "unknown" || len(bob.Quota.Windows) != 2 {
		t.Fatalf("bob headline = %v %q, quota %+v", bob.HeadlinePercent, bob.Level, bob.Quota)
	}

	out := text(r)
	for _, want := range []string{
		"ACCOUNTS  2 · 1 stale · 1 unknown\n",
		"● ann      █▏░░░░  20%        ?  reset  20%     3d   8h~\n",
		"  └ ~ Claude Code updates its usage cache only while it runs\n",
		"○ bob      ······ unknown     ?  reset    ?  reset    3h\n",
		"  └ every window reset since the reading 3h ago\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "!") {
		t.Fatalf("a reset window is still marked:\n%s", out)
	}
}

func TestTeamSkipsWindowsThatHaveResetAndMarksStale(t *testing.T) {
	f := newFixture(t, emptyState())
	other := emptyState()
	addAccount(other, "codex", "bob", true, &state.Quota{At: now.Add(-72 * time.Hour), Windows: []snapshot.Window{
		win("5h", 100, now.Add(-67*time.Hour)),
	}}, 20)
	addAccount(other, "claude", "ann", true, &state.Quota{At: now.Add(-7 * time.Hour), Windows: []snapshot.Window{
		win("5h", 97, now.Add(-2*time.Hour)),
		win("7d", 40, now.Add(48*time.Hour)),
	}}, 20)
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: []snapshot.Doc{
		otherDoc(t, f.key, "d-other-device", "otherbox", now.Add(-5*time.Minute), other),
	}}
	r := Build(f.in)
	for _, p := range r.Team.Providers {
		for _, a := range p.Accounts {
			switch a.Label {
			case "bob":
				if a.HeadlinePercent != nil || a.Level != "unknown" {
					t.Fatalf("bob headline = %v %q", a.HeadlinePercent, a.Level)
				}
			case "ann":
				if a.HeadlinePercent == nil || *a.HeadlinePercent != 40 || a.Level != "ok" {
					t.Fatalf("ann headline = %v %q", a.HeadlinePercent, a.Level)
				}
			}
		}
	}
	out := text(r)
	for _, want := range []string{
		"  ann      ██▍░░░  40%        ?  reset  40%     2d   7h~  otherbox\n",
		"  └ ~ last read on otherbox 7h ago\n",
		"  bob      ······ unknown     ?  reset    —          3d~  otherbox\n",
		"  └ every window reset since the reading 3d ago\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text lacks %q:\n%s", want, out)
		}
	}
}

// otherDoc is another device's published snapshot, decoded as a pull would.
func otherDoc(t *testing.T, key *team.Key, device, host string, at time.Time, st *state.State) snapshot.Doc {
	t.Helper()
	body, err := json.Marshal(collect.BuildDoc(st, key, device, host, "kim", "v1.2.0", at))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := snapshot.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestTeamAddsTokensAndTakesNewestQuota(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann", true, &state.Quota{At: now.Add(-2 * time.Hour), Windows: []snapshot.Window{win("5h", 40, now.Add(time.Hour))}}, 1000)
	f := newFixture(t, st)

	other := emptyState()
	other.Sources["codex"] = state.Source{Status: "error"}
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
	if !r.Team.Devices[0].This || r.Team.Devices[0].Label != "thisbox" || r.Team.Devices[2].OSUser != "kim" {
		t.Fatalf("devices = %+v", r.Team.Devices)
	}
	if r.Team.Devices[2].AgeSeconds != 300 || r.Team.Devices[2].CollectorVersion != "v1.2.0" {
		t.Fatalf("other device = %+v", r.Team.Devices[2])
	}

	var ann *TeamAccount
	for _, p := range r.Team.Providers {
		for i := range p.Accounts {
			if p.Provider == "claude" && p.Accounts[i].Label == "ann" {
				ann = &p.Accounts[i]
			}
		}
	}
	if ann == nil {
		t.Fatalf("team = %+v", r.Team.Providers)
	}
	if ann.Tokens != (snapshot.Tokens{Input: 1501, Output: 750}) || ann.Sessions != 3 || len(ann.Devices) != 3 {
		t.Fatalf("ann = %+v", ann)
	}
	// Newest reading, never a sum or an average.
	if ann.HeadlinePercent == nil || *ann.HeadlinePercent != 70 || ann.Level != "ok" {
		t.Fatalf("ann headline = %v %q", ann.HeadlinePercent, ann.Level)
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

	out := text(r)
	for _, want := range []string{
		"● ann      ████▏░  70%      70%     1h    —          10m  thisbox, otherbox +1\n",
		"  bob      ······ unknown     —           —            —  otherbox\n",
		"DEVICES  3 · read 1m ago · 1 with errors · 2 outdated\n",
		"● thisbox (sam)   v1.2.3     <1m  ✓  ✓  ✓  ✓   ann\n",
		"✕ otherbox (kim)  v1.2.0      5m  ·  ✕  ·  ·   codex error; outdated; latest v…\n",
		"↓ aaabox (kim)    v1.2.0      1h  ·  ·  ·  ·   outdated; latest v1.2.3\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text lacks %q:\n%s", want, out)
		}
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
	if r.Team.PulledAt != nil || len(r.Team.Devices) != 1 || !r.Team.Devices[0].This {
		t.Fatalf("team = %+v", r.Team)
	}
	if out := text(r); strings.Contains(out, "DEVICES") || strings.Contains(out, "--tokens") {
		t.Fatalf("one device shows team parts:\n%s", out)
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
	out := text(r)
	for _, want := range []string{
		"ai-usage v1.2.3 · thisbox (sam) · team " + f.key.Fingerprint()[:8] + "…",
		"\n✓ collected just now  ✕ relay failing · last push 20m ago  ✕ not scheduled\n↑ v1.3.0 runs next time\n",
		"\n  ✕ relay: relay unreachable: refused\n  ✕ schedule: crontab: permission denied\n",
		"\n  claude ───────────────────────────────────────── no accounts · ✕ partial here\n",
		"\n  grok ─────────────────────────────────────── no accounts · not installed here\n",
		"\nclaude partial: 2 malformed lines · codex no usage yet · grok not installed · …\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text lacks %q:\n%s", want, out)
		}
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
	if r := Build(f.in); r.Collector.Update.Staged != nil || strings.Contains(text(r), "runs next time") {
		t.Fatalf("staged after it ran: %v", r.Collector.Update.Staged)
	}

	f.in.RelayURL = ""
	r = Build(f.in)
	if !strings.Contains(text(r), "  · no relay  ") {
		t.Fatalf("unconfigured relay not shown:\n%s", text(r))
	}
	if status := StatusText(r, "", Options{Loc: time.UTC}); !strings.Contains(status, "\nrelay         · not configured; the team view shows this device only\n                set one: ai-usage relay set URL\n") {
		t.Fatalf("unconfigured relay not shown:\n%s", status)
	}
}

func TestProjectsListIsCut(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann", true, nil, 0)
	for i := 0; i < topProjects+3; i++ {
		st.Sessions[state.Key("claude", string(rune('a'+i)))] = &state.Session{
			Provider: "claude", Project: "/p/" + string(rune('a'+i)), Updated: now,
			By: map[string]snapshot.Tokens{"ann": {Input: int64(100 + i)}},
		}
	}
	r := Build(newFixture(t, st).in)
	out := text(r)
	if !strings.Contains(out, "    /p/d  ") || !strings.Contains(out, "\n    + 3 more projects · ai-usage --projects\n") || strings.Contains(out, "/p/c ") {
		t.Fatalf("text:\n%s", out)
	}
	if all := Text(r, Options{Mode: Projects, Loc: time.UTC}); !strings.Contains(all, "\nPROJECTS  thisbox (sam)") || !strings.Contains(all, "    /p/a  ") || strings.Contains(all, "more project") {
		t.Fatalf("projects view:\n%s", all)
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
	reset := now.Add(10 * time.Hour)
	st := emptyState()
	st.LastError, st.LastErrorAt = "x", now
	st.Relay.LastError = "y"
	st.Update = state.Update{CheckedAt: now, Latest: "v9.0.0", Installed: "v9.0.0", Error: "z"}
	st.Schedule.Error = "w"
	addAccount(st, "claude", "ann", true, &state.Quota{At: now, Source: "cache", Windows: []snapshot.Window{{Name: "5h", Percent: 30, ResetsAt: &reset, Minutes: 300}}}, 10)
	st.Accounts[state.Key("claude", "ann")].Plan = "max"
	addAccount(st, "codex", "bob", true, codexQuota(now, 40), 10)
	addHermes(st, "openai-codex", "codex", "bob", 5)
	f := newFixture(t, st)
	f.in.Samples = []state.Sample{sampleWith(now.Add(-time.Hour), now.Add(-time.Hour), "claude", "ann", win("5h", 20, reset))}
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: []snapshot.Doc{otherDoc(t, f.key, "d-other-device", "o", now, st)}}
	r := Build(f.in)
	if r.SchemaVersion != 2 {
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
	want := []string{
		"collector", "collector.device", "collector.device_label", "collector.last_error", "collector.last_error_at",
		"collector.last_run_at", "collector.last_success_at", "collector.os_user", "collector.relay",
		"collector.relay.last_error", "collector.relay.last_pull_at", "collector.relay.last_push_at", "collector.relay.pending",
		"collector.relay.url", "collector.schedule", "collector.schedule.error", "collector.schedule.foreground", "collector.schedule.registered",
		"collector.team", "collector.update", "collector.update.checked_at", "collector.update.error",
		"collector.update.latest", "collector.update.staged", "collector.version",
		"generated_at",
		"providers", "providers.accounts", "providers.accounts.current", "providers.accounts.headline_percent",
		"providers.accounts.home", "providers.accounts.label", "providers.accounts.last_active_at", "providers.accounts.level",
		"providers.accounts.link", "providers.accounts.link.label", "providers.accounts.link.provider",
		"providers.accounts.linked_usage", "providers.accounts.linked_usage.provider", "providers.accounts.linked_usage.sessions",
		"providers.accounts.linked_usage.tokens", "providers.accounts.linked_usage.tokens.cache_read", "providers.accounts.linked_usage.tokens.cache_write",
		"providers.accounts.linked_usage.tokens.input", "providers.accounts.linked_usage.tokens.output", "providers.accounts.plan",
		"providers.accounts.projects", "providers.accounts.projects.path", "providers.accounts.projects.sessions",
		"providers.accounts.projects.tokens", "providers.accounts.projects.tokens.cache_read", "providers.accounts.projects.tokens.cache_write",
		"providers.accounts.projects.tokens.input", "providers.accounts.projects.tokens.output",
		"providers.accounts.quota", "providers.accounts.quota.age_seconds", "providers.accounts.quota.from", "providers.accounts.quota.observed_at",
		"providers.accounts.quota.source", "providers.accounts.quota.stale", "providers.accounts.quota.windows",
		"providers.accounts.quota.windows.level", "providers.accounts.quota.windows.minutes", "providers.accounts.quota.windows.name",
		"providers.accounts.quota.windows.pace", "providers.accounts.quota.windows.pace.fills_at", "providers.accounts.quota.windows.pace.percent_per_hour",
		"providers.accounts.quota.windows.percent", "providers.accounts.quota.windows.resets_at",
		"providers.accounts.sessions", "providers.accounts.tokens", "providers.accounts.tokens.cache_read",
		"providers.accounts.tokens.cache_write", "providers.accounts.tokens.input", "providers.accounts.tokens.output",
		"providers.error", "providers.homes", "providers.provider", "providers.status",
		"schema_version",
		"team", "team.devices", "team.devices.age_seconds", "team.devices.collected_at", "team.devices.collector_version",
		"team.devices.device", "team.devices.label", "team.devices.last_error", "team.devices.last_success_at",
		"team.devices.os_user", "team.devices.sources", "team.devices.sources.error", "team.devices.sources.provider",
		"team.devices.sources.status", "team.devices.this_device",
		"team.providers", "team.providers.accounts", "team.providers.accounts.devices", "team.providers.accounts.headline_percent",
		"team.providers.accounts.label", "team.providers.accounts.level",
		"team.providers.accounts.link", "team.providers.accounts.link.label", "team.providers.accounts.link.provider",
		"team.providers.accounts.linked_usage", "team.providers.accounts.linked_usage.devices", "team.providers.accounts.linked_usage.label",
		"team.providers.accounts.linked_usage.provider", "team.providers.accounts.linked_usage.sessions", "team.providers.accounts.linked_usage.tokens",
		"team.providers.accounts.linked_usage.tokens.cache_read", "team.providers.accounts.linked_usage.tokens.cache_write",
		"team.providers.accounts.linked_usage.tokens.input", "team.providers.accounts.linked_usage.tokens.output",
		"team.providers.accounts.per_device", "team.providers.accounts.per_device.current", "team.providers.accounts.per_device.device",
		"team.providers.accounts.per_device.device_id", "team.providers.accounts.per_device.last_active_at", "team.providers.accounts.per_device.sessions",
		"team.providers.accounts.per_device.tokens", "team.providers.accounts.per_device.tokens.cache_read", "team.providers.accounts.per_device.tokens.cache_write",
		"team.providers.accounts.per_device.tokens.input", "team.providers.accounts.per_device.tokens.output",
		"team.providers.accounts.plan", "team.providers.accounts.quota",
		"team.providers.accounts.quota.age_seconds", "team.providers.accounts.quota.device", "team.providers.accounts.quota.from", "team.providers.accounts.quota.observed_at",
		"team.providers.accounts.quota.stale", "team.providers.accounts.quota.windows", "team.providers.accounts.quota.windows.level",
		"team.providers.accounts.quota.windows.minutes", "team.providers.accounts.quota.windows.name", "team.providers.accounts.quota.windows.pace",
		"team.providers.accounts.quota.windows.pace.fills_at", "team.providers.accounts.quota.windows.pace.percent_per_hour",
		"team.providers.accounts.quota.windows.percent", "team.providers.accounts.quota.windows.resets_at",
		"team.providers.accounts.sessions", "team.providers.accounts.tokens", "team.providers.accounts.tokens.cache_read",
		"team.providers.accounts.tokens.cache_write", "team.providers.accounts.tokens.input", "team.providers.accounts.tokens.output",
		"team.providers.provider", "team.pulled_at",
	}
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
	for _, want := range []string{`"providers":[{`, `"accounts":[]`, `"homes":["/home/.claude"]`, `"last_error":null`, `"pulled_at":null`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("empty report lacks %s: %s", want, b)
		}
	}
}

func TestHeaderKeepsTheClockPastAOneDevicePage(t *testing.T) {
	f := newFixture(t, emptyState())
	f.in.Hostname = "runner-" + strings.Repeat("x", 45)
	head, _, _ := strings.Cut(Text(Build(f.in), Options{Width: 120, Loc: time.UTC}), "\n")
	if !strings.HasSuffix(head, now.In(time.UTC).Format("Mon 2 Jan 15:04")) || width(head) > 119 {
		t.Fatalf("header lost its clock or overran the terminal: %q", head)
	}
}

func TestSplitHostUser(t *testing.T) {
	for in, want := range map[string][2]string{
		"box (sam)":          {"box", "sam"},
		"ci (staging) (kim)": {"ci (staging)", "kim"},
		"box":                {"box", ""},
		"box (sam":           {"box (sam", ""},
	} {
		if h, u := splitHostUser(in); h != want[0] || u != want[1] {
			t.Errorf("splitHostUser(%q) = %q, %q", in, h, u)
		}
	}
}
