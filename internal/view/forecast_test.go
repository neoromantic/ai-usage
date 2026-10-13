package view

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// week7 is a 7-day window that resets at reset.
func week7(pct float64, reset time.Time) snapshot.Window {
	return snapshot.Window{Name: "7d", Percent: pct, Minutes: 7 * 24 * 60, ResetsAt: &reset}
}

func TestForecastStates(t *testing.T) {
	// The reading is taken at now, 40% into a week that resets in 4.2 days.
	reset := now.Add(time.Duration(0.6 * float64(week)))
	for _, c := range []struct {
		used     float64
		state    string
		forecast float64
	}{
		{100, StateOut, 250},
		{62.8, StateOver, 157},
		{40, StateOver, 100},
		{36.8, StateTight, 92},
		{34, StateTight, 85},
		{33.6, StateOK, 84},
		{32.4, StateOK, 81},
		{20, StateOK, 50},
		{13.2, StateUnder, 33},
		{0, StateUnder, 0},
	} {
		w := readWindow(week7(c.used, reset), now, now, nil)
		if w.State != c.state || w.Forecast == nil || w.Forecast.Percent != c.forecast {
			t.Errorf("%v%% used: %s %+v, want %s at %v%%", c.used, w.State, w.Forecast, c.state, c.forecast)
		}
		if w.Stale || w.Reset {
			t.Errorf("%v%% used: stale %v, reset %v", c.used, w.Stale, w.Reset)
		}
	}
}

func TestForecastRunsOut(t *testing.T) {
	start := now.Add(-2 * 24 * time.Hour)
	reset := start.Add(week)
	w := readWindow(week7(50, reset), now, now, nil)
	// Half the window in two days: at that pace, full in four.
	if w.State != StateOver || w.Forecast.Percent != 175 || w.Forecast.RunsOutAt == nil || !w.Forecast.RunsOutAt.Equal(start.Add(4*24*time.Hour)) {
		t.Fatalf("forecast = %s %+v", w.State, w.Forecast)
	}
	if !w.Forecast.RunsOutAt.Before(reset) {
		t.Fatalf("runs out at %v, after the reset %v", w.Forecast.RunsOutAt, reset)
	}
	// Out has no time to run out.
	if w := readWindow(week7(100, reset), now, now, nil); w.State != StateOut || w.Forecast.RunsOutAt != nil {
		t.Fatalf("out = %s %+v", w.State, w.Forecast)
	}
	// Just under the line has none either.
	if w := readWindow(week7(20, reset), now, now, nil); w.Forecast.RunsOutAt != nil {
		t.Fatalf("ok runs out at %v", w.Forecast.RunsOutAt)
	}
}

// A forecast that rounds to 100% runs out before the reset when it is over
// 100% before it is rounded, and at the reset when it is 100% exactly.
func TestForecastRunsOutAtRoundedHundred(t *testing.T) {
	// Half the week gone.
	start := now.Add(-week / 2)
	reset := start.Add(week)
	w := readWindow(week7(50.2, reset), now, now, nil)
	// 100.4%: 50.2% in 3.5 days, full in 3.5 × 100 ÷ 50.2 days, 40 minutes
	// before the reset.
	if w.State != StateOver || w.Forecast.Percent != 100 || w.Forecast.RunsOutAt == nil {
		t.Fatalf("100.4%% = %s %+v, want over at 100%%, running out", w.State, w.Forecast)
	}
	if d := reset.Sub(*w.Forecast.RunsOutAt); d < 40*time.Minute || d > 41*time.Minute {
		t.Fatalf("100.4%% runs out %v before the reset", d)
	}
	// 100% exactly has no time to run out before the reset.
	if w := readWindow(week7(50, reset), now, now, nil); w.State != StateOver || w.Forecast.Percent != 100 || w.Forecast.RunsOutAt != nil {
		t.Fatalf("100%% = %s %+v", w.State, w.Forecast)
	}
	// 99.6% rounds to 100% as well, and does not run out.
	if w := readWindow(week7(49.8, reset), now, now, nil); w.State != StateOver || w.Forecast.Percent != 100 || w.Forecast.RunsOutAt != nil {
		t.Fatalf("99.6%% = %s %+v", w.State, w.Forecast)
	}
}

