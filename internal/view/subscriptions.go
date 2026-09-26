package view

import (
	"strconv"
	"strings"
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
			rows := p.subRows(a)
			// An account with a reading of its main window but no forecast
			// yet, as in the first tenth of the window, counts in neither.
			switch {
			case a.State != StateUnknown && a.State != "":
				counts[a.State]++
			case !known(rows[0].win):
				noReading++
			}
			grp.rows = append(grp.rows, rows...)
		}
		if len(grp.rows) > 0 {
			groups = append(groups, grp)
		}
	}

	title := p.subTitle(total, noReading, counts)
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

// subRows is an account's rows: its own, with its main window (nil without
// a quota), then one for each window that limits it more.
func (p *page) subRows(a *TeamAccount) []subRow {
	var main *Window
	if a.Quota != nil {
		main = mainWindow(a.Quota.Windows)
	}
	row := subRow{here: a.Current, name: p.txt(shownLabel(a)), win: main, users: a.Users, last: p.g.none}
	if a.Plan != nil {
		row.plan = p.txt(*a.Plan)
	}
	if a.Busiest != nil {
		row.busiest = p.txt(*a.Busiest)
	}
	if a.LastActiveAt != nil {
		row.last = age(p.now.Sub(*a.LastActiveAt))
	}
	rows := []subRow{row}
	if main == nil {
		return rows
	}
	for j := range a.Quota.Windows {
		if w := &a.Quota.Windows[j]; limitsMore(*w, *main) {
			rows = append(rows, subRow{window: true, name: p.txt(trimLength(w.Name, main.Name)), win: w})
		}
	}
	return rows
}

// subTitle is the section's title: how many subscriptions there are, and how
// many of them are out, over, tight or without a reading.
func (p *page) subTitle(total, noReading int, counts map[string]int) chunks {
	g := p.g
	title := chunks{p.bold("SUBSCRIPTIONS"), p.plain("  "), p.muted(strconv.Itoa(total))}
	for _, s := range []string{StateOut, StateOver, StateTight} {
		if n := counts[s]; n > 0 {
			title = append(title, p.muted(g.sep), p.inState(strconv.Itoa(n)+" "+s, s))
		}
	}
	if noReading > 0 {
		title = append(title, p.muted(g.sep), p.muted(strconv.Itoa(noReading)+" no reading"))
	}
	return title
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
	for _, c := range []struct {
		head        string
		w           int
		right, show bool
	}{
		{"PLAN", l.plan, false, l.showPlan},
		{"THIS WEEK", l.bar, false, true},
		{"LEFT", l.left, true, true},
		{"RESETS", l.resets(), false, true},
		{"AT RESET", l.at, true, true},
		{"USERS", l.users(), false, true},
		{"LAST", l.last, true, l.showLast},
	} {
		if !c.show {
			continue
		}
		out = append(out, p.space(2))
		if c.right {
			out = append(out, p.right(p.muted(c.head), c.w)...)
		} else {
			out = append(out, p.left(p.muted(c.head), c.w)...)
		}
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
