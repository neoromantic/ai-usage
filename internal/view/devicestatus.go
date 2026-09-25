package view

import (
	"image/color"
	"strconv"
	"strings"
)

// statusCol is a column of the status view right of the device's name: its
// heading, the group heading over it, and a cell for each row, then one for
// TOTAL.
type statusCol struct {
	key   string // what it shows: user, version, seen, via, note, or a period
	head  chunk
	group string
	right bool
	cells []chunks
	// marks are the marks its cells draw, noted for the legend when it shows.
	marks map[string]bool
	w, x  int
}

const (
	// statusUser is the widest an OS user is, and statusVersion a release.
	statusUser    = 12
	statusVersion = 12
	// statusNote is how narrow NOTE is cut before a column goes, and
	// statusCut the least of a silent device's last error it keeps.
	statusNote = 16
	statusCut  = 12
)

// deviceStatus is the status view of DEVICES: a row per device, in the
// matrix's order, with its mark, its OS user, its collector's release, how
// long ago it reported, the harnesses it reads, its tokens in each period,
// and a note of what is wrong with it. Where the page is narrow, NOTE is cut
// first, then USER goes, then VIA, then the periods: today, 30d, and 7d,
// but never the chosen one or 90d.
func (p *page) deviceStatus() []chunks {
	g := p.g
	per := p.o.Period
	rows := append([]Row(nil), p.r.Team.Matrix.Rows...)
	// A device with no row in the matrix still has one here.
	has := map[*TeamDevice]bool{}
	for _, r := range rows {
		has[p.device(r.DeviceID, r.Device)] = true
	}
	for i := range p.r.Team.Devices {
		if d := &p.r.Team.Devices[i]; !has[d] {
			rows = append(rows, Row{Device: d.Label, DeviceID: d.Device, Usage: d.Usage})
		}
	}
	sortRows(rows, per)
	n := len(rows)
	devs := make([]*TeamDevice, n+1)
	var grand Usage
	for i, r := range rows {
		devs[i] = p.device(r.DeviceID, r.Device)
		grand = grand.add(r.Usage)
	}

	col := func(key, head, group string, right bool, cell func(i int, d *TeamDevice) chunks) *statusCol {
		c := &statusCol{key: key, head: p.muted(head), group: group, right: right, w: width(head), cells: make([]chunks, n+1)}
		saved := p.seen
		p.seen = map[string]bool{}
		for i := range c.cells {
			c.cells[i] = cell(i, devs[i])
			c.w = max(c.w, c.cells[i].width())
		}
		c.marks, p.seen = p.seen, saved
		return c
	}
	user := col("user", "USER", "", false, func(_ int, d *TeamDevice) chunks {
		if d == nil || d.OSUser == "" {
			return nil
		}
		return chunks{p.muted(truncEnd(p.txt(d.OSUser), statusUser, g.ell))}
	})
	version := col("version", "VERSION", "COLLECTOR", false, func(_ int, d *TeamDevice) chunks {
		if d == nil {
			return nil
		}
		v := truncEnd(p.txt(d.CollectorVersion), statusVersion, g.ell)
		switch {
		case d.Old:
			// Dim, as OLD is, and marked, so it reads without color.
			p.mark("old")
			return chunks{p.muted(v + " " + g.old)}
		case v == "":
			p.mark("none")
			return chunks{p.faint(g.none)}
		}
		return chunks{p.plain(v)}
	})
	seen := col("seen", "SEEN", "COLLECTOR", true, func(_ int, d *TeamDevice) chunks {
		if d == nil {
			return nil
		}
		s := dur(p.now.Sub(d.CollectedAt))
		if d.Silent {
			return chunks{p.paint(s, p.th.Tight, false, false)}
		}
		return chunks{p.muted(s)}
	})
	via := col("via", "VIA", "COLLECTOR", false, func(_ int, d *TeamDevice) chunks {
		if d == nil {
			return nil
		}
		return p.harnesses(*d)
	})
	cols := []*statusCol{user, version, seen, via}
	for _, q := range Periods {
		c := col(q.String(), strings.ToUpper(q.String()), "TOKENS", true, func(i int, _ *TeamDevice) chunks {
			u := grand
			if i < n {
				u = rows[i].Usage
			}
			return chunks{p.cell(p.tokens(q, u))}
		})
		// Every header is dim, the period the rows are in the order of too:
		// the title names it.
		c.w = max(c.w, 4)
		cols = append(cols, c)
	}
	note := col("note", "NOTE", "", false, func(_ int, d *TeamDevice) chunks {
		if d == nil {
			return nil
		}
		return p.deviceNote(*d, -1)
	})
	for _, c := range note.cells {
		if len(c) > 0 {
			cols = append(cols, note)
			break
		}
	}

	nameW := width("DEVICE")
	for _, r := range rows {
		nameW = max(nameW, min(width(p.txt(r.Device)), deviceName))
	}
	lead := 2 + nameW
	gone := map[string]bool{}
	// fixed is how wide the table is but for NOTE.
	fixed := func() int {
		w := lead
		for _, c := range cols {
			if c != note && !gone[c.key] {
				w += 2 + c.w
			}
		}
		return w
	}
	noteMin := 0
	if cols[len(cols)-1] == note {
		noteMin = 2 + min(note.w, statusNote)
	}
	drops := []string{"user", "via"}
	for _, q := range []Period{Today, Month, Week} {
		if q != per {
			drops = append(drops, q.String())
		}
	}
	for _, k := range drops {
		if fixed()+noteMin <= p.w {
			break
		}
		gone[k] = true
	}
	if noteMin > 0 && fixed()+2+note.w > p.w {
		room := p.w - fixed() - 2
		gone["note"] = room < width(note.head.text)
		note.w = width(note.head.text)
		for i, d := range devs {
			if d != nil {
				note.cells[i] = p.deviceNote(*d, room)
				note.w = max(note.w, note.cells[i].width())
			}
		}
	}
	var shown []*statusCol
	x := lead
	for _, c := range cols {
		if gone[c.key] {
			continue
		}
		c.x = x + 2
		x = c.x + c.w
		shown = append(shown, c)
		for k := range c.marks {
			p.mark(k)
		}
	}
	edge := x

	title := p.statusTitle(devs[:n], edge)

	// The groups over their columns, each with a thin rule, as the matrix
	// has its providers.
	heads := chunks{p.space(lead)}
	cur := lead
	for i := 0; i < len(shown); i++ {
		first := shown[i]
		if first.group == "" {
			continue
		}
		for i+1 < len(shown) && shown[i+1].group == first.group {
			i++
		}
		last := shown[i]
		span := last.x + last.w - first.x
		heads = append(heads, p.space(first.x-cur))
		if span >= width(first.group)+2 {
			heads = append(heads, p.muted(first.group), p.plain(" "), p.faint(strings.Repeat(g.rule, span-width(first.group)-1)))
		} else {
			heads = append(heads, p.muted(truncEnd(first.group, span, g.ell)))
		}
		cur = last.x + last.w
	}

	cells := func(line chunks, i int) chunks {
		cur := lead
		for _, c := range shown {
			cell := c.cells[i].cut(c.w, g.ell)
			pad := p.space(c.w - cell.width())
			line = append(line, p.space(c.x-cur))
			if c.right {
				line = append(append(line, pad), cell...)
			} else {
				line = append(append(line, cell...), pad)
			}
			cur = c.x + c.w
		}
		return line
	}
	head := chunks{p.space(2), p.muted("DEVICE"), p.space(nameW - width("DEVICE"))}
	cur = lead
	for _, c := range shown {
		pad := p.space(c.w - width(c.head.text))
		head = append(head, p.space(c.x-cur))
		if c.right {
			head = append(head, pad, c.head)
		} else {
			head = append(head, c.head, pad)
		}
		cur = c.x + c.w
	}
	out := []chunks{title, heads, head}
	for i, r := range rows {
		line := chunks{p.rowMark(r), p.space(1)}
		line = append(line, p.left(p.plain(truncEnd(p.txt(r.Device), nameW, g.ell)), nameW)...)
		out = append(out, cells(line, i))
	}
	total := chunks{p.space(2)}
	total = append(total, p.left(p.muted("TOTAL"), nameW)...)
	return append(out, cells(total, n))
}