func TestForecastCapsAt999(t *testing.T) {
	reset := now.Add(week - time.Hour)
	if w := readWindow(week7(95, reset), now, now, nil); w.State != StateOver || w.Forecast.Percent != MaxForecast {
		t.Fatalf("forecast = %s %+v", w.State, w.Forecast)
	}
	// At the very start of a window, what was used counts as used over its
	// first tenth: 1% is too early to say, 10% is over.
	if w := readWindow(week7(1, now.Add(week)), now, now, nil); w.State != StateUnknown || w.Forecast != nil {
		t.Fatalf("1%% at the start = %s %+v", w.State, w.Forecast)
	}
	if w := readWindow(week7(10, now.Add(week)), now, now, nil); w.State != StateOver || w.Forecast.Percent != 110 {
		t.Fatalf("10%% at the start = %s %+v", w.State, w.Forecast)
	}
}

func TestForecastInTheFirstTenth(t *testing.T) {
	// 5% into the week. What was used counts as used over its first tenth:
	// the forecast is used × 10.5.
	reset := now.Add(time.Duration(0.95 * float64(week)))
	for _, c := range []struct {
		used  float64
		state string
	}{
		{3, StateUnknown}, // 32%: too early to say
		{5, StateUnknown}, // 53%
		{9, StateUnknown}, // 95%
		{10, StateOver},   // 105%: over already
		{100, StateOut},
	} {
		w := readWindow(week7(c.used, reset), now, now, nil)
		if w.State != c.state {
			t.Errorf("%v%% used in the first tenth: %s, want %s", c.used, w.State, c.state)
		}
		if c.state == StateUnknown && w.Forecast != nil {
			t.Errorf("%v%% used in the first tenth: forecast %+v", c.used, w.Forecast)
		}
	}
	// A tenth in, the forecast shows.
	if w := readWindow(week7(3, now.Add(time.Duration(0.9*float64(week)))), now, now, nil); w.State != StateUnder || w.Forecast.Percent != 30 {
		t.Fatalf("a tenth in: %s %+v", w.State, w.Forecast)
	}
}

func TestForecastOfAStaleReading(t *testing.T) {
	// Read a day ago, 40% into the week: the forecast is of the reading,
	// and the reading is marked old.
	at := now.Add(-24 * time.Hour)
	reset := at.Add(time.Duration(0.6 * float64(week)))
	w := readWindow(week7(62.8, reset), at, now, nil)
	if !w.Stale || w.Reset || w.State != StateOver || w.Forecast.Percent != 157 || !w.ObservedAt.Equal(at) {
		t.Fatalf("stale = %+v %+v", w, w.Forecast)
	}
	if w := readWindow(week7(62.8, reset), now.Add(-6*time.Hour), now, nil); w.Stale {
		t.Fatal("a reading 6 hours old is stale")
	}
	// A full window stays full until it resets.
	if w := readWindow(week7(100, reset), at, now, nil); w.Stale || w.State != StateOut {
		t.Fatalf("a full window read a day ago: %+v", w)
	}
}

func TestForecastOfAWindowThatReset(t *testing.T) {
	at := now.Add(-3 * 24 * time.Hour)
	for _, pct := range []float64{100, 60, 0} {
		w := readWindow(week7(pct, now.Add(-time.Minute)), at, now, nil)
		if !w.Reset || w.State != StateUnknown || w.Forecast != nil {
			t.Errorf("%v%% before the reset: %+v", pct, w)
		}
	}
	// Resetting exactly now counts as reset.
	if w := readWindow(week7(60, now), at, now, nil); !w.Reset {
		t.Fatal("a window that resets now has not reset")
	}
}

