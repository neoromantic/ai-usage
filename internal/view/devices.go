package view

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// devices is who spends what: DEVICES × SUBSCRIPTIONS, or its status view,
// or USAGE on a team of one device, where a matrix of one row says little.
func (p *page) devices() []chunks {
	switch {
	case !p.deviceViews():
		return p.usage()
	case p.o.DeviceStatus:
		return p.deviceStatus()
	}
	return p.grid()
}

// deviceViews says DEVICES has two views, the matrix and the status table:
// on a team of more than one device.
func (p *page) deviceViews() bool { return len(p.r.Team.Devices) > 1 }

// viewPills are the two views of DEVICES, the one shown chosen.
func (p *page) viewPills() chunks {
	st := p.o.DeviceStatus
	return chunks{p.pill("usage", !st), p.plain(" "), p.pill("status", st)}
}

// gridCol is one column of the matrix: a subscription, or the tokens of a
// provider that have no quota.
type gridCol struct {
	c     *Column
	idx   int    // the column's index in the cells of a row
	group string // its provider, empty for no quota
	w     int
	x     int // where its cell starts on the line
	top   float64
}

const (
	gridCell    = 8  // the narrowest a matrix column is
	deviceWidth = 20 // the widest a device's name is
	// devicesHead is how many lines are over the rows of DEVICES, in the
	// matrix and in the status view alike: the title, the group headings,
	// and the column headers.
	devicesHead = 3
)

