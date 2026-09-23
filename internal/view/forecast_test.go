package view

import (
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
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
		w := readWindow(week7(c.used, reset), now, now)
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
	w := readWindow(week7(50, reset), now, now)
	// Half the window in two days: at that pace, full in four.
	if w.State != StateOver || w.Forecast.Percent != 175 || w.Forecast.RunsOutAt == nil || !w.Forecast.RunsOutAt.Equal(start.Add(4*24*time.Hour)) {
		t.Fatalf("forecast = %s %+v", w.State, w.Forecast)
	}
	if !w.Forecast.RunsOutAt.Before(reset) {
		t.Fatalf("runs out at %v, after the reset %v", w.Forecast.RunsOutAt, reset)
	}
	// Out has no time to run out.
	if w := readWindow(week7(100, reset), now, now); w.State != StateOut || w.Forecast.RunsOutAt != nil {
		t.Fatalf("out = %s %+v", w.State, w.Forecast)
	}
	// Just under the line has none either.
	if w := readWindow(week7(20, reset), now, now); w.Forecast.RunsOutAt != nil {
		t.Fatalf("ok runs out at %v", w.Forecast.RunsOutAt)
	}
}

func TestForecastCapsAt999(t *testing.T) {
	reset := now.Add(week - time.Hour)
	if w := readWindow(week7(90, reset), now, now); w.State != StateOver || w.Forecast.Percent != MaxForecast {
		t.Fatalf("forecast = %s %+v", w.State, w.Forecast)
	}
	// At the very start of a window, any use is over.
	if w := readWindow(week7(1, now.Add(week)), now, now); w.State != StateOver || w.Forecast.Percent != MaxForecast {
		t.Fatalf("forecast at the start = %s %+v", w.State, w.Forecast)
	}
}

func TestForecastInTheFirstTenth(t *testing.T) {
	// 5% into the week.
	reset := now.Add(time.Duration(0.95 * float64(week)))
	for _, c := range []struct {
		used  float64
		state string
	}{
		{3, StateUnknown}, // 60%: too early to say
		{4.9, StateUnknown},
		{5, StateOver}, // 100%: over already
		{8, StateOver},
		{100, StateOut},
	} {
		w := readWindow(week7(c.used, reset), now, now)
		if w.State != c.state {
			t.Errorf("%v%% used in the first tenth: %s, want %s", c.used, w.State, c.state)
		}
		if c.state == StateUnknown && w.Forecast != nil {
			t.Errorf("%v%% used in the first tenth: forecast %+v", c.used, w.Forecast)
		}
	}
	// A tenth in, the forecast shows.
	if w := readWindow(week7(3, now.Add(time.Duration(0.9*float64(week)))), now, now); w.State != StateUnder || w.Forecast.Percent != 30 {
		t.Fatalf("a tenth in: %s %+v", w.State, w.Forecast)
	}
}

func TestForecastOfAStaleReading(t *testing.T) {
	// Read a day ago, 40% into the week: the forecast is of the reading,
	// and the reading is marked old.
	at := now.Add(-24 * time.Hour)
	reset := at.Add(time.Duration(0.6 * float64(week)))
	w := readWindow(week7(62.8, reset), at, now)
	if !w.Stale || w.Reset || w.State != StateOver || w.Forecast.Percent != 157 || !w.ObservedAt.Equal(at) {
		t.Fatalf("stale = %+v %+v", w, w.Forecast)
	}
	if w := readWindow(week7(62.8, reset), now.Add(-6*time.Hour), now); w.Stale {
		t.Fatal("a reading 6 hours old is stale")
	}
	// A full window stays full until it resets.
	if w := readWindow(week7(100, reset), at, now); w.Stale || w.State != StateOut {
		t.Fatalf("a full window read a day ago: %+v", w)
	}
}

func TestForecastOfAWindowThatReset(t *testing.T) {
	at := now.Add(-3 * 24 * time.Hour)
	for _, pct := range []float64{100, 60, 0} {
		w := readWindow(week7(pct, now.Add(-time.Minute)), at, now)
		if !w.Reset || w.State != StateUnknown || w.Forecast != nil {
			t.Errorf("%v%% before the reset: %+v", pct, w)
		}
	}
	// Resetting exactly now counts as reset.
	if w := readWindow(week7(60, now), at, now); !w.Reset {
		t.Fatal("a window that resets now has not reset")
	}
}

func TestForecastNeedsTheWindow(t *testing.T) {
	reset := now.Add(24 * time.Hour)
	for name, w := range map[string]snapshot.Window{
		"no reset":  {Name: "7d", Percent: 50, Minutes: 10080},
		"no length": {Name: "credits", Percent: 50, ResetsAt: &reset},
	} {
		if r := readWindow(w, now, now); r.State != StateUnknown || r.Forecast != nil {
			t.Errorf("%s: %s %+v", name, r.State, r.Forecast)
		}
	}
	// Out is out without a forecast.
	if r := readWindow(snapshot.Window{Name: "credits", Percent: 100}, now, now); r.State != StateOut {
		t.Fatalf("full with no reset: %s", r.State)
	}
	// The length comes from the name when the harness gives none.
	if r := readWindow(snapshot.Window{Name: "5h", Percent: 50, ResetsAt: tp(now.Add(150 * time.Minute))}, now, now); r.State != StateOver || r.Forecast.Percent != 100 {
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
		if got := mainIndex(ws); got != c.want {
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
		ws, state := readQuota(readings(c.ws, now), now)
		var rows []string
		m := mainWindow(ws)
		for _, w := range ws {
			if limitsMore(w, *m) {
				rows = append(rows, w.Name)
			}
		}
		if state != c.want || len(rows) != len(c.rows) || (len(rows) > 0 && rows[0] != c.rows[0]) {
			t.Errorf("%s: state %s rows %v, want %s %v", c.name, state, rows, c.want, c.rows)
		}
		if (m == nil) != (len(c.ws) == 0) {
			t.Errorf("%s: main window %+v", c.name, m)
		}
	}
}