func TestForecastNeedsTheWindow(t *testing.T) {
	reset := now.Add(24 * time.Hour)
	for name, w := range map[string]snapshot.Window{
		"no reset":  {Name: "7d", Percent: 50, Minutes: 10080},
		"no length": {Name: "credits", Percent: 50, ResetsAt: &reset},
	} {
		if r := readWindow(w, now, now, nil); r.State != StateUnknown || r.Forecast != nil {
			t.Errorf("%s: %s %+v", name, r.State, r.Forecast)
		}
	}
	// Out is out without a forecast.
	if r := readWindow(snapshot.Window{Name: "credits", Percent: 100}, now, now, nil); r.State != StateOut {
		t.Fatalf("full with no reset: %s", r.State)
	}
	// The length comes from the name when the harness gives none.
	if r := readWindow(snapshot.Window{Name: "5h", Percent: 50, ResetsAt: tp(now.Add(150 * time.Minute))}, now, now, nil); r.State != StateOver || r.Forecast.Percent != 100 {
		t.Fatalf("5h by name: %s %+v", r.State, r.Forecast)
	}
}

func TestMainWindow(t *testing.T) {
	reset := now.Add(time.Hour)
	for _, c := range []struct {
		names []string
		want  int
	}{
		{nil, -1},
		{[]string{"5h", "7d", "7d Opus"}, 1},
		{[]string{"5h", "7d Opus"}, 1},
		{[]string{"month credits"}, 0},
		{[]string{"5h", "spark 7d"}, 0}, // no length in the name: the 5h is the longest known
		{[]string{"7d credits"}, 0},
	} {
		var ws []snapshot.Window
		for _, n := range c.names {
			ws = append(ws, win(n, 10, reset))
		}
		if got := mainIndex(readings(ws, now)); got != c.want {
			t.Errorf("%v: main %d, want %d", c.names, got, c.want)
		}
	}
}

