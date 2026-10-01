package view

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

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
		"error Mac.localdomain",
		"silent leebook",
		"old Mac.localdomain,leebook",
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
	idle := subAccount("lee@mail.test", false, now)
	idle.NoReading, idle.Burn = strPtr("not read on srv1"), Usage{} // not in use: no news
	r := Report{GeneratedAt: now, Collector: Collector{Version: "v0.4.0", DeviceLabel: "annbook"}}
	r.Team = Team{
		PulledAt: tp(now), Latest: strPtr("v0.4.0"),
		Providers: []TeamProvider{{Provider: "claude", Accounts: []TeamAccount{
			subAccount("lee@corp.test", false, now.Add(-30*time.Hour), weekly(100)),
			subAccount("bob@mail.test", false, now, weekly(80)),
			subAccount("ann@acme.dev", true, now, weekly(52)),
			subAccount("kim@mail.test", false, now, weekly(52)), // tight, but not logged in here
			outside, noReading, idle,
			subAccount("kim@corp.test", false, now, weekly(20)),
		}}},
		Devices: []TeamDevice{
			{Device: "d-0a1b2c3d4e5f60718293a4b5", Label: "annbook", This: true, CollectorVersion: "v0.4.0", CollectedAt: now},
			{Device: "d-3f9a1c2e5b7d9f1a3c5e7b9d", Label: "annbook", CollectorVersion: "v0.3.4", CollectedAt: now.Add(-3 * 24 * time.Hour),
				Silent: true, Old: true, ReplacedBy: strPtr("annbook")},
			{Device: "d-b01c1d2e3f4a5b6c7d8e9f10", Label: "bot-b", CollectorVersion: "v0.4.0", CollectedAt: now, Error: strPtr("codex: app-server exited without answering")},
			{Device: "d-b02c1d2e3f4a5b6c7d8e9f20", Label: "bot-c", CollectorVersion: "v0.4.0", CollectedAt: now.Add(-30 * time.Hour), Silent: true},
			{Device: "d-b00c1d2e3f4a5b6c7d8e9f00", Label: "bot-a", CollectorVersion: "v0.3.4", CollectedAt: now, Old: true},
			{Device: "d-2f8a6c4e0b9d7f1a3c5e7b9d", Label: "srv1", CollectorVersion: "v0.2.0", CollectedAt: now, Old: true,
				Limitations: []string{LimitNoClaudeQuota, LimitNoUpdateErrors}},
		},
	}
	r.Attention = attention(r.Team, r.Collector, now)
	return r
}

// Every kind in its place: what runs out, a tight window of one's own, one
// someone outside the team fills, failures, a subscription in use with no
// reading, silence, a reinstall's old record, old releases (the ones that
// need a reinstall first), and what will be left unused.
func TestAttentionOrderOfEveryKind(t *testing.T) {
	r := newKindsReport()
	var got []string
	for _, a := range r.Attention {
		got = append(got, strings.Join(strings.Fields(a.Kind+" "+a.Name+" "+strings.Join(a.Devices, ",")), " "))
	}
	want := []string{
		"out lee", "over bob", "tight ann", "outside sam", "error bot-b",
		"no_reading a-very-long-subscription-name", "silent bot-c", "duplicate annbook",
		"old srv1", "old bot-a", "under kim",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	out, dup, stuck, old := r.Attention[0], r.Attention[7], r.Attention[8], r.Attention[9]
	if out.ReadingAge != 30*3600 {
		t.Errorf("a stale OUT has no age: %+v", out)
	}
	if dup.DeviceID != "d-3f9a1c2e5b7d9f1a3c5e7b9d" || dup.Message != "annbook" {
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
		8:  "DUPLICATE annbook ai-usage team forget-device 3f9a1c2e",
		9:  "OLD 1 device on v0.2.0 no Claude quota; reinstall",
		10: "OLD 1 device on v0.3.4 bot-a · latest v0.4.0",
	} {
		if got := squeeze(lines[i]); got != want {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
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
		8: "DUPLICATE annbook reinstalled as annbook: ai-usage team forget-device 3f9a1c2e",
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
	want := []string{"no_reading  quota not read: the usage request timed out on srv1", "old srv1 v1.2.3"}
	if got := attentionList(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("attention = %q, want %q", got, want)
	}
}
