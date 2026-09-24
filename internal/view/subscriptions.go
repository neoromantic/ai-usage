package view

import (
	"math"
	"strconv"
	"strings"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// subRow is one row of SUBSCRIPTIONS: an account, or one of its windows
// that limits it more than its main window does.
type subRow struct {
	window  bool // a window's row, under its account
	here    bool
	name    string
	plan    string
	win     *Window // the account's main window, or the row's; nil with no reading
	users   int
	busiest string
	last    string
}

type subGroup struct {
	provider string
	rows     []subRow
}

// subLayout is the widths of the SUBSCRIPTIONS columns, and which of the
// columns that drop as the page narrows are still shown.
type subLayout struct {
	acct, plan, bar, left, cd, clock, at, count, busiest, last int

	showPlan, showClock, showBusiest, showLast bool
}

func (l subLayout) resets() int {
	w := l.cd
	if l.showClock && l.clock > 0 {
		w += 1 + l.clock
	}
	return max(w, width("RESETS"))
}

func (l subLayout) users() int {
	w := l.count
	if l.showBusiest && l.busiest > 0 {
		w += 2 + l.busiest
	}
	return max(w, width("USERS"))
}

func (l subLayout) width() int {
	n := 2 + l.acct + 2 + l.bar + 2 + l.left + 2 + l.resets() + 2 + l.at + 2 + l.users()
	if l.showPlan {
		n += 2 + l.plan
	}
	if l.showLast {
		n += 2 + l.last
	}
	return n
}

const (
	barCells    = 24
	narrowBar   = 12
	planWidth   = 10
	minAccount  = 12
	windowShift = 2 // how far a window's row is indented under its account
)

// subscriptions is how much is left on each subscription, when it resets,
// and how full it will be then, one provider at a time.
func (p *page) subscriptions() []chunks {
	g := p.g
	var groups []subGroup
	total, noReading := 0, 0
	counts := map[string]int{}
	for _, tp := range p.r.Team.Providers {
		grp := subGroup{provider: tp.Provider}
		for i := range tp.Accounts {
			a := &tp.Accounts[i]
			if !a.Subscription {
				continue
			}
			total++
			var main *Window
			if a.Quota != nil {
				main = mainWindow(a.Quota.Windows)
			}
			if a.State == StateUnknown || a.State == "" {
				noReading++
			} else {
				counts[a.State]++
			}
			label := a.Label
			if a.Alias != nil && a.Name == *a.Alias {
				label = *a.Alias
			}
			row := subRow{here: a.Current, name: p.txt(shortID(label)), win: main, users: a.Users, last: g.none}
			if a.Plan != nil {
				row.plan = p.txt(*a.Plan)
			}
			if a.Busiest != nil {
				row.busiest = p.txt(*a.Busiest)
			}
			if a.LastActiveAt != nil {
				row.last = age(p.now.Sub(*a.LastActiveAt))
			}
			grp.rows = append(grp.rows, row)
			if main == nil {
				continue
			}
			for j := range a.Quota.Windows {
				if w := &a.Quota.Windows[j]; limitsMore(*w, *main) {
					grp.rows = append(grp.rows, subRow{window: true, name: p.txt(trimLength(w.Name, main.Name)), win: w})
				}
			}
		}
		if len(grp.rows) > 0 {
			groups = append(groups, grp)
		}
	}

	title := chunks{p.bold("SUBSCRIPTIONS"), p.plain("  "), p.muted(strconv.Itoa(total))}
	for _, s := range []string{StateOut, StateOver, StateTight} {
		if n := counts[s]; n > 0 {
			title = append(title, p.muted(g.sep), p.inState(strconv.Itoa(n)+" "+s, s))
		}
	}
	if noReading > 0 {
		title = append(title, p.muted(g.sep), p.muted(strconv.Itoa(noReading)+" no reading"))
	}
	if total == 0 {
		return []chunks{title, {p.space(2), p.muted("no subscription has been used on this team's devices yet")}}
	}

	l := p.layoutSubs(groups)
	out := []chunks{title}
	for i, grp := range groups {
		out = append(out, nil)
		if i == 0 {
			out = append(out, p.subHeader(l, grp.provider))
		} else {
			out = append(out, chunks{p.space(2), p.bold(strings.ToUpper(grp.provider))})
		}
		for _, r := range grp.rows {
			out = append(out, p.subLine(l, r))
		}
	}
	return out
}

// layoutSubs sizes the columns to their cells, then drops columns as the
// page narrows: LAST, then PLAN, then the reset's clock, then the busiest
// user, and last the bar shrinks to half. An account still too wide is cut
// in the middle.
func (p *page) layoutSubs(groups []subGroup) subLayout {
	l := subLayout{bar: barCells, left: width("LEFT"), at: width("AT RESET"), count: 2, last: width("LAST"),
		showPlan: true, showClock: true, showBusiest: true, showLast: true}
	for _, grp := range groups {
		l.acct = max(l.acct, width(grp.provider))
		for _, r := range grp.rows {
			name := width(r.name)
			if r.window {
				name += windowShift
			}
			l.acct = max(l.acct, name)
			l.plan = max(l.plan, min(width(r.plan), planWidth))
			l.left = max(l.left, width(p.leftText(r.win)))
			cd, clock := p.resetsText(r.win)
			l.cd, l.clock = max(l.cd, width(cd)), max(l.clock, width(clock))
			l.at = max(l.at, width(p.atResetText(r.win)))
			if !r.window {
				l.count = max(l.count, width(strconv.Itoa(r.users)))
				l.busiest = max(l.busiest, width(r.busiest))
				l.last = max(l.last, width(r.last))
			}
		}
	}
	if l.plan > 0 {
		l.plan = max(l.plan, width("PLAN"))
	} else {
		l.showPlan = false
	}
	for _, drop := range []func(){
		func() { l.showLast = false },
		func() { l.showPlan = false },
		func() { l.showClock = false },
		func() { l.showBusiest = false },
		func() { l.bar = narrowBar },
	} {
		if l.width() <= p.w {
			break
		}
		drop()
	}
	if over := l.width() - p.w; over > 0 {
		l.acct = max(l.acct-over, minAccount)
	}
	return l
}

// subHeader is the first provider's heading, with the column headers.
func (p *page) subHeader(l subLayout, provider string) chunks {
	out := chunks{p.space(2)}
	out = append(out, p.left(p.bold(strings.ToUpper(provider)), l.acct)...)
	if l.showPlan {
		out = append(out, p.space(2))
		out = append(out, p.left(p.muted("PLAN"), l.plan)...)
	}
	out = append(out, p.space(2))
	out = append(out, p.left(p.muted("THIS WEEK"), l.bar)...)
	out = append(out, p.space(2))
	out = append(out, p.right(p.muted("LEFT"), l.left)...)
	out = append(out, p.space(2))
	out = append(out, p.left(p.muted("RESETS"), l.resets())...)
	out = append(out, p.space(2))
	out = append(out, p.right(p.muted("AT RESET"), l.at)...)
	out = append(out, p.space(2))
	out = append(out, p.left(p.muted("USERS"), l.users())...)
	if l.showLast {
		out = append(out, p.space(2))
		out = append(out, p.right(p.muted("LAST"), l.last)...)
	}
	return out
}

func (p *page) subLine(l subLayout, r subRow) chunks {
	g := p.g
	out := chunks{p.space(1), p.space(1)}
	if r.here {
		out[0] = p.accent(g.here)
		p.mark("here")
	}
	if r.window {
		out = append(out, p.space(windowShift))
		out = append(out, p.left(p.muted(truncEnd(r.name, l.acct-windowShift, g.ell)), l.acct-windowShift)...)
	} else {
		out = append(out, p.left(p.plain(truncMid(r.name, l.acct, g.ell)), l.acct)...)
	}
	if l.showPlan {
		out = append(out, p.space(2))
		out = append(out, p.left(p.muted(truncEnd(r.plan, l.plan, g.ell)), l.plan)...)
	}
	out = append(out, p.space(2))
	out = append(out, p.bar(r.win, l.bar)...)
	out = append(out, p.space(2))
	out = append(out, p.right(p.leftCell(r.win), l.left)...)
	out = append(out, p.space(2))
	var resets chunks
	if cd, clock := p.resetsText(r.win); cd != "" {
		resets = p.left(p.plain(cd), l.cd)
		if l.showClock {
			resets = append(resets, p.space(1), p.muted(clock))
		}
	}
	out = append(out, resets.padTo(l.resets())...)
	out = append(out, p.space(2))
	out = append(out, p.right(p.atReset(r.win), l.at)...)
	if r.window {
		return out
	}
	out = append(out, p.space(2))
	count := strconv.Itoa(r.users)
	if r.users == 0 {
		count = g.none
		p.mark("none")
	}
	if l.showBusiest && l.busiest > 0 {
		users := p.right(p.plain(count), l.count)
		users = append(users, p.space(2), p.muted(truncEnd(r.busiest, l.busiest, g.ell)))
		out = append(out, users.padTo(l.users())...)
	} else {
		out = append(out, p.right(p.plain(count), l.users())...)
	}
	if l.showLast {
		if r.last == g.none {
			p.mark("none")
		}
		out = append(out, p.space(2))
		out = append(out, p.right(p.muted(r.last), l.last)...)
	}
	return out
}

// known says how full a window is now is known: it has a reading, the
// window has not reset since, and it was read since any refusal.
func known(w *Window) bool { return w != nil && !w.Reset && !unread(w) }

// unread says a window was not read since Claude refused a request: how
// full it is now is not known.
func unread(w *Window) bool { return w != nil && w.Unread }

// leftText is what is left of a window: a whole percent, ~ before it for an
// old reading, and ? when it is not known.
func (p *page) leftText(w *Window) string {
	if !known(w) {
		return "?"
	}
	s := strconv.Itoa(max(100-int(math.Floor(w.Percent)), 0)) + "%"
	if w.Stale {
		s = "~" + s
	}
	return s
}

func (p *page) leftCell(w *Window) chunk {
	s := p.leftText(w)
	switch {
	case w != nil && unread(w):
		p.mark("unread")
		return p.muted(s)
	case !known(w):
		p.mark("unknown")
		return p.muted(s)
	case w.Stale:
		p.mark("stale")
	}
	return p.plain(s)
}

// resetsText is the countdown to a window's reset and its local clock.
func (p *page) resetsText(w *Window) (string, string) {
	if w == nil || w.Reset || w.ResetsAt == nil {
		return "", ""
	}
	return dur(w.ResetsAt.Sub(p.now)), p.clock(*w.ResetsAt)
}

// atResetText is the forecast: `157% over`, `81% ok`, `out`, or a dash
// when there is none.
func (p *page) atResetText(w *Window) string {
	switch {
	case !known(w):
		return p.g.dash
	case w.State == StateOut:
		return "out"
	case w.Forecast == nil || w.State == StateUnknown || w.State == "":
		return p.g.dash
	}
	s := strconv.Itoa(int(w.Forecast.Percent)) + "% " + w.State
	if w.Stale {
		s = "~" + s
	}
	return s
}

func (p *page) atReset(w *Window) chunk {
	s := p.atResetText(w)
	if s == p.g.dash {
		p.mark("dash")
		return p.faint(s)
	}
	return p.inState(s, w.State)
}

// elapsed is how much of a window had passed when it was read, from 0 to 1.
func elapsed(w Window) (float64, bool) {
	if w.Forecast != nil {
		return w.Forecast.Elapsed, true
	}
	sw := snapshot.Window{Name: w.Name, Minutes: w.Minutes, ResetsAt: w.ResetsAt}
	start, ok := sw.Start()
	if !ok || w.ObservedAt.IsZero() {
		return 0, false
	}
	e := float64(w.ObservedAt.Sub(start)) / float64(sw.Length())
	return min(max(e, 0), 1), true
}

// bar is a window as n cells: the used part in the color of its forecast,
// the rest faint, and a tick where even use of the window would be by its
// reading. A window with no reading is dotted.
func (p *page) bar(w *Window, n int) chunks {
	g := p.g
	if !known(w) {
		p.mark("dotted")
		return chunks{p.faint(strings.Repeat(g.unread, n))}
	}
	used := int(math.Round(w.Percent / 100 * float64(n)))
	switch {
	case w.Percent >= 100:
		used = n
	case w.Percent > 0 && used == 0:
		used = 1
	case used >= n:
		used = n - 1
	}
	used = max(used, 0)
	tick := -1
	if e, ok := elapsed(*w); ok {
		tick = min(int(e*float64(n)), n-1)
	}
	color := p.th.State(w.State)
	if w.State == StateUnknown || w.State == "" {
		color = p.th.Muted
	}
	var out chunks
	run := func(s string, k int, c chunk) {
		if k > 0 {
			c.text = strings.Repeat(s, k)
			out = append(out, c)
		}
	}
	usedPart := func(k int) {
		if k > 0 {
			p.mark("used")
		}
		run(g.used, k, p.paint("", color, false, false))
	}
	leftPart := func(k int) {
		if k > 0 {
			p.mark("left")
		}
		run(g.left, k, p.faint(""))
	}
	if tick < 0 {
		usedPart(used)
		leftPart(n - used)
		return out
	}
	// The tick is bright: bold in the terminal's own color.
	tickMark := chunk{text: g.tick}
	if p.o.Color {
		tickMark = p.bold(g.tick)
	}
	if tick < used {
		tickMark.text = g.tickIn
		p.mark("tickIn")
		usedPart(tick)
		out = append(out, tickMark)
		usedPart(used - tick - 1)
		leftPart(n - used)
		return out
	}
	p.mark("tick")
	usedPart(used)
	leftPart(tick - used)
	out = append(out, tickMark)
	leftPart(n - tick - 1)
	return out
}
