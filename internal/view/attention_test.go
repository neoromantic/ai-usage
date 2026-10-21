package view

import (
	"cmp"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

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
		"error kim@Mac.localdomain",
		"silent kim@leebook",
		"old kim@Mac.localdomain,kim@leebook",
		"under codex bots",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A full window stays full until it resets, but the provider can reset
	// it early: a reading older than StaleAfter says how old it is.
	fable := r.Attention[0]
	if fable.At == nil || !fable.At.Equal(start.Add(week)) || fable.ReadingAge != 30*3600 || fable.Account != "sam@mail.test" {
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

// A window whose run-out time has passed did not run out when the team
// last spent on it before that time: it would run out then at that pace,
// and its line says so, with the reading's age, after the windows that run
// out, however fresh the reading. One the team spent on since ran out, and
// a full window is out until it resets.
func TestAttentionWouldRunOut(t *testing.T) {
	start := now.Add(-4 * 24 * time.Hour) // 4/7 of the week gone
	read := now.Add(-30 * time.Hour)
	// 80% 66 hours into the week, read 30 hours ago: 204% at that pace,
	// which runs it out 13½ hours ago, Monday 22:30.
	weekly := func(pct float64) *state.Quota {
		return &state.Quota{At: read, Source: "cache", Windows: []snapshot.Window{week7(pct, start.Add(week))}}
	}
	st := emptyState()
	// Spent an hour after the reading, and not since.
	addAccount(st, "claude", "ann@acme.dev", true, weekly(80), 0)
	spend(st, "claude", "ann@acme.dev", "/work/a", 1000, read.Add(time.Hour))
	// Spent past its run-out time: it ran out.
	addAccount(st, "codex", "bob@mail.test", false, weekly(80), 0)
	spend(st, "codex", "bob@mail.test", "/work/b", 1000, now.Add(-2*time.Hour))
	// Full, and not spent on since: out all the same.
	addAccount(st, "claude", "kim@mail.test", false, weekly(100), 0)
	// A 5-hour window read 3 hours ago, an hour and a half into it, at
	// 49%, 163% at that pace: it would have run out at 10:33, but its last
	// use came before the reading.
	five := &state.Quota{At: now.Add(-3 * time.Hour), Source: "harness", Windows: []snapshot.Window{
		{Name: "5h", Percent: 49, Minutes: 300, ResetsAt: tp(now.Add(30 * time.Minute))},
	}}
	addAccount(st, "codex", "lee@corp.test", false, five, 0)
	spend(st, "codex", "lee@corp.test", "/work/l", 1000, now.Add(-200*time.Minute))
	f := newFixture(t, st)
	f.in.Loc = time.UTC
	r := Build(f.in)

	var got []string
	for _, a := range r.Attention {
		got = append(got, a.Kind+" "+a.Name+": "+a.Text)
	}
	want := []string{
		"out kim: back Fri 12:00, in 3d · reading 1d old",
		"over bob: ran out ~Mon 22:30 at this week's pace, 3d 13h before reset · reading 1d old",
		"over ann: would run out ~Mon 22:30 at this week's pace, 3d 13h before reset · reading 1d old",
		"over lee: would run out ~Tue 10:33 at this window's pace, 1h 56m before reset · reading 3h old",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	stoppedOf := func(q *Quota) bool { return mainWindow(q.Windows).Forecast.Stopped }
	for _, c := range []struct {
		provider, label string
		stopped         bool
	}{{"claude", "ann@acme.dev", true}, {"codex", "bob@mail.test", false}, {"codex", "lee@corp.test", true}} {
		team, own := findTeamAccount(t, r, c.provider, c.label), findAccount(t, r, c.provider, c.label)
		if stoppedOf(team.Quota) != c.stopped || stoppedOf(own.Quota) != c.stopped {
			t.Errorf("%s: stopped %v, %v on this device, want %v", c.label, stoppedOf(team.Quota), stoppedOf(own.Quota), c.stopped)
		}
		if w := mainWindow(team.Quota.Windows); w.State != StateOver {
			t.Errorf("%s: %s, want over", c.label, w.State)
		}
	}
	// A report saved before stopped has it filled in.
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var old Report
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(b), `"stopped":true,`, "")), &old); err != nil {
		t.Fatal(err)
	}
	if stoppedOf(findTeamAccount(t, old, "claude", "ann@acme.dev").Quota) {
		t.Fatal("the saved report still has stopped")
	}
	Fill(&old)
	if !stoppedOf(findTeamAccount(t, old, "claude", "ann@acme.dev").Quota) || !stoppedOf(findAccount(t, old, "claude", "ann@acme.dev").Quota) {
		t.Fatal("Fill left stopped out")
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
		"error sam@thisbox relay: service unavailable (HTTP 503)",
		"error sam@thisbox update: cannot write beside /opt/bin/ai-usage: permission denied",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	page := plainText(r, Options{Width: 120, Loc: time.UTC})
	for _, s := range []string{
		"● relay failing  ● update check failed\n",
		"\n ERROR  sam@thisbox  relay: service unavailable (HTTP 503)\n",
		"\n ERROR  sam@thisbox  update: cannot write beside /opt/bin/ai-usage: permission denied\n",
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
		{"a bug", 15 * time.Minute, []string{"error sam@thisbox " + bug}, " ERROR  sam@thisbox  " + bug},
		{"a bug every run", 30 * time.Hour, []string{"error sam@thisbox " + bug, "silent sam@thisbox "}, " ERROR   sam@thisbox  " + bug},
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

// subAccount is a team subscription with burn this week, read at at.
func subAccount(label string, current bool, at time.Time, ws ...snapshot.Window) TeamAccount {
	name, _, _ := strings.Cut(label, "@")
	a := TeamAccount{Label: label, Name: name, Subscription: true, Current: current, Burn: Usage{Today: 1e6, Week: 4e6}, State: StateUnknown}
	if len(ws) > 0 {
		q := Quota{ObservedAt: at, AgeSeconds: int64(now.Sub(at).Seconds())}
		q.Windows, a.State = readQuota(readings(ws, at), nil, now)
		a.Quota = &q
	}
	return a
}

// newKindsReport is a team with an entry of every ATTENTION kind, and a
// subscription whose label is 40 characters long.
func newKindsReport() Report {
	start := now.Add(-4 * 24 * time.Hour) // 4/7 of the week gone
	weekly := func(pct float64) snapshot.Window { return week7(pct, start.Add(week)) }
	long := "a-very-long-subscription-name@acme.dev" + "xy"
	outside := subAccount("sam@mail.test", false, now, weekly(40))
	outside.Outside = &Outside{Points: 25, Since: now.Add(-20 * time.Hour)}
	noReading := subAccount(long, false, now)
	noReading.NoReading = strPtr("annbook runs v0.2.0: reinstall with install.sh")
	unused := subAccount("lee@mail.test", false, now)
	unused.NoReading, unused.Burn = strPtr("not read on srv1"), Usage{} // not in use: no news
	// Read, and no team device has used it for 11 days: IDLE, not UNDER.
	idle := subAccount("max@mail.test", false, now, weekly(0))
	idle.Burn, idle.LastActiveAt = Usage{}, tp(now.Add(-11*24*time.Hour))
	r := Report{GeneratedAt: now, Collector: Collector{Version: "v0.4.0", DeviceLabel: "annbook"}}
	r.Team = Team{
		PulledAt: tp(now), Latest: strPtr("v0.4.0"),
		Providers: []TeamProvider{{Provider: "claude", Accounts: []TeamAccount{
			subAccount("lee@corp.test", false, now.Add(-30*time.Hour), weekly(100)),
			subAccount("bob@mail.test", false, now, weekly(80)),
			subAccount("ann@acme.dev", true, now, weekly(52)),
			subAccount("kim@mail.test", false, now, weekly(52)), // tight, but not logged in here
			outside, noReading, unused, idle,
			subAccount("kim@corp.test", false, now, weekly(20)),
		}}},
		Devices: []TeamDevice{
			{Device: "d-0a1b2c3d4e5f60718293a4b5", Label: "annbook", OSUser: "ann", This: true, CollectorVersion: "v0.4.0", CollectedAt: now},
			{Device: "d-3f9a1c2e5b7d9f1a3c5e7b9d", Label: "annbook", OSUser: "ann", CollectorVersion: "v0.3.4", CollectedAt: now.Add(-3 * 24 * time.Hour),
				Silent: true, Old: true, ReplacedBy: strPtr("ann@annbook")},
			{Device: "d-b01c1d2e3f4a5b6c7d8e9f10", Label: "bot-b", CollectorVersion: "v0.4.0", CollectedAt: now, Error: strPtr("codex: app-server exited without answering")},
			{Device: "d-b02c1d2e3f4a5b6c7d8e9f20", Label: "bot-c", CollectorVersion: "v0.4.0", CollectedAt: now.Add(-30 * time.Hour), Silent: true},
			{Device: "d-b00c1d2e3f4a5b6c7d8e9f00", Label: "bot-a", CollectorVersion: "v0.3.4", CollectedAt: now, Old: true},
			{Device: "d-2f8a6c4e0b9d7f1a3c5e7b9d", Label: "srv1", CollectorVersion: "v0.2.0", CollectedAt: now, Old: true,
				Limitations: []string{LimitNoClaudeQuota, LimitNoUpdateErrors}},
		},
	}
	for i := range r.Team.Devices {
		d := &r.Team.Devices[i]
		d.Name = DeviceName(d.Label, d.OSUser)
	}
	r.Attention = attention(r.Team, r.Collector, now)
	return r
}

// Every kind in its place: what runs out, a tight window of one's own, one
// someone outside the team fills, failures, a subscription in use with no
// reading, silence, a reinstall's old record, old releases (the ones that
// need a reinstall first), what will be left unused, and what no one uses.
func TestAttentionOrderOfEveryKind(t *testing.T) {
	r := newKindsReport()
	var got []string
	for _, a := range r.Attention {
		got = append(got, strings.Join(strings.Fields(a.Kind+" "+a.Name+" "+strings.Join(a.Devices, ",")), " "))
	}
	want := []string{
		"out lee", "over bob", "tight ann", "outside sam", "error bot-b",
		"no_reading a-very-long-subscription-name", "silent bot-c", "duplicate ann@annbook",
		"old srv1", "old bot-a", "under kim", "idle max",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	out, dup, stuck, old := r.Attention[0], r.Attention[7], r.Attention[8], r.Attention[9]
	if out.ReadingAge != 30*3600 {
		t.Errorf("a stale OUT has no age: %+v", out)
	}
	if dup.DeviceID != "d-3f9a1c2e5b7d9f1a3c5e7b9d" || dup.Message != "ann@annbook" {
		t.Errorf("duplicate = %+v", dup)
	}
	if !reflect.DeepEqual(stuck.Limitations, []string{LimitNoClaudeQuota, LimitNoUpdateErrors}) || stuck.Message != "v0.4.0" || len(old.Limitations) != 0 {
		t.Errorf("old = %+v, %+v", stuck, old)
	}
}

// At 80 columns every ATTENTION line fits, beside a 40-character label,
// and each new kind keeps what to do.
func TestAttentionLinesAt80(t *testing.T) {
	r := newKindsReport()
	lines := pageSection(Render(r, Options{Width: 80, Loc: time.UTC, Interactive: true}), "ATTENTION")
	if len(lines) != len(r.Attention)+1 {
		t.Fatalf("ATTENTION =\n%s", strings.Join(lines, "\n"))
	}
	for _, l := range lines {
		if width(l) > 80 {
			t.Errorf("%d columns: %q", width(l), l)
		}
	}
	squeeze := func(l string) string { return strings.Join(strings.Fields(l), " ") }
	for i, want := range map[int]string{
		1:  "OUT claude lee@corp.test back Fri 12:00, in 3d · reading 1d old",
		3:  "TIGHT claude ann@acme.dev ~91% at reset Fri 12:00",
		4:  "OUTSIDE claude sam@mail.test +25 points, no team device spent",
		6:  "NO READING claude a-very-long-subscr… on v0.2.0: reinstall with install.sh",
		8:  "DUPLICATE ann@annbook ai-usage team forget-device 3f9a1c2e",
		9:  "OLD 1 device on v0.2.0 no Claude quota; reinstall",
		10: "OLD 1 device on v0.3.4 bot-a · latest v0.4.0",
		12: "IDLE claude max@mail.test idle 11d, paid and unused",
	} {
		if got := squeeze(lines[i]); got != want {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
	}
	// Where NO READING widens the kind column, a stale OUT keeps its
	// reading's age before how long until it is back.
	soon := r
	soon.Attention = slices.Clone(r.Attention)
	soon.Attention[0].At = tp(now.Add(47 * time.Hour))
	if l := pageSection(Render(soon, Options{Width: 80, Loc: time.UTC, Interactive: true}), "ATTENTION")[1]; !strings.HasSuffix(l, "back Thu 11:00 · reading 1d old") || width(l) > 80 {
		t.Errorf("a stale OUT back in 1d 23h = %q", l)
	}
	// A little more room shows the release and what to do; OLD names the
	// device.
	mid := pageSection(Render(r, Options{Width: 86, Loc: time.UTC, Interactive: true}), "ATTENTION")
	if !strings.HasSuffix(mid[6], "  on v0.2.0: reinstall with install.sh") {
		t.Errorf("at 86, NO READING = %q", mid[6])
	}
	// With room, the fuller forms.
	wide := pageSection(Render(r, Options{Width: 160, Loc: time.UTC, Interactive: true}), "ATTENTION")
	for i, want := range map[int]string{
		3: "TIGHT claude ann@acme.dev ~91% at reset Fri 12:00 at this week's pace",
		4: "OUTSIDE claude sam@mail.test +25 points in 20h, no team device spent",
		6: "NO READING claude a-very-long-subscripti… annbook runs v0.2.0: reinstall with install.sh",
		8: "DUPLICATE ann@annbook reinstalled as ann@annbook: ai-usage team forget-device 3f9a1c2e",
		9: "OLD 1 device on v0.2.0 srv1 · cannot read Claude quota; reinstall with install.sh",
	} {
		if got := squeeze(wide[i]); got != want {
			t.Errorf("at 160, line %d = %q, want %q", i, got, want)
		}
	}
}

// A forecast at the last day's pace says so.
func TestAttentionNamesTheRecentPace(t *testing.T) {
	r := newKindsReport()
	ann := &r.Team.Providers[0].Accounts[2]
	ann.Quota.Windows[0].Forecast.Basis = BasisRecent
	lines := pageSection(Render(r, Options{Width: 160, Loc: time.UTC, Interactive: true}), "ATTENTION")
	if !strings.Contains(lines[3], "~91% at reset Fri 12:00 at the last day's pace") {
		t.Fatalf("TIGHT = %q", lines[3])
	}
	// A 5h window's readings go back only to its reset: its pace is the
	// recent one, and at 80 columns the line drops it before misnaming it.
	bob := &r.Team.Providers[0].Accounts[1].Quota.Windows[0]
	bob.Name, bob.Minutes, bob.Forecast.Basis = "5h", 300, BasisRecent
	r.Attention = r.Attention[:2]
	for _, w := range []int{80, 160} {
		lines = pageSection(Render(r, Options{Width: w, Loc: time.UTC, Interactive: true}), "ATTENTION")
		if strings.Contains(lines[2], "last day's") || w > 80 && !strings.Contains(lines[2], "at the recent pace") || width(lines[2]) > w-1 {
			t.Errorf("OVER of a 5h window at %d = %q", w, lines[2])
		}
	}
}

// A harness that reads usage but no quota is not an ERROR: the
// subscription in use has no reading, and says why.
func TestQuotaOnlyProblemIsNoError(t *testing.T) {
	st := emptyState()
	f := newFixture(t, st)
	other := emptyState()
	other.Sources["claude"] = state.Source{Status: "ok", Error: "claude: quota not read: the usage request timed out"}
	addAccount(other, "claude", "kim@corp.test", true, nil, 1000)
	spend(other, "claude", "kim@corp.test", "/work/kim", 1000, now.Add(-2*time.Hour))
	r := withTeam(t, f, otherDoc(t, f.key, "d-b01c1d2e3f4a5b6c7d8e9f10", "srv1", now, other))
	want := []string{"no_reading  quota not read: the usage request timed out on kim@srv1", "old kim@srv1 v1.2.3"}
	if got := attentionList(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("attention = %q, want %q", got, want)
	}
}

// A paid subscription with a reading that no team device has used for a
// week is IDLE, the longest idle first, in place of its UNDER. Not one used
// sooner, one with no reading, the unknown account, a free plan, Hermes, or
// one that someone outside the team uses.
func TestIdle(t *testing.T) {
	start := now.Add(-4 * 24 * time.Hour) // 4/7 of the week gone
	weekly := func(pct float64) snapshot.Window { return week7(pct, start.Add(week)) }
	acct := func(label string, last time.Duration, ws ...snapshot.Window) TeamAccount {
		a := subAccount(label, false, now, ws...)
		a.Burn, a.LastActiveAt = Usage{}, tp(now.Add(-last))
		return a
	}
	day := 24 * time.Hour
	free := acct("free@mail.test", 20*day, weekly(0))
	free.Plan = strPtr("Free")
	outside := acct("web@mail.test", 20*day, weekly(30))
	outside.Outside = &Outside{Points: 30, Since: now.Add(-10 * time.Hour)}
	hermes := acct("openai-codex", 20*day, weekly(0))
	hermes.Subscription = false
	// Its window began before its last use: what filled it may be the
	// team's.
	month := acct("month@mail.test", 9*day, snapshot.Window{Name: "30d", Percent: 40, Minutes: 30 * 24 * 60, ResetsAt: tp(now.Add(15 * day))})
	r := Report{GeneratedAt: now, Collector: Collector{Version: "v0.4.0", DeviceLabel: "annbook"}}
	r.Team = Team{Providers: []TeamProvider{{Provider: "codex", Accounts: []TeamAccount{
		acct("ann@acme.dev", 8*day, weekly(0)),
		acct("bob@mail.test", 6*day, weekly(0)), // used 6 days ago: UNDER only
		acct("kim@mail.test", 30*day, weekly(0)),
		acct("lee@mail.test", 30*day),                   // no reading
		acct(collect.UnknownAccount, 30*day, weekly(0)), // no login to pay for
		acct("sam@mail.test", 10*day, weekly(12)),       // used outside the team since
		acct("old@mail.test", 9*day, weekly(0.4)),       // under 1%: unused
		free, outside, hermes, month,
	}}}}
	r.Attention = attention(r.Team, r.Collector, now)
	var got []string
	for _, a := range r.Attention {
		s := a.Kind + " " + a.Name
		if a.Kind == AttentionIdle {
			s += " " + age(now.Sub(*a.At))
		}
		got = append(got, s)
	}
	// The others keep their UNDER: a free plan, the unknown account, and one
	// that someone used since.
	want := []string{"outside web", "under bob", "under unknown", "under free", "under sam",
		"idle kim 30d", "idle old 9d", "idle month 9d", "idle ann 8d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention = %q, want %q", got, want)
	}
	line := ""
	for _, l := range pageSection(Render(r, Options{Width: 80, Loc: time.UTC, Interactive: true}), "ATTENTION") {
		if strings.Contains(l, "kim@") {
			line = strings.Join(strings.Fields(l), " ")
		}
	}
	if line != "IDLE codex kim@mail.test idle 30d, paid and unused" {
		t.Errorf("IDLE = %q", line)
	}
	// The static page counts the ones it does not show.
	static := pageSection(Render(r, Options{Width: 80, Loc: time.UTC}), "ATTENTION")
	if l := strings.TrimSpace(static[len(static)-1]); l != "+3 more: 3 idle" {
		t.Errorf("static ATTENTION ends in %q", l)
	}
}

// The report words each ATTENTION entry as its line does at any width: the
// subject uncut, and the text in its longest form, with clocks in the zone
// it was given.
func TestAttentionWordsAreTheLines(t *testing.T) {
	r := newKindsReport()
	describe(&r, time.UTC)
	o := Options{Width: 400, Loc: time.UTC, Interactive: true}
	p := newPage(&r, o)
	lines := pageSection(Render(r, o), "ATTENTION")[1:]
	squeeze := func(l string) string { return strings.Join(strings.Fields(l), " ") }
	for i, a := range r.Attention {
		word, _ := p.badgeOf(a.Kind)
		line, ok := strings.CutPrefix(squeeze(lines[i]), word+" ")
		subject, cut := strings.CutSuffix(line, " "+squeeze(a.Text))
		// The line cuts a long subject.
		if !ok || !cut || a.Text == "" || subject != squeeze(a.Subject) && !strings.HasPrefix(a.Subject, strings.TrimSuffix(subject, "…")) {
			t.Errorf("%s: subject %q, text %q, line %q", a.Kind, a.Subject, a.Text, lines[i])
		}
	}
	got := map[string]string{}
	for _, a := range r.Attention {
		got[a.Subject] = a.Text
	}
	for subject, text := range map[string]string{
		"claude lee@corp.test":                            "back Fri 12:00, in 3d · reading 1d old",
		"claude a-very-long-subscription-name@acme.devxy": "annbook runs v0.2.0: reinstall with install.sh",
		"1 device on v0.2.0":                              "srv1 · cannot read Claude quota; reinstall with install.sh",
		"claude max@mail.test":                            "idle 11d, paid and unused",
	} {
		if got[subject] != text {
			t.Errorf("%s: %q, want %q", subject, got[subject], text)
		}
	}
	// In another zone, other clocks.
	r = newKindsReport()
	describe(&r, sampleZone)
	if a := r.Attention[0]; a.Text != "back Fri 15:00, in 3d · reading 1d old" {
		t.Errorf("at UTC+3: %q", a.Text)
	}
}

// Each device's note is what the status view's NOTE says of it in full,
// with clocks in the zone the report was given, and nil where it says
// nothing.
func TestDeviceNoteIsTheStatusViews(t *testing.T) {
	r := troubled(t)
	for i := range r.Team.Devices {
		r.Team.Devices[i].Note = nil
	}
	describe(&r, time.UTC)
	p := newPage(&r, Options{Loc: time.UTC})
	none := 0
	for _, d := range r.Team.Devices {
		if want := p.deviceNote(d, -1).String(); deref(d.Note) != cmp.Or(want, "<nil>") {
			t.Errorf("%s: note %q, want %q", d.Label, deref(d.Note), want)
		}
		if d.Note == nil {
			none++
		}
		if d.Label == "bot-e" && deref(d.Note) != "silent since Mon 11:02 · last error: codex: not logged in" {
			t.Errorf("bot-e: note %q", deref(d.Note))
		}
	}
	if none == 0 {
		t.Error("every device has a note")
	}
}

// ATTENTION with every kind, and tokens no login was known for, at 80 and
// 120 columns, and its entries in the JSON.
func TestAttentionGolden(t *testing.T) {
	r := newKindsReport()
	unknown := subAccount(collect.UnknownAccount, false, now)
	unknown.NoReading = strPtr("not logged in on srv1: run claude and log in")
	r.Team.Providers[0].Accounts = append(r.Team.Providers[0].Accounts, unknown)
	r.Attention = attention(r.Team, r.Collector, now)
	describe(&r, time.UTC)
	var b strings.Builder
	for _, w := range []int{80, 120} {
		for _, l := range pageSection(Render(r, Options{Width: w, Loc: time.UTC, Interactive: true}), "ATTENTION") {
			b.WriteString(l + "\n")
		}
		b.WriteString("\n")
	}
	golden(t, "attention.golden", b.String())
	js, err := json.MarshalIndent(r.Attention, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "attention.json.golden", string(js)+"\n")
}
