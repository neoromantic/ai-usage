package view

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// MaxForecast caps a forecast, so one early burst does not print a number
// that means nothing.
const MaxForecast = 999

// readWindow is a window read at `at` as the report sees it at now: how old
// the reading is, whether the window reset since, and its forecast.
func readWindow(w snapshot.Window, at, now time.Time) Window {
	win := Window{
		Name:       w.Name,
		Percent:    w.Percent,
		ResetsAt:   timeOf(w.ResetsAt),
		Minutes:    w.Minutes,
		ObservedAt: at.UTC(),
		Stale:      now.Sub(at) > StaleAfter && w.Percent < 100,
		State:      StateUnknown,
	}
	if w.ResetsAt != nil && !w.ResetsAt.After(now) {
		// The percent is from a period that has ended. How full the new
		// one is, nobody has read.
		win.Reset = true
		return win
	}
	win.Forecast = forecast(w, at)
	switch {
	case w.Percent >= 100:
		win.State = StateOut
		if win.Forecast != nil {
			win.Forecast.RunsOutAt = nil
		}
	case win.Forecast != nil:
		win.State = forecastState(win.Forecast.Percent)
	}
	return win
}

// forecast is how full w will be at its reset if it is used from its
// reading on at its average pace so far: used ÷ elapsed. There is none
// without the window's length and reset time, and none in its first tenth
// unless it is over already.
func forecast(w snapshot.Window, at time.Time) *Forecast {
	start, ok := w.Start()
	if !ok {
		return nil
	}
	length := w.Length()
	elapsed := float64(at.Sub(start)) / float64(length)
	elapsed = min(max(elapsed, 0), 1)
	used := max(w.Percent, 0)
	pct := float64(MaxForecast)
	if elapsed > 0 {
		pct = min(math.Round(used/elapsed), MaxForecast)
	} else if used == 0 {
		pct = 0
	}
	if elapsed < 0.1 && pct < 100 {
		return nil
	}
	f := &Forecast{Percent: pct, Elapsed: elapsed}
	if pct > 100 && used > 0 && used < 100 {
		// At the same pace, used grows to 100 in 100/used of the time it
		// took to reach used.
		runs := start.Add(time.Duration(100 / used * float64(at.Sub(start))))
		f.RunsOutAt = timePtr(runs)
	}
	return f
}

// forecastState is the band a forecast falls in.
func forecastState(pct float64) string {
	switch {
	case pct >= 100:
		return StateOver
	case pct >= 85:
		return StateTight
	case pct >= 50:
		return StateOK
	default:
		return StateUnder
	}
}

// stateRank orders states from the worst: out, over, tight, ok, under, then
// unknown.
func stateRank(s string) int {
	switch s {
	case StateOut:
		return 0
	case StateOver:
		return 1
	case StateTight:
		return 2
	case StateOK:
		return 3
	case StateUnder:
		return 4
	default:
		return 5
	}
}

// worse is the worse of two states.
func worse(a, b string) string {
	if stateRank(b) < stateRank(a) {
		return b
	}
	return a
}

const week = 7 * 24 * time.Hour

// mainIndex is the index of a quota's main window, the weekly one: the one
// named 7d, else the first a week long, else the longest, else the first.
// It is -1 with no windows.
func mainIndex(ws []snapshot.Window) int {
	if len(ws) == 0 {
		return -1
	}
	for i, w := range ws {
		if w.Name == "7d" {
			return i
		}
	}
	for i, w := range ws {
		if w.Length() == week {
			return i
		}
	}
	best := 0
	for i, w := range ws {
		if w.Length() > ws[best].Length() {
			best = i
		}
	}
	return best
}

// limitsMore says a window that is not the main one limits the account more
// than the main one does, so the report shows it as a row of its own: it is
// out, it is over, or it is fuller. A reset window limits nothing it knows.
func limitsMore(w, main Window) bool {
	if w.Main || w.Reset {
		return false
	}
	if w.State == StateOut || w.State == StateOver {
		return true
	}
	if w.Percent <= 0 {
		return false
	}
	return main.Reset || w.Percent > main.Percent
}

// reading is a window and when it was read. In the team view the windows of
// one account can come from different readings.
type reading struct {
	snapshot.Window
	At time.Time
	// Unread is a window the reading does not cover.
	Unread bool
}

// claudeWindows are the windows every Claude account has.
var claudeWindows = []snapshot.Window{{Name: "5h", Minutes: 5 * 60}, {Name: "7d", Minutes: 7 * 24 * 60}}

// withUnread adds, unread, the windows a Claude reading lacks. Claude always
// has a 5-hour and a weekly window, so a reading without one came from a
// request refused for a full window, which reads that window alone. The
// window it lacks then is not known, which is not the same as none.
func withUnread(provider string, rs []reading) []reading {
	if provider != "claude" || len(rs) == 0 {
		return rs
	}
	at := rs[0].At
	for _, r := range rs {
		if r.At.After(at) {
			at = r.At
		}
	}
	// Clipped, so what is added never lands in the caller's array.
	rs, n := slices.Clip(rs), len(rs)
	for _, w := range claudeWindows {
		if !slices.ContainsFunc(rs[:n], func(r reading) bool { return r.Name == w.Name }) {
			rs = append(rs, reading{Window: w, At: at, Unread: true})
		}
	}
	if len(rs) > n {
		slices.SortStableFunc(rs, func(a, b reading) int {
			return cmp.Or(cmp.Compare(a.Minutes, b.Minutes), strings.Compare(a.Name, b.Name))
		})
	}
	return rs
}

// readings are the windows of one reading taken at at.
func readings(ws []snapshot.Window, at time.Time) []reading {
	out := make([]reading, len(ws))
	for i, w := range ws {
		out[i] = reading{Window: w, At: at}
	}
	return out
}

// readQuota reads every window, marks the main one, and gives the state of
// the account: the worst state of the main window and of every window that
// limits more.
func readQuota(rs []reading, now time.Time) ([]Window, string) {
	ws := make([]snapshot.Window, len(rs))
	for i, r := range rs {
		ws[i] = r.Window
	}
	out := make([]Window, 0, len(rs))
	m := mainIndex(ws)
	for i, r := range rs {
		win := readWindow(r.Window, r.At, now)
		if r.Unread {
			win = Window{Name: r.Name, Minutes: r.Minutes, ObservedAt: r.At.UTC(), State: StateUnknown, Unread: true}
		}
		win.Main = i == m
		out = append(out, win)
	}
	if m < 0 {
		return out, StateUnknown
	}
	state := out[m].State
	for _, w := range out {
		if limitsMore(w, out[m]) {
			state = worse(state, w.State)
		}
	}
	return out, state
}

// anyStale says a window's reading is stale.
func anyStale(ws []Window) bool {
	for _, w := range ws {
		if w.Stale {
			return true
		}
	}
	return false
}

// mainWindow is the main window of a read quota, or nil.
func mainWindow(ws []Window) *Window {
	for i := range ws {
		if ws[i].Main {
			return &ws[i]
		}
	}
	return nil
}