func TestAccountStateIsTheWorstWindowThatLimits(t *testing.T) {
	start := now.Add(-3 * 24 * time.Hour)
	weekReset := start.Add(week)
	fiveReset := now.Add(2 * time.Hour)
	five := func(pct float64) snapshot.Window {
		return snapshot.Window{Name: "5h", Percent: pct, Minutes: 300, ResetsAt: &fiveReset}
	}
	opus := func(pct float64) snapshot.Window {
		return snapshot.Window{Name: "7d Opus", Percent: pct, Minutes: 10080, ResetsAt: &weekReset}
	}
	for _, c := range []struct {
		name string
		ws   []snapshot.Window
		want string
		rows []string // windows that limit more than the main one
	}{
		{"weekly alone", []snapshot.Window{week7(30, weekReset)}, StateOK, nil},
		{"5h out", []snapshot.Window{five(100), week7(10, weekReset)}, StateOut, []string{"5h"}},
		{"model window fuller", []snapshot.Window{week7(20, weekReset), opus(60)}, StateOver, []string{"7d Opus"}},
		{"model window at 0 stays hidden", []snapshot.Window{week7(20, weekReset), opus(0)}, StateUnder, nil},
		{"emptier 5h stays hidden", []snapshot.Window{five(5), week7(30, weekReset)}, StateOK, nil},
		{"no windows", nil, StateUnknown, nil},
	} {
		ws, state := readQuota(readings(c.ws, now), nil, now)
		var rows []string
		m := mainWindow(ws)
		for _, w := range ws {
			if w.Limits && !w.Main {
				rows = append(rows, w.Name)
			}
		}
		if state != c.want || len(rows) != len(c.rows) || (len(rows) > 0 && rows[0] != c.rows[0]) {
			t.Errorf("%s: state %s rows %v, want %s %v", c.name, state, rows, c.want, c.rows)
		}
		if (m == nil) != (len(c.ws) == 0) || (m != nil && !m.Limits) {
			t.Errorf("%s: main window %+v", c.name, m)
		}
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
	// The refusals are 10 hours old: the provider can reset a window early,
	// so OUT says how old they are.
	for _, a := range r.Attention {
		if a.Kind != AttentionOut || a.ReadingAge != 10*3600 {
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

// pt is a reading of a weekly window that resets at reset.
func pt(at time.Time, pct float64, reset time.Time) pacePoint {
	return pacePoint{At: at, Window: "7d", ResetsAt: &reset, Percent: pct}
}

// A week left idle at first, then used fast: the average since the week
// began says tight, but the pace since it was idle, and faster still the
// pace of the last day, runs it out a day before the reset.
func TestForecastFromTheRecentPace(t *testing.T) {
	open := time.Date(2026, 9, 21, 15, 0, 0, 0, time.UTC) // a Monday
	reset := open.Add(week)
	at := time.Date(2026, 9, 27, 12, 14, 0, 0, time.UTC)
	pts := []pacePoint{
		pt(time.Date(2026, 9, 25, 22, 30, 0, 0, time.UTC), 0, reset),
		pt(time.Date(2026, 9, 26, 12, 30, 0, 0, time.UTC), 14, reset),
		pt(time.Date(2026, 9, 26, 22, 15, 0, 0, time.UTC), 29, reset),
		pt(time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC), 79, reset),
	}
	if w := readWindow(week7(80, reset), at, at, nil); w.State != StateTight || w.Forecast.Percent != 95 || w.Forecast.Basis != BasisWindow {
		t.Fatalf("without the readings: %s %+v", w.State, w.Forecast)
	}
	w := readWindow(week7(80, reset), at, at, pts)
	f := w.Forecast
	if w.State != StateOver || f.Percent <= 100 || f.Basis != BasisRecent || f.RunsOutAt == nil || !f.RunsOutAt.Before(reset) {
		t.Fatalf("with the readings: %s %+v", w.State, f)
	}
	// 2.9 points an hour: the 20 left go in under 7 hours.
	if f.Pace < 2.8 || f.Pace > 3 || f.RunsOutAt.Sub(at) > 7*time.Hour || f.Percent != math.Round(80+f.Pace*reset.Sub(at).Hours()) {
		t.Fatalf("pace %v, runs out at %v, forecast %v", f.Pace, f.RunsOutAt, f.Percent)
	}
	// The share of the week gone stays the week's.
	if math.Abs(f.Elapsed-at.Sub(open).Hours()/week.Hours()) > 1e-9 {
		t.Fatalf("elapsed %v", f.Elapsed)
	}
}

// A window idle at first fills from its last reading at 0%, not from its
// start.
func TestForecastFromAnIdleStart(t *testing.T) {
	reset := now.Add(3 * 24 * time.Hour) // 4 days of the week gone
	pts := []pacePoint{pt(now.Add(-48*time.Hour), 0, reset), pt(now.Add(-24*time.Hour), 0, reset)}
	// 24 points in the 24 hours since: 1 an hour, 72 more by the reset.
	w := readWindow(week7(24, reset), now, now, pts)
	if f := w.Forecast; w.State != StateTight || f.Percent != 96 || f.Pace != 1 || f.Basis != BasisWindow {
		t.Fatalf("idle start: %s %+v", w.State, f)
	}
	// Since the week began, it is under.
	if w := readWindow(week7(24, reset), now, now, nil); w.State != StateUnder || w.Forecast.Percent != 42 {
		t.Fatalf("from the start: %s %+v", w.State, w.Forecast)
	}
}

// A week a provider reset early for everyone, as Codex does, and then used
// a few points in its first hours, is not over: those hours are a working
// morning, not the pace of the week. While idle, each reading reset a week
// after it was taken; the first use fixed the reset.
func TestForecastEarlyInAWeekIsNotOver(t *testing.T) {
	open := now.Add(-3*time.Hour - 20*time.Minute) // the first use
	reset := open.Add(week)
	idle := func(at time.Time) pacePoint { return pt(at, 0, at.Add(week)) }
	pts := []pacePoint{
		idle(open.Add(-2 * time.Hour)),
		idle(open.Add(-time.Hour)),
		pt(open.Add(5*time.Minute), 0, reset),
		pt(open.Add(80*time.Minute), 2, reset),
		pt(open.Add(140*time.Minute), 4, reset),
	}
	// 6 points in 3¼ hours: nearly 2 an hour, which over the week would be
	// 311%.
	if w := readWindow(week7(6, reset), now, now, pts); w.State != StateUnknown || w.Forecast != nil {
		t.Fatalf("a morning's use: %s %+v", w.State, w.Forecast)
	}
	// Idle at 0% until 40 minutes ago, then a point: 1.5 an hour, 248% over
	// the week.
	pts = []pacePoint{pt(now.Add(-3*time.Hour), 0, reset), pt(now.Add(-2*time.Hour), 0, reset), pt(now.Add(-40*time.Minute), 0, reset)}
	if w := readWindow(week7(1, reset), now, now, pts); w.State != StateUnknown || w.Forecast != nil {
		t.Fatalf("40 minutes' use: %s %+v", w.State, w.Forecast)
	}
	// 30% of the week used by its 12th hour is over: it runs out in
	// 100 ÷ 30 × 16.8 hours from 16.8 hours before the reading.
	reset = now.Add(week - 12*time.Hour)
	w := readWindow(week7(30, reset), now, now, nil)
	if f := w.Forecast; w.State != StateOver || f.Percent != 309 || f.Basis != BasisWindow || f.RunsOutAt == nil || f.RunsOutAt.Sub(now).Round(time.Minute) != 39*time.Hour+12*time.Minute {
		t.Fatalf("a heavy start: %s %+v", w.State, f)
	}
}

// A window that sat at 0% past its first tenth fills from its last reading
// at 0%, over a tenth of the window at least: 2 points read 20 minutes
// after it are not 6 points an hour.
func TestForecastRightAfterAnIdleStretch(t *testing.T) {
	reset := now.Add(time.Duration(0.75 * float64(week))) // a quarter of the week gone
	pts := []pacePoint{
		pt(now.Add(-20*time.Hour), 0, reset),
		pt(now.Add(-10*time.Hour), 0, reset),
		pt(now.Add(-40*time.Minute), 0, reset),
		pt(now.Add(-20*time.Minute), 0, reset),
	}
	// 2 points over 16.8 hours, 126 hours before the reset: 17%.
	w := readWindow(week7(2, reset), now, now, pts)
	if f := w.Forecast; w.State != StateUnder || f.Percent != 17 || f.Basis != BasisWindow || math.Abs(f.Pace-2/16.8) > 1e-9 {
		t.Fatalf("after an idle stretch: %s %+v", w.State, f)
	}
}

// The last day's pace needs readings that span a tenth of the window, an
// hour at least: a weekly window's needs 16.8 hours of them, so a device
// that slept through the night does not take its morning for the pace.
func TestRecentPaceNeedsATenthOfTheWindow(t *testing.T) {
	reset := now.Add(3 * 24 * time.Hour) // 4 days of the week gone
	// 14 points in 14 hours: too short a span, so the week's average.
	w := readWindow(week7(24, reset), now, now, []pacePoint{pt(now.Add(-14*time.Hour), 10, reset)})
	if f := w.Forecast; w.State != StateUnder || f.Basis != BasisWindow || f.Percent != 42 {
		t.Fatalf("14 hours of readings: %s %+v", w.State, f)
	}
	// 17 points in 17 hours: 1 an hour, 72 more by the reset.
	w = readWindow(week7(24, reset), now, now, []pacePoint{pt(now.Add(-17*time.Hour), 7, reset)})
	if f := w.Forecast; w.State != StateTight || f.Basis != BasisRecent || f.Percent != 96 {
		t.Fatalf("17 hours of readings: %s %+v", w.State, f)
	}
	// A 5-hour window's pace needs an hour of readings, as it did.
	five := now.Add(3 * time.Hour)
	pts := []pacePoint{{At: now.Add(-time.Hour), Window: "5h", ResetsAt: &five, Percent: 10}}
	w = readWindow(snapshot.Window{Name: "5h", Percent: 30, Minutes: 300, ResetsAt: &five}, now, now, pts)
	if f := w.Forecast; w.State != StateTight || f.Basis != BasisRecent || f.Percent != 90 {
		t.Fatalf("an hour of a 5-hour window: %s %+v", w.State, f)
	}
}

// A drop of more than 2 points within one period is use taken back: the
// pace starts again after it.
func TestForecastAfterADrop(t *testing.T) {
	reset := now.Add(3 * 24 * time.Hour)
	pts := []pacePoint{
		pt(now.Add(-40*time.Hour), 60, reset),
		pt(now.Add(-30*time.Hour), 67, reset),
		pt(now.Add(-20*time.Hour), 10, reset),
		pt(now.Add(-10*time.Hour), 14, reset),
	}
	series := paceSeries(week7(18, reset), now, pts)
	var got []float64
	for _, p := range series {
		got = append(got, p.Percent)
	}
	if !reflect.DeepEqual(got, []float64{10, 14, 18}) {
		t.Fatalf("series = %v", got)
	}
	// 0.4 points an hour since the drop, faster than the week's average.
	w := readWindow(week7(18, reset), now, now, pts)
	if f := w.Forecast; f.Basis != BasisRecent || math.Abs(f.Pace-0.4) > 1e-9 || f.Percent != 47 {
		t.Fatalf("after the drop: %+v", f)
	}
}

// Only readings of the window's current period count, one per time, and
// the pace needs an hour of them.
func TestPaceSeries(t *testing.T) {
	reset := now.Add(3 * 24 * time.Hour)
	other := reset.Add(-week)
	pts := []pacePoint{
		pt(now.Add(-3*time.Hour), 90, other),                                       // the period before
		{At: now.Add(-3 * time.Hour), Window: "5h", ResetsAt: &reset, Percent: 70}, // another window
		{At: now.Add(-3 * time.Hour), Window: "7d", Percent: 70},                   // no reset time
		pt(now.Add(-2*time.Hour), 10, reset.Add(30*time.Second)),                   // the same reset, give or take
		pt(now.Add(-2*time.Hour), 10, reset),                                       // read twice
		pt(now.Add(time.Hour), 40, reset),                                          // after the reading
	}
	series := paceSeries(week7(12, reset), now, pts)
	if len(series) != 2 || series[0].Percent != 10 || !series[1].At.Equal(now) {
		t.Fatalf("series = %+v", series)
	}
	if _, ok := recentSlope(series[:1], now, slopeMin); ok {
		t.Fatal("a slope from one reading")
	}
	if _, ok := recentSlope([]pacePoint{pt(now.Add(-50*time.Minute), 10, reset), pt(now, 12, reset)}, now, slopeMin); ok {
		t.Fatal("a slope from 50 minutes")
	}
	// A pace that falls is no pace.
	if s, ok := recentSlope([]pacePoint{pt(now.Add(-2*time.Hour), 14, reset), pt(now, 13, reset)}, now, slopeMin); !ok || s != 0 {
		t.Fatalf("falling slope = %v %v", s, ok)
	}
}

// Build takes the pace from this device's samples and the team's reads,
// for this device's account and the team's, however each spells the label.
func TestBuildForecastsFromSamplesAndHistory(t *testing.T) {
	reset := now.Add(3 * 24 * time.Hour)
	st := emptyState()
	addAccount(st, "claude", "ann@acme.dev", true, &state.Quota{At: now, Windows: []snapshot.Window{week7(30, reset)}}, 10)
	f := newFixture(t, st)
	sample := func(at time.Time, pct float64) state.Sample {
		return state.Sample{At: at, Accounts: []state.SampleAccount{{Provider: "claude", Label: "ann@acme.dev", QuotaAt: tp(at), Windows: []snapshot.Window{week7(pct, reset)}}}}
	}
	f.in.Samples = []state.Sample{sample(now.Add(-18*time.Hour), 12), sample(now.Add(-9*time.Hour), 21)}
	r := withTeam(t, f)
	for _, q := range []*Quota{findAccount(t, r, "claude", "ann@acme.dev").Quota, findTeamAccount(t, r, "claude", "ann@acme.dev").Quota} {
		// 1 point an hour over the last 18 hours.
		if m := mainWindow(q.Windows); m.Forecast.Basis != BasisRecent || math.Abs(m.Forecast.Pace-1) > 1e-9 || m.State != StateOver {
			fc := m.Forecast
			t.Fatalf("with samples: %+v", fc)
		}
	}
	// The team's reads hold another device's readings, spelled otherwise.
	f.in.Samples = nil
	f.in.Team.History = map[string][]collect.Point{collect.HistoryKey("claude", "Ann@Acme.dev"): {
		{At: now.Add(-18 * time.Hour), Window: "7d", ResetsAt: &reset, Percent: 12},
	}}
	r = Build(f.in)
	if fc := mainWindow(findTeamAccount(t, r, "claude", "ann@acme.dev").Quota.Windows).Forecast; fc.Basis != BasisRecent || math.Abs(fc.Pace-1) > 1e-9 {
		t.Fatalf("with the team's reads: %+v", fc)
	}
}
