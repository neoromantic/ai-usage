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
// tokens in the period first, tokens not known before none, then by name.
// The status view shows its rows in the same order.
func sortRows(rows []Row, per Period) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := per.Of(rows[i].Usage), per.Of(rows[j].Usage)
		if a != b {
			return a > b
		}
		if ka, kb := per.Known(rows[i].Usage), per.Known(rows[j].Usage); ka != kb {
			return kb
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
	// known says every value in the column is known, so the largest of
	// them, top, is the largest in the column.
	known bool
}

const (
	gridCell   = 8  // the narrowest a matrix column is
	deviceName = 20 // the widest a device's name is
)

// grid is DEVICES × SUBSCRIPTIONS: a row per device, the most tokens in
// the period first, and a column per subscription, grouped under its
// provider, with totals on the right and at the bottom. The cells are a
// heat map on a log scale; without color, the largest in each column is
// bold, unless the column has a value that is not known. The share mode
// shows each device's estimated share of each window. Tokens that are not
// all known, as a device's on a collector older than v0.2.0, show as
// tokens prints them, and a share that is not known is ?.
func (p *page) grid() []chunks {
	g := p.g
	m := p.r.Team.Matrix
	per, share := p.o.Period, p.o.Share
	var cols []gridCol
	for i := range m.Columns {
		c := &m.Columns[i]
		if share && c.NoQuota {
			continue
		}
		grp := c.Provider
		if c.NoQuota {
			grp = ""
		}
		// A column in the share mode whose shares are not all known has no
		// known share above 0 to be bold.
		cols = append(cols, gridCol{c: c, idx: i, group: grp, known: share || per.Known(c.Usage)})
	}
	rows := append([]Row(nil), m.Rows...)
	sortRows(rows, per)
	value := func(r Row, c gridCol) float64 {
		if c.idx >= len(r.Cells) {
			return 0
		}
		cell := r.Cells[c.idx]
		if share {
			if cell.Share == nil {
				return 0
			}
			return *cell.Share
		}
		return float64(per.Of(cell.Usage))
	}
	text := func(r Row, c gridCol) string {
		if c.idx >= len(r.Cells) {
			return g.none
		}
		cell := r.Cells[c.idx]
		if !share {
			return p.tokens(per, cell.Usage)
		}
		// A window whose tokens are not all known cannot be split, though
		// how full it is is known.
		if cell.Share == nil && c.c.Percent != nil && c.c.WindowUnknown {
			return "?"
		}
		return p.percent(cell.Share)
	}
	bottom := func(c gridCol) string {
		if share {
			if c.c.Percent == nil {
				return "?"
			}
			return p.percent(c.c.Percent)
		}
		return p.tokens(per, c.c.Usage)
	}

	var top float64
	for k := range cols {
		c := &cols[k]
		c.w = max(gridCell, width(p.txt(c.c.Name)), width(bottom(*c)))
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
	totalW := 0
	if !share {
		totalW = max(gridCell, width(p.tokens(per, grand)))
	}
	// after is how wide the matrix is right of its last column: TOTAL, and
	// over it the count of the columns off the right edge, if any.
	more := func(n int) string { return "+" + strconv.Itoa(n) + " more" }
	after := func(off int) int {
		n := 0
		if totalW > 0 {
			n = 2 + totalW
		}
		if off > 0 {
			n = max(n, 2+width(more(off)))
		}
		return n
	}

	// The columns that fit, from the scroll on.
	scroll := clamp(p.o.MatrixScroll, 0, max(len(cols)-1, 0))
	var shown []int
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
	hidden := len(cols) - scroll - len(shown)
	edge := x + after(hidden)

	title := chunks{p.bold("DEVICES " + g.times + " SUBSCRIPTIONS"), p.plain("  ")}
	if share {
		title = append(title, p.muted(strconv.Itoa(len(rows))+g.sep+"% of each window, estimated"))
	} else {
		title = append(title, p.muted(strconv.Itoa(len(rows))+g.sep+per.String()+g.sep+"M tokens in+out"))
	}
	// The views go before the matrix's modes, and are the first to go
	// where both do not fit.
	pills := chunks{p.pill("tokens", !share), p.plain(" "), p.pill("share", share)}
	if views := append(p.viewPills(), p.plain("  ")); max(edge, title.width()+2+views.width()+pills.width()) <= p.w {
		pills = append(views, pills...)
	}
	if at := max(edge, title.width()+2+pills.width()); at <= p.w {
		p.mark("chosen")
		title = append(title.padTo(at-pills.width()), pills...)
	}
	out := []chunks{title}

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
		name := "NO QUOTA"
		if first.group != "" {
			name = strings.ToUpper(first.group)
		}
		span := last.x + last.w - first.x
		heads = append(heads, p.space(first.x-cur))
		if span >= width(name)+2 {
			heads = append(heads, p.muted(name), p.plain(" "), p.faint(strings.Repeat(g.rule, span-width(name)-1)))
		} else {
			heads = append(heads, p.muted(truncEnd(name, span, g.ell)))
		}
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
	if totalW > 0 {
		names = append(names, p.space(2))
		names = append(names, p.right(p.muted("TOTAL"), totalW)...)
	}
	out = append(out, names)

	for _, r := range rows {
		line := chunks{p.rowMark(r), p.space(1)}
		line = append(line, p.left(p.plain(truncEnd(p.txt(r.Device), nameW, g.ell)), nameW)...)
		cur := lead
		for _, k := range shown {
			c := cols[k]
			line = append(line, p.space(c.x-cur))
			line = append(line, p.right(p.heat(text(r, c), value(r, c), c, top), c.w)...)
			cur = c.x + c.w
		}
		if totalW > 0 {
			line = append(line, p.space(2))
			line = append(line, p.right(p.cell(p.tokens(per, r.Usage)), totalW)...)
		}
		out = append(out, line)
	}

	total := chunks{p.space(2)}
	total = append(total, p.left(p.muted("TOTAL"), nameW)...)
	cur = lead
	for _, k := range shown {
		c := cols[k]
		total = append(total, p.space(c.x-cur))
		if share && c.c.Percent == nil {
			// A window whose percent is not known has a plain ? at the
			// bottom.
			total = append(total, p.right(p.plain(bottom(c)), c.w)...)
		} else {
			total = append(total, p.right(p.cell(bottom(c)), c.w)...)
		}
		cur = c.x + c.w
	}
	if totalW > 0 {
		total = append(total, p.space(2))
		total = append(total, p.right(p.cell(p.tokens(per, grand)), totalW)...)
	}
	return append(out, total)
}

// pill is one choice of a mode: the chosen one between ‹ ›, in reverse
// accent; the others dim.
func (p *page) pill(s string, chosen bool) chunk {
	if chosen {
		return p.paint(p.g.open+s+p.g.shut, p.th.Accent, false, true)
	}
	return p.muted(strings.Repeat(" ", width(p.g.open)) + s + strings.Repeat(" ", width(p.g.shut)))
}

// cell is a number in plain text, the none mark faint, or ? muted.
func (p *page) cell(s string) chunk {
	switch {
	case s == p.g.none:
		p.mark("none")
		return p.faint(s)
	case s == "?":
		p.mark("unknown")
		return p.muted(s)
	case strings.HasPrefix(s, p.g.atLeast):
		p.mark("atLeast")
	}
	return p.plain(s)
}

// heat is a matrix cell in column c. With color it grows brighter with its
// value, on a log scale against top, the largest cell, and bold at the top
// step. Without color, the largest in its column is bold, when every value
// in the column is known.
func (p *page) heat(s string, v float64, c gridCol, top float64) chunk {
	if v <= 0 {
		return p.cell(s)
	}
	if strings.HasPrefix(s, p.g.atLeast) {
		p.mark("atLeast")
	}
	if !p.o.Color {
		if c.known && v == c.top {
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
		for _, tp := range p.r.Team.Providers {
			for _, a := range tp.Accounts {
				if a.Subscription {
					cols = append(cols, Column{Provider: tp.Provider, Label: a.Label, Name: a.Name, State: a.State, Usage: a.Usage})
				}
			}
		}
	}
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
	periods := []Period{Today, Week, Month, Quarter}
	numW := make([]int, len(periods))
	for i, per := range periods {
		numW[i] = max(width(strings.ToUpper(per.String())), width(p.tokens(per, sum)), 4)
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

	groupName := func(grp string) string {
		if grp == "" {
			return "NO QUOTA"
		}
		return strings.ToUpper(grp)
	}
	numbers := func(line chunks, u Usage) chunks {
		for i, per := range periods {
			line = append(line, p.space(2))
			line = append(line, p.right(p.cell(p.tokens(per, u)), numW[i])...)
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
				for k, per := range periods {
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
	total := chunks{p.space(2)}
	total = append(total, p.left(p.muted("TOTAL"), acct)...)
	return append(out, nil, numbers(total, sum))
}