// statusTitle is the status view's title: how many devices there are, how
// many fail, are silent, or run an old release, in the colors of those
// states, the period the rows are in the order of, and the views on the
// right at edge, or further right where the title is longer. The unit goes
// first where the page is narrow, then the views.
func (p *page) statusTitle(devs []*TeamDevice, edge int) chunks {
	g := p.g
	var errs, silent, old int
	for _, d := range devs {
		switch {
		case d == nil:
			continue
		case d.Silent:
			// A silent device's error is the one it last reported.
			silent++
		case d.Error != nil:
			errs++
		}
		if d != nil && d.Old {
			old++
		}
	}
	title := chunks{p.bold("DEVICES"), p.plain("  "), p.muted(strconv.Itoa(len(devs)))}
	count := func(n int, what string, c chunk) {
		if n > 0 {
			c.text = strconv.Itoa(n) + " " + what
			title = append(title, p.muted(g.sep), c)
		}
	}
	errors := "error"
	if errs > 1 {
		errors = "errors"
	}
	count(errs, errors, p.paint("", p.th.Out, false, false))
	count(silent, "silent", p.paint("", p.th.Tight, false, false))
	count(old, "old", p.muted(""))
	title = append(title, p.muted(g.sep+"by "+p.o.Period.String()))
	unit := append(append(chunks(nil), title...), p.muted(g.sep+"M tokens in+out"))

	pills := p.viewPills()
	for _, t := range []chunks{unit, title} {
		if at := max(edge, t.width()+2+pills.width()); at <= p.w {
			p.mark("chosen")
			return append(t.padTo(at-pills.width()), pills...)
		}
	}
	if unit.width() <= p.w {
		return unit
	}
	return title
}