// grid is DEVICES × SUBSCRIPTIONS: a row per device and a column per
// subscription, grouped under its provider, all in byBurn's order for the
// period, with totals on the right and at the bottom. The columns that do
// not fit fold into REST, so a row's cells add up to its TOTAL. The cells
// are a heat map on a log scale; without color, the largest in each column
// is bold. The share mode shows each value as a part of its column's total:
// a subscription's column adds up to 100, and TOTAL is each device's part
// of the team's burn. A TOTAL estimated from input and output has a ~.
func (p *page) grid() []chunks {
	g := p.g
	m := p.r.Team.Matrix
	per, share := p.o.Period, p.o.Share
	var cols []gridCol
	for _, i := range columnOrder(m.Columns, per) {
		c := &m.Columns[i]
		grp := c.Provider
		if c.NoQuota {
			grp = ""
		}
		cols = append(cols, gridCol{c: c, idx: i, group: grp})
	}
	rows := append([]Row(nil), m.Rows...)
	sortRows(rows, per)
	value := func(r Row, c gridCol) float64 {
		if c.idx >= len(r.Cells) {
			return 0
		}
		cell := r.Cells[c.idx]
		if share {
			if v := per.share(cell.BurnShare); v != nil {
				return *v
			}
			return 0
		}
		return float64(per.Of(cell.Burn))
	}
	// part prints a share in the period: the none mark when its whole has
	// no tokens.
	part := func(s Share) string { return p.percent(per.share(s)) }
	// totalOf prints a total: in the share mode, all of it when it has any
	// tokens.
	totalOf := func(u Usage) string {
		switch {
		case !share:
			return p.millions(per.Of(u))
		case per.Of(u) > 0:
			return "100"
		}
		return g.none
	}
	text := func(r Row, c gridCol) string {
		if c.idx >= len(r.Cells) {
			return g.none
		}
		cell := r.Cells[c.idx]
		if share {
			return part(cell.BurnShare)
		}
		return p.millions(per.Of(cell.Burn))
	}

	var top float64
	for k := range cols {
		c := &cols[k]
		c.w = max(gridCell, width(p.txt(c.c.Name)), width(totalOf(c.c.Burn)))
		for _, r := range rows {
			c.w = max(c.w, width(text(r, *c)))
			c.top = max(c.top, value(r, *c))
		}
		top = max(top, c.top)
	}
	nameW := width("TOTAL")
	var grand Usage
	estimated := false
	for _, r := range rows {
		nameW = max(nameW, min(width(p.txt(r.Device)), deviceWidth))
		grand = grand.add(r.Burn)
		estimated = estimated || r.BurnEstimated
	}
	// est marks a total estimated.
	est := func(s string, on bool) string {
		if on && s != g.none {
			p.mark("estimated")
			return g.silent + s
		}
		return s
	}
	lead := 2 + nameW
	totalW := max(gridCell, width(totalOf(grand))+btoi(estimated))
	// A fold of columns is at most all of them.
	restW := max(gridCell, width(totalOf(grand)))
	shown, folded, edge := p.fitColumns(cols, lead, restW, totalW)
	rest := p.restColumn(cols, folded, rows)
	out := []chunks{p.gridTitle(len(rows), edge)}
	restX := edge - 2 - totalW - restW

	// The providers over their columns, each with a thin rule, and the
	// count of the columns folded into REST.
	heads := chunks{p.space(lead)}
	cur := lead
	for i := 0; i < len(shown); {
		first := cols[shown[i]]
		j := i
		for j+1 < len(shown) && cols[shown[j+1]].group == first.group {
			j++
		}
		last := cols[shown[j]]
		heads = append(heads, p.space(first.x-cur))
		heads = append(heads, p.groupHead(groupName(first.group), last.x+last.w-first.x)...)
		cur = last.x + last.w
		i = j + 1
	}
	if len(folded) > 0 {
		n := "+" + strconv.Itoa(len(folded))
		heads = append(heads, p.space(restX+restW-width(n)-cur), p.muted(n))
	}
	out = append(out, heads)

	// The subscriptions' short names, in the colors of their states.
	names := chunks{p.space(lead)}
	cur = lead
	for _, k := range shown {
		c := cols[k]
		name := p.muted(p.txt(c.c.Name))
		if !c.c.NoQuota && c.c.State != StateUnknown && c.c.State != "" {
			name = p.inState(p.txt(c.c.Name), c.c.State)
		}
		names = append(names, p.space(c.x-cur))
		names = append(names, p.right(name, c.w)...)
		cur = c.x + c.w
	}
	if len(folded) > 0 {
		names = append(names, p.space(restX-cur))
		names = append(names, p.right(p.muted("rest"), restW)...)
	}
	names = append(names, p.space(2))
	names = append(names, p.right(p.muted("TOTAL"), totalW)...)
	out = append(out, names)

	// restCell prints REST's part of burn u, of the folded columns' whole.
	restCell := func(u, whole Usage) chunk {
		s := p.millions(per.Of(u))
		if share {
			s = part(shareOf(u, whole))
		}
		if s == g.none {
			return p.cell(s)
		}
		return p.muted(s)
	}
	for i, r := range rows {
		line := p.deviceLead(r, nameW)
		cur := lead
		for _, k := range shown {
			c := cols[k]
			line = append(line, p.space(c.x-cur))
			line = append(line, p.right(p.heat(text(r, c), value(r, c), c, top), c.w)...)
			cur = c.x + c.w
		}
		if len(folded) > 0 {
			line = append(line, p.space(restX-cur))
			line = append(line, p.right(restCell(rest.rows[i], rest.total), restW)...)
		}
		line = append(line, p.space(2))
		if share {
			line = append(line, p.right(p.cell(est(part(r.BurnShare), r.BurnEstimated)), totalW)...)
		} else {
			line = append(line, p.right(p.cell(est(p.millions(per.Of(r.Burn)), r.BurnEstimated)), totalW)...)
		}
		p.pick(len(out), Pick{Device: r.DeviceID})
		out = append(out, line)
	}

	total := p.totalLead(nameW)
	cur = lead
	for _, k := range shown {
		c := cols[k]
		total = append(total, p.space(c.x-cur))
		total = append(total, p.right(p.cell(totalOf(c.c.Burn)), c.w)...)
		cur = c.x + c.w
	}
	if len(folded) > 0 {
		total = append(total, p.space(restX-cur))
		total = append(total, p.right(p.cell(totalOf(rest.total)), restW)...)
	}
	total = append(total, p.space(2))
	total = append(total, p.right(p.cell(est(totalOf(grand), estimated)), totalW)...)
	return append(out, total)
}

