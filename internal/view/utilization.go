package view

import (
	"math"
	"strconv"
	"strings"
)

// utilRow is one row of UTILIZATION: an account with its main window's
// history, or another of its windows, under it.
type utilRow struct {
	window bool // a window's row, under its account
	here   bool
	name   string
	u      *Utilization // nil on an account whose main window has none
}

type utilGroup struct {
	provider string
	rows     []utilRow
}

// utilLayout is the widths of the UTILIZATION columns, how many periods the
// history shows, and whether CYCLES, the first column to drop as the page
// narrows, is still shown.
type utilLayout struct {
	acct, cycles, out, avg, hist int

	showCycles bool
}

const (
	// cycleCell is how wide a period's percent is in the history, and
	// minHistory how few periods it shows before columns drop.
	cycleCell  = 4
	minHistory = 4
)

// fixed is how wide the row is without the history.
func (l utilLayout) fixed() int {
	n := 2 + l.acct + 2 + l.out + 2 + l.avg + 2
	if l.showCycles {
		n += 2 + l.cycles
	}
	return n
}

// fits is how many periods fit in w columns after the rest of the row.
func (l utilLayout) fits(w int) int { return max((w-l.fixed()+1)/(cycleCell+1), 0) }

// utilization is how full each subscription's windows were before each of
// their resets, one provider at a time, as SUBSCRIPTIONS groups them: an
// account's main window on its row, and its other windows with a period
// that ended under it. It is left out when no window has one.
func (p *page) utilization() []chunks {
	var groups []utilGroup
	most := 0
	for _, tp := range p.r.Team.Providers {
		grp := utilGroup{provider: tp.Provider}
		for i := range tp.Accounts {
			a := &tp.Accounts[i]
			if !a.Subscription || len(a.Utilization) == 0 {
				continue
			}
			row := utilRow{here: a.Current, name: p.txt(shownLabel(a))}
			rest := a.Utilization
			main := rest[0].Window
			if rest[0].Main {
				row.u, rest = &rest[0], rest[1:]
			} else if a.Quota != nil && mainWindow(a.Quota.Windows) != nil {
				main = mainWindow(a.Quota.Windows).Name
			}
			grp.rows = append(grp.rows, row)
			for j := range rest {
				grp.rows = append(grp.rows, utilRow{window: true, name: p.txt(trimLength(rest[j].Window, main)), u: &rest[j]})
			}
		}
		for _, r := range grp.rows {
			if r.u != nil {
				most = max(most, len(r.u.Cycles))
			}
		}
		if len(grp.rows) > 0 {
			groups = append(groups, grp)
		}
	}
	if len(groups) == 0 {
		return nil
	}
	l := p.layoutUtil(groups, most)
	out := []chunks{{p.bold("UTILIZATION"), p.plain("  "), p.muted("fullest % before each reset")}}
	// Why the team gets no cycles but this device's, cut with what to do
	// kept whole, as the legend cuts why totals are estimated.
	if r := p.r.Team.CyclesReason; r != nil {
		const only = "only this device's cycles: "
		why := cutReason(p.txt(strings.ReplaceAll(*r, "…", p.g.ell)), p.w-2-width(only), p.g.ell)
		out = append(out, chunks{p.space(2), p.muted(only + why)})
	}
	for i, grp := range groups {
		out = append(out, nil)
		if i == 0 {
			out = append(out, p.utilHeader(l, grp.provider))
		} else {
			out = append(out, chunks{p.space(2), p.bold(strings.ToUpper(grp.provider))})
		}
		for _, r := range grp.rows {
			out = append(out, p.utilLine(l, r))
		}
	}
	return out
}

// layoutUtil sizes the columns to their cells, and gives the history the
// rest of the width, up to the most periods a row has. As the page narrows,
// the history shows fewer periods, down to minHistory; then CYCLES drops,
// and an account still too wide is cut in the middle.
func (p *page) layoutUtil(groups []utilGroup, most int) utilLayout {
	l := utilLayout{cycles: width("CYCLES"), out: width("OUT"), avg: width("AVG"), showCycles: true}
	for _, grp := range groups {
		l.acct = max(l.acct, width(grp.provider))
		for _, r := range grp.rows {
			name := width(r.name)
			if r.window {
				name += windowShift
			}
			l.acct = max(l.acct, name)
			if r.u != nil {
				l.cycles = max(l.cycles, width(strconv.Itoa(len(r.u.Cycles))))
				l.out = max(l.out, width(strconv.Itoa(r.u.Out)))
				l.avg = max(l.avg, width(wholePercent(r.u.Average)+"%"))
			}
		}
	}
	want := min(most, minHistory)
	if l.fits(p.w) < want {
		l.showCycles = false
	}
	if over := want - l.fits(p.w); over > 0 {
		l.acct = max(l.acct-over*(cycleCell+1), minAccount)
	}
	l.hist = max(min(most, l.fits(p.w)), 1)
	return l
}

// utilHeader is the first provider's heading, with the column headers.
func (p *page) utilHeader(l utilLayout, provider string) chunks {
	out := chunks{p.space(2)}
	out = append(out, p.left(p.bold(strings.ToUpper(provider)), l.acct)...)
	if l.showCycles {
		out = append(out, p.space(2))
		out = append(out, p.right(p.muted("CYCLES"), l.cycles)...)
	}
	out = append(out, p.space(2))
	out = append(out, p.right(p.muted("OUT"), l.out)...)
	out = append(out, p.space(2))
	out = append(out, p.right(p.muted("AVG"), l.avg)...)
	out = append(out, p.space(2))
	head := "NEWEST FIRST"
	if room := l.hist*(cycleCell+1) - 1; width(head) > room {
		head = "NEWEST"
	}
	return append(out, p.muted(head))
}

func (p *page) utilLine(l utilLayout, r utilRow) chunks {
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
	u := r.u
	if u == nil {
		return out
	}
	if l.showCycles {
		out = append(out, p.space(2))
		out = append(out, p.right(p.plain(strconv.Itoa(len(u.Cycles))), l.cycles)...)
	}
	out = append(out, p.space(2))
	if u.Out > 0 {
		out = append(out, p.right(p.inState(strconv.Itoa(u.Out), StateOut), l.out)...)
	} else {
		out = append(out, p.right(p.muted(g.none), l.out)...)
		p.mark("none")
	}
	out = append(out, p.space(2))
	out = append(out, p.right(p.inState(wholePercent(u.Average)+"%", cycleState(u.Average)), l.avg)...)
	out = append(out, p.space(1))
	for i, c := range u.Cycles {
		if i == l.hist {
			break
		}
		out = append(out, p.space(1))
		cell := chunks{p.inState(wholePercent(c.Percent), c.State)}
		if c.Partial {
			cell = append(chunks{p.muted(g.silent)}, cell...)
			p.mark("partial")
		}
		out = append(out, p.align(cell, cycleCell, true)...)
	}
	return out
}

// wholePercent is a percent as a whole number, up to 999.
func wholePercent(v float64) string {
	return strconv.Itoa(min(int(math.Round(v)), 999))
}