// harnesses are the harnesses a device reads, as its sources list them, dim,
// with a failing one in the error's color and marked; none when it reads
// none.
func (p *page) harnesses(d TeamDevice) chunks {
	g := p.g
	var out chunks
	dim := ""
	for _, s := range d.Sources {
		if s.Status == "skipped" {
			continue
		}
		if len(out) > 0 || dim != "" {
			dim += ", "
		}
		name := p.txt(s.Provider)
		if s.Status == "error" || s.Status == "partial" {
			p.mark("fail")
			if dim != "" {
				out = append(out, p.muted(dim))
				dim = ""
			}
			out = append(out, p.paint(name+" "+g.fail, p.th.Out, false, false))
			continue
		}
		dim += name
	}
	if dim != "" {
		out = append(out, p.muted(dim))
	}
	if len(out) == 0 {
		p.mark("none")
		return chunks{p.faint(g.none)}
	}
	return out
}

// deviceNote is NOTE within w columns, or all of it for w below 0: a silent
// device's silence, with the error it last reported, as ATTENTION has it;
// else what fails on the device, in the error's color; else, on an old
// release, why its release check failed, in the error's color, how long it
// has not updated once that is longer than updating takes, in the tight
// color, or the release it is behind; else why its release check failed.
// The rest is dim. A short column cuts the error, then drops the last one,
// says only "since" of the silence, then only its day, then only "silent",
// so a time is never cut, and says only "latest" of the release. It keeps a
// release check's error while 12 columns of it fit, after a shorter form of
// what failed if need be, and how long a device has not updated, but not the
// release.
func (p *page) deviceNote(d TeamDevice, w int) chunks {
	g := p.g
	fit := func(forms ...chunk) chunks {
		for _, f := range forms {
			if w < 0 || width(f.text) <= w {
				return chunks{f}
			}
		}
		return chunks{forms[len(forms)-1]}.cut(w, g.ell)
	}
	// update is what of a failed release check fits: its error, cut, after
	// the longest form of what failed that leaves 12 columns of it, else
	// only what failed, in one of its forms.
	update := func(paint func(string) chunk, what ...string) chunks {
		msg := p.txt(strings.TrimPrefix(*d.UpdateError, "update check: "))
		for _, s := range what {
			if w < 0 || w >= width(s+": ")+statusCut {
				return fit(paint(s + ": " + msg))
			}
		}
		var forms []chunk
		for _, s := range what {
			forms = append(forms, paint(s))
		}
		return fit(forms...)
	}
	color := func(c color.Color) func(string) chunk {
		return func(s string) chunk { return p.paint(s, c, false, false) }
	}
	switch {
	case d.Silent:
		c := p.clock(d.CollectedAt)
		s := "silent since " + c
		// The clock without its time is its weekday or its date.
		silence := []chunk{p.muted(s), p.muted("since " + c), p.muted("since " + c[:strings.LastIndexByte(c, ' ')]), p.muted("silent")}
		if d.Error == nil {
			return fit(silence...)
		}
		last := s + g.sep + "last error: " + p.txt(*d.Error)
		if w >= width(last)-width(p.txt(*d.Error))+statusCut {
			return fit(p.muted(last))
		}
		return fit(append([]chunk{p.muted(last)}, silence...)...)
	case d.Error != nil:
		return fit(p.paint(p.txt(*d.Error), p.th.Out, false, false))
	case d.Old && d.UpdateError != nil:
		return update(color(p.th.Out), "update failing", "update")
	case d.Old && p.r.Team.Latest != nil:
		latest := "latest " + p.txt(*p.r.Team.Latest)
		if d.BehindSince != nil && p.now.Sub(*d.BehindSince) >= BehindAfter {
			tight, a := color(p.th.Tight), age(p.now.Sub(*d.BehindSince))
			return fit(tight("not updated for "+a+g.sep+latest), tight("not updated for "+a), tight("not updated "+a))
		}
		return fit(p.muted("update: "+latest), p.muted(latest))
	case d.UpdateError != nil:
		return update(p.muted, "update check failing", "check failing")
	}
	return nil
}
