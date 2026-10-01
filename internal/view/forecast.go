package view

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// MaxForecast caps a forecast, so one early burst does not print a number
// that means nothing.
const MaxForecast = 999

// readWindow is a window read at `at` as the report sees it at now: how old
// the reading is, whether the window reset since, and its forecast, at the
// pace its readings pts show.
func readWindow(w snapshot.Window, at, now time.Time, pts []pacePoint) Window {
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
	win.Forecast = forecast(w, at, pts)
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

// Forecast bases: the pace a forecast takes.
const (
	BasisWindow = "window"
	BasisRecent = "recent"
)

// The pace of the recent readings is the least-squares slope of those over
// slopeSpan before the reading, when they span slopeMin or more. A drop of
// more than dropPoints between two readings of one period starts the series
// again: the provider took use back.
const (
	slopeSpan  = 24 * time.Hour
	slopeMin   = time.Hour
	dropPoints = 2
)

// forecast is how full w will be at its reset if it is used from its
// reading on at its pace: the faster of its average since it began, or
// since its last reading at 0% after that, and the pace of its readings of
// the last day, pts with the reading itself. There is none without the
// window's length and reset time, and none in its first tenth unless it is
// over already.
func forecast(w snapshot.Window, at time.Time, pts []pacePoint) *Forecast {
	start, ok := w.Start()
	if !ok {
		return nil
	}
	reset := *w.ResetsAt
	elapsed := float64(at.Sub(start)) / float64(w.Length())
	elapsed = min(max(elapsed, 0), 1)
	used := max(w.Percent, 0)
	series := paceSeries(w, at, pts)
	// The average runs from the window's start, or from the last reading
	// at 0% after it: a window left idle at first fills from there.
	from := start
	for _, p := range series {
		if p.Percent <= 0 && p.At.After(from) {
			from = p.At
		}
	}
	left := reset.Sub(at).Hours()
	f := &Forecast{Elapsed: elapsed, Basis: BasisWindow}
	// avg is the forecast at the average pace: used ÷ the share of the time
	// from `from` to the reset that had passed at the reading.
	avg := float64(MaxForecast)
	switch {
	case used == 0:
		avg = 0
	case at.After(from):
		avg = used / (float64(at.Sub(from)) / float64(reset.Sub(from)))
		f.Pace = used / at.Sub(from).Hours()
	}
	raw := avg
	if slope, ok := recentSlope(series, at); ok && used+slope*left > avg {
		raw, f.Pace, f.Basis = used+slope*left, slope, BasisRecent
	}
	f.Percent = min(math.Round(raw), MaxForecast)
	if elapsed < 0.1 && f.Percent < 100 {
		return nil
	}
	// It runs out before its reset when the forecast is over 100% before it
	// is rounded, so a forecast of 100.4% runs out too. At exactly 100% it
	// runs out at its reset. With no time elapsed, the pace is infinite.
	if used > 0 && used < 100 && raw > 100 {
		var runs time.Time
		if f.Basis == BasisRecent {
			runs = at.Add(time.Duration((100 - used) / f.Pace * float64(time.Hour)))
		} else {
			// At the same pace, used grows to 100 in 100/used of the time
			// it took to reach used.
			runs = from.Add(time.Duration(100 / used * float64(at.Sub(from))))
		}
		if runs.Before(reset) {
			f.RunsOutAt = timePtr(runs)
		}
	}
	return f
}

// pacePoint is one reading of a window, from this device's samples or the
// team's reads.
type pacePoint struct {
	At       time.Time
	Window   string
	ResetsAt *time.Time
	Percent  float64
}

// paceSeries is the readings of w's current period up to its reading at at,
// that reading included, oldest first and one per time, from the last drop
// of more than dropPoints on: those of pts of the same window that reset
// when it does, give or take a minute.
func paceSeries(w snapshot.Window, at time.Time, pts []pacePoint) []pacePoint {
	var out []pacePoint
	for _, p := range pts {
		if p.Window == w.Name && p.ResetsAt != nil && p.ResetsAt.Sub(*w.ResetsAt).Abs() <= time.Minute && p.At.Before(at) {
			out = append(out, p)
		}
	}
	out = append(out, pacePoint{At: at, Window: w.Name, ResetsAt: w.ResetsAt, Percent: w.Percent})
	slices.SortStableFunc(out, func(a, b pacePoint) int { return a.At.Compare(b.At) })
	// Of readings at one time, the last added counts.
	out = slices.Clip(out)
	for i := len(out) - 1; i > 0; i-- {
		if out[i].At.Equal(out[i-1].At) {
			out = slices.Delete(out, i-1, i)
		}
	}
	for i := len(out) - 1; i > 0; i-- {
		if out[i].Percent < out[i-1].Percent-dropPoints {
			return out[i:]
		}
	}
	return out
}

// recentSlope is the pace of the readings of the slopeSpan before at, in
// points an hour, at least 0: the least-squares slope of percent over time.
// It is false when they span less than slopeMin.
func recentSlope(series []pacePoint, at time.Time) (float64, bool) {
	var ps []pacePoint
	for _, p := range series {
		if !p.At.Before(at.Add(-slopeSpan)) {
			ps = append(ps, p)
		}
	}
	if len(ps) < 2 || ps[len(ps)-1].At.Sub(ps[0].At) < slopeMin {
		return 0, false
	}
	var mx, my float64
	for _, p := range ps {
		mx += p.At.Sub(at).Hours()
		my += p.Percent
	}
	n := float64(len(ps))
	mx, my = mx/n, my/n
	var sxy, sxx float64
	for _, p := range ps {
		x := p.At.Sub(at).Hours() - mx
		sxy += x * (p.Percent - my)
		sxx += x * x
	}
	return max(sxy/sxx, 0), true
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

// stateOrder is the states from the worst. Unknown ranks after them.
var stateOrder = []string{StateOut, StateOver, StateTight, StateOK, StateUnder}

// stateRank is a state's place in stateOrder.
func stateRank(s string) int {
	if i := slices.Index(stateOrder, s); i >= 0 {
		return i
	}
	return len(stateOrder)
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
func mainIndex(rs []reading) int {
	if len(rs) == 0 {
		return -1
	}
	for i, r := range rs {
		if r.Name == "7d" {
			return i
		}
	}
	for i, r := range rs {
		if r.Length() == week {
			return i
		}
	}
	best := 0
	for i, r := range rs {
		if r.Length() > rs[best].Length() {
			best = i
		}
	}
	return best
}

// limitsMore says a window that is not the main one limits the account more
// than the main one does, so the report marks it as one that limits the
// account: it is out, it is over, or it is fuller. A reset window limits
// nothing it knows.
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

// readQuota reads every window at the pace its readings among pts show, marks the main one and those that limit the
// account, and gives the state of the account: the worst state of the
// windows that limit it.
func readQuota(rs []reading, pts []pacePoint, now time.Time) ([]Window, string) {
	out := make([]Window, 0, len(rs))
	m := mainIndex(rs)
	for i, r := range rs {
		win := readWindow(r.Window, r.At, now, pts)
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
	for i := range out {
		out[i].Limits = i == m || limitsMore(out[i], out[m])
		if out[i].Limits {
			state = worse(state, out[i].State)
		}
	}
	return out, state
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

var lengthPrefix = regexp.MustCompile(`^(\d+[mhd]) (.+)$`)

// trimLength drops the length before a window's name when it is the main
// window's length: "7d Fable" is "Fable" beside "7d".
func trimLength(name, mainName string) string {
	m := lengthPrefix.FindStringSubmatch(name)
	if m == nil {
		return name
	}
	if mm := lengthPrefix.FindStringSubmatch(mainName + " x"); mm != nil && mm[1] == m[1] {
		return m[2]
	}
	return name
}

// knownMain is a quota's main window when how full it is now is known, or
// nil.
func knownMain(q *Quota) *Window {
	if q == nil {
		return nil
	}
	if m := mainWindow(q.Windows); known(m) {
		return m
	}
	return nil
}

// known says how full a window is now is known: it has a reading, the
// window has not reset since, and it was read since any refusal.
func known(w *Window) bool { return w != nil && !w.Reset && !w.Unread }