// rest is the burn of the matrix's folded columns: each row's, in the order
// of the rows, and all of it.
type rest struct {
	rows  []Usage
	total Usage
}

// restColumn adds up the folded columns of cols for each of rows.
func (p *page) restColumn(cols []gridCol, folded []int, rows []Row) rest {
	out := rest{rows: make([]Usage, len(rows))}
	for _, k := range folded {
		c := cols[k]
		out.total = out.total.add(c.c.Burn)
		for i, r := range rows {
			if c.idx < len(r.Cells) {
				out.rows[i] = out.rows[i].add(r.Cells[c.idx].Burn)
			}
		}
	}
	return out
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fitColumns places the columns that fit on the page, from the scroll on,
// between the rows' lead and TOTAL, totalW wide, with REST, restW wide,
// before TOTAL when any column does not show, and notes how many there are
// and how many show. It returns the shown ones, the folded ones, those
// scrolled off the left and those off the right edge, and where the matrix
// ends.
func (p *page) fitColumns(cols []gridCol, lead, restW, totalW int) (shown, folded []int, edge int) {
	// after is how wide the matrix is right of its last column: REST, when
	// columns fold, and TOTAL.
	after := func(folds bool) int {
		n := 2 + totalW
		if folds {
			n += 2 + restW
		}
		return n
	}
	scroll := clamp(p.o.MatrixScroll, 0, max(len(cols)-1, 0))
	x := lead
	for k := scroll; k < len(cols); k++ {
		gap := 1
		if k == scroll || cols[k].group != cols[k-1].group {
			gap = 2
		}
		if len(shown) > 0 && x+gap+cols[k].w+after(scroll > 0 || k < len(cols)-1) > p.w {
			break
		}
		cols[k].x = x + gap
		x += gap + cols[k].w
		shown = append(shown, k)
	}
	p.matrixColumns, p.matrixShown = len(cols), len(shown)
	for k := range cols {
		if k < scroll || k >= scroll+len(shown) {
			folded = append(folded, k)
		}
	}
	return shown, folded, x + after(len(folded) > 0)
}

// ownUnit is the unit of this device's own tables, PROJECTS and USAGE:
// weighted tokens, or input plus output in a report saved before burn.
func (p *page) ownUnit() string {
	if p.r.inOut {
		return "M tokens in+out"
	}
	return "M weighted tokens"
}

// teamPeriod is the period the team's tables show, and what days it counts:
// UTC days, but for a today that counts from local midnight.
func (p *page) teamPeriod() string {
	if p.o.Period == Today && p.r.Team.TodayLocal {
		return Today.String()
	}
	return p.o.Period.String() + " UTC"
}

// gridTitle is the matrix's title line: its name, the count of its rows,
// the period and the days it counts, and the unit, and the pills of its
// views and modes, ending at edge.
func (p *page) gridTitle(rows, edge int) chunks {
	g, share := p.g, p.o.Share
	title := chunks{p.bold("DEVICES " + g.times + " SUBSCRIPTIONS"), p.plain("  ")}
	unit := "M weighted tokens"
	if share {
		unit = "% of column total"
	}
	title = append(title, p.muted(strconv.Itoa(rows)+g.sep+p.teamPeriod()+g.sep+unit))
	// The views go before the matrix's modes, and are the first to go
	// where both do not fit.
	modes := chunks{p.pill("tokens", !share), p.plain(" "), p.pill("share", share)}
	if t, ok := p.pillsAt(title, append(append(p.viewPills(), p.plain("  ")), modes...), edge); ok {
		return t
	}
	title, _ = p.pillsAt(title, modes, edge)
	return title
}

// groupName is the heading of a group of subscriptions: its provider, or
// NO QUOTA for the group of tokens with none.
func groupName(grp string) string {
	if grp == "" {
		return "NO QUOTA"
	}
	return strings.ToUpper(grp)
}

// groupHead is a group's heading over span columns: its name and a thin
// rule to the end, or only the name, cut, where the rule does not fit.
func (p *page) groupHead(name string, span int) chunks {
	if span >= width(name)+2 {
		return chunks{p.muted(name), p.plain(" "), p.faint(strings.Repeat(p.g.rule, span-width(name)-1))}
	}
	return chunks{p.muted(truncEnd(name, span, p.g.ell))}
}

// deviceLead is the start of a device's row: its mark, and its name in
// nameW columns.
func (p *page) deviceLead(r Row, nameW int) chunks {
	return append(chunks{p.rowMark(r), p.space(1)}, p.left(p.plain(truncName(p.txt(r.Device), nameW, p.g.ell)), nameW)...)
}

// totalLead is the start of the TOTAL line: TOTAL in w columns, after the
// two of the marks.
func (p *page) totalLead(w int) chunks {
	return append(chunks{p.space(2)}, p.left(p.muted("TOTAL"), w)...)
}

// pillsAt is title t with pills on its right, ending at edge, or further
// right where t is longer, and notes the chosen one for the legend. Where
// they do not fit on the page, it is t as it is, and false.
func (p *page) pillsAt(t, pills chunks, edge int) (chunks, bool) {
	at := max(edge, t.width()+2+pills.width())
	if at > p.w {
		return t, false
	}
	p.mark("chosen")
	return append(t.padTo(at-pills.width()), pills...), true
}

// pill is one choice of a mode: the chosen one between ‹ ›, in reverse
// accent; the others dim.
func (p *page) pill(s string, chosen bool) chunk {
	if chosen {
		return p.paint(p.g.open+s+p.g.shut, p.th.Accent, false, true)
	}
	return p.muted(strings.Repeat(" ", width(p.g.open)) + s + strings.Repeat(" ", width(p.g.shut)))
}

// cell is a number in plain text, or the none mark faint.
func (p *page) cell(s string) chunk {
	if s == p.g.none {
		p.mark("none")
		return p.faint(s)
	}
	return p.plain(s)
}

// heat is a matrix cell in column c. With color it grows brighter with its
// value, on a log scale against top, the largest cell, and bold at the top
// step. Without color, the largest in its column is bold.
func (p *page) heat(s string, v float64, c gridCol, top float64) chunk {
	if v <= 0 {
		return p.cell(s)
	}
	if !p.o.Color {
		if v == c.top {
			return p.bold(s)
		}
		return p.plain(s)
	}
	step := heatStep(v, top)
	return p.paint(s, p.th.Heat[step-1], step == len(p.th.Heat), false)
}

// heatStep is the step of the heat map a value takes, from 1 to 5: a step
// for every half of a tenfold below the largest value.
func heatStep(v, top float64) int {
	if v <= 0 || top <= 0 {
		return 0
	}
	return clamp(5-int(math.Floor(math.Log10(top/v)*2)), 1, 5)
}

// rowMark is a device's state before its name: this device, a silence, an
// error, or an old release. A silent device's error is the one it last
// reported, so its silence shows, as in ATTENTION.
func (p *page) rowMark(r Row) chunk {
	g := p.g
	d := p.device(r.DeviceID, r.Device)
	switch {
	case d == nil:
		return p.space(1)
	case d.This:
		p.mark("this")
		return p.accent(g.here)
	case d.Silent:
		p.mark("silent")
		return p.paint(g.silent, p.th.Tight, false, false)
	case d.Error != nil:
		p.mark("fail")
		return p.paint(g.fail, p.th.Out, false, false)
	case d.Old:
		p.mark("old")
		return p.muted(g.old)
	}
	return p.space(1)
}

// usage is USAGE: this device's weighted tokens on each subscription, and
// those with no quota, today and in the last 7, 30, and 90 days, in its
// local periods, as PROJECTS counts them.
func (p *page) usage() []chunks {
	g := p.g
	cols := p.r.Team.Matrix.Columns
	if len(cols) == 0 {
		return nil
	}
	type usageRow struct {
		group string
		here  bool
		name  string
		u     Usage
		// pick is the account the row opens, when it is one.
		pick *Pick
	}
	burn := p.localBurn()
	var rows []usageRow
	var sum Usage
	for i, c := range cols {
		r := usageRow{group: c.Provider, name: p.txt(c.Name), u: burn[i]}
		if c.NoQuota {
			r.group = ""
		} else {
			label := shortID(c.Label)
			if a := p.account(c.Provider, c.Label); a != nil {
				r.here, label = a.Current, shownLabel(a)
				r.pick = &Pick{Provider: c.Provider, Label: c.Label}
			}
			r.name = p.txt(label)
		}
		rows = append(rows, r)
		sum = sum.add(r.u)
	}
	numW := make([]int, len(Periods))
	for i, per := range Periods {
		numW[i] = max(width(strings.ToUpper(per.String())), width(p.millions(per.Of(sum))), 4)
	}
	acct := width("TOTAL")
	for _, r := range rows {
		acct = max(acct, width(r.name), width(r.group), width("NO QUOTA"))
	}
	rest := 0
	for _, w := range numW {
		rest += 2 + w
	}
	acct = max(min(acct, p.w-2-rest), minAccount)

	numbers := func(line chunks, u Usage) chunks {
		for i, per := range Periods {
			line = append(line, p.space(2))
			line = append(line, p.right(p.cell(p.millions(per.Of(u))), numW[i])...)
		}
		return line
	}
	out := []chunks{{p.bold("USAGE"), p.plain("  "), p.muted(p.txt(p.r.Collector.name()) + g.sep + p.ownUnit())}}
	for i, r := range rows {
		if i == 0 || rows[i-1].group != r.group {
			out = append(out, nil)
			head := chunks{p.space(2)}
			head = append(head, p.left(p.bold(groupName(r.group)), acct)...)
			if i == 0 {
				// Every header is dim. The chosen period orders nothing
				// here, so none stands out.
				for k, per := range Periods {
					head = append(head, p.space(2))
					head = append(head, p.right(p.muted(strings.ToUpper(per.String())), numW[k])...)
				}
			}
			out = append(out, head)
		}
		line := chunks{p.space(1), p.space(1)}
		if r.here {
			line[0] = p.accent(g.here)
			p.mark("here")
		}
		line = append(line, p.left(p.plain(truncMid(r.name, acct, g.ell)), acct)...)
		if r.pick != nil {
			p.pick(len(out), *r.pick)
		}
		out = append(out, numbers(line, r.u))
	}
	return append(out, nil, numbers(p.totalLead(acct), sum))
}

// localBurn is this device's burn in the local periods for each column of
// the matrix, from its own accounts: a subscription's, what Hermes spent
// through that login, and a provider's with no quota. An account with no
// column of its own counts in none.
func (p *page) localBurn() []Usage {
	cols := p.r.Team.Matrix.Columns
	out := make([]Usage, len(cols))
	find := func(provider, label string, noQuota bool) int {
		return slices.IndexFunc(cols, func(c Column) bool {
			return c.Provider == provider && c.NoQuota == noQuota && (noQuota || c.Label == label)
		})
	}
	for _, pv := range p.r.Providers {
		for _, a := range pv.Accounts {
			i := -1
			switch {
			case subscription(pv.Provider):
				i = find(pv.Provider, a.Label, false)
			case a.Link != nil && a.Link.Label != "":
				i = find(a.Link.Provider, a.Link.Label, false)
			}
			if i < 0 {
				i = find(pv.Provider, "", true)
			}
			if i >= 0 {
				out[i] = out[i].add(a.Burn)
			}
		}
	}
	return out
}
