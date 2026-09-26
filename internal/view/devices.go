package view

import (
	"math"
	"sort"
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

// sortRows puts the matrix's rows in the order it shows them: the most
// tokens in the period first, then by name. The status view shows its rows
// in the same order.
func sortRows(rows []Row, per Period) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := per.Of(rows[i].Usage), per.Of(rows[j].Usage)
		if a != b {
			return a > b
		}
		return rows[i].Device < rows[j].Device
	})
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
	gridCell   = 8  // the narrowest a matrix column is
	deviceName = 20 // the widest a device's name is
	// devicesHead is how many lines are over the rows of DEVICES, in the
	// matrix and in the status view alike: the title, the group headings,
	// and the column headers.
	devicesHead = 3
)

// grid is DEVICES × SUBSCRIPTIONS: a row per device, the most tokens in
// the period first, and a column per subscription, grouped under its
// provider, with totals on the right and at the bottom. The cells are a
// heat map on a log scale; without color, the largest in each column is
// bold. The share mode shows each value as a part of its column's total: a
// subscription's column adds up to 100, and TOTAL is each device's part of
// the team's tokens.
func (p *page) grid() []chunks {
	g := p.g
	m := p.r.Team.Matrix
	per, share := p.o.Period, p.o.Share
	var cols []gridCol
	for i := range m.Columns {
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
			if v := per.share(cell.Share); v != nil {
				return *v
			}
			return 0
		}
		return float64(per.Of(cell.Usage))
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
			return part(cell.Share)
		}
		return p.millions(per.Of(cell.Usage))
	}

	var top float64
	for k := range cols {
		c := &cols[k]
		c.w = max(gridCell, width(p.txt(c.c.Name)), width(totalOf(c.c.Usage)))
		for _, r := range rows {
			c.w = max(c.w, width(text(r, *c)))
			c.top = max(c.top, value(r, *c))
		}
		top = max(top, c.top)
	}
	nameW := width("TOTAL")
	var grand Usage
	for _, r := range rows {
		nameW = max(nameW, min(width(p.txt(r.Device)), deviceName))
		grand = grand.add(r.Usage)
	}
	lead := 2 + nameW
	totalW := max(gridCell, width(totalOf(grand)))
	shown, hidden, edge := p.fitColumns(cols, lead, totalW)
	out := []chunks{p.gridTitle(len(rows), edge)}

	// The providers over their columns, each with a thin rule.
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
	if hidden > 0 {
		heads = append(heads, p.space(edge-width(more(hidden))-cur), p.muted(more(hidden)))
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
	names = append(names, p.space(2))
	names = append(names, p.right(p.muted("TOTAL"), totalW)...)
	out = append(out, names)

	for _, r := range rows {
		line := p.deviceLead(r, nameW)
		cur := lead
		for _, k := range shown {
			c := cols[k]
			line = append(line, p.space(c.x-cur))
			line = append(line, p.right(p.heat(text(r, c), value(r, c), c, top), c.w)...)
			cur = c.x + c.w
		}
		line = append(line, p.space(2))
		if share {
			line = append(line, p.right(p.cell(part(r.Share)), totalW)...)
		} else {
			line = append(line, p.right(p.cell(p.millions(per.Of(r.Usage))), totalW)...)
		}
		out = append(out, line)
	}

	total := p.totalLead(nameW)
	cur = lead
	for _, k := range shown {
		c := cols[k]
		total = append(total, p.space(c.x-cur))
		total = append(total, p.right(p.cell(totalOf(c.c.Usage)), c.w)...)
		cur = c.x + c.w
	}
	total = append(total, p.space(2))
	total = append(total, p.right(p.cell(totalOf(grand)), totalW)...)
	return append(out, total)
}

// fitColumns places the columns that fit on the page, from the scroll on,
// between the rows' lead and TOTAL, totalW wide, and notes how many there
// are and how many show. It returns the shown ones, how many are off the
// right edge, and where the matrix ends.
func (p *page) fitColumns(cols []gridCol, lead, totalW int) (shown []int, hidden, edge int) {
	// after is how wide the matrix is right of its last column: TOTAL, and
	// over it the count of the columns off the right edge, if any.
	after := func(off int) int {
		n := 2 + totalW
		if off > 0 {
			n = max(n, 2+width(more(off)))
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
		if len(shown) > 0 && x+gap+cols[k].w+after(len(cols)-k-1) > p.w {
			break
		}
		cols[k].x = x + gap
		x += gap + cols[k].w
		shown = append(shown, k)
	}
	p.matrixColumns, p.matrixShown = len(cols), len(shown)
	// The count is of the columns off the right edge only. Those scrolled
	// off the left are the interactive view's, whose key bar leads back.
	hidden = len(cols) - scroll - len(shown)
	return shown, hidden, x + after(hidden)
}

// more says how many of the matrix's columns are off the right edge.
func more(n int) string { return "+" + strconv.Itoa(n) + " more" }

// gridTitle is the matrix's title line: its name, the count of its rows,
// the period and the unit, and the pills of its views and modes, ending at
// edge.
func (p *page) gridTitle(rows, edge int) chunks {
	g, per, share := p.g, p.o.Period, p.o.Share
	title := chunks{p.bold("DEVICES " + g.times + " SUBSCRIPTIONS"), p.plain("  ")}
	unit := "M tokens in+out"
	if share {
		unit = "% of column total"
	}
	title = append(title, p.muted(strconv.Itoa(rows)+g.sep+per.String()+g.sep+unit))
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
	return append(chunks{p.rowMark(r), p.space(1)}, p.left(p.plain(truncEnd(p.txt(r.Device), nameW, p.g.ell)), nameW)...)
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

// usage is USAGE: this device's tokens on each subscription, and those
// with no quota, today and in 7, 30, and 90 days.
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
	}
	var rows []usageRow
	var sum Usage
	for _, c := range cols {
		r := usageRow{group: c.Provider, name: p.txt(c.Name), u: c.Usage}
		if c.NoQuota {
			r.group = ""
		} else {
			label := c.Label
			if a := p.account(c.Provider, c.Label); a != nil {
				r.here = a.Current
				if a.Alias != nil && a.Name == *a.Alias {
					label = *a.Alias
				}
			}
			r.name = p.txt(shortID(label))
		}
		rows = append(rows, r)
		sum = sum.add(c.Usage)
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
	out := []chunks{{p.bold("USAGE"), p.plain("  "), p.muted(p.txt(p.r.Collector.DeviceLabel) + g.sep + "M tokens in+out")}}
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
		out = append(out, numbers(line, r.u))
	}
	return append(out, nil, numbers(p.totalLead(acct), sum))
}
