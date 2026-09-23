package view

import (
	"strconv"
	"strings"
)

// projects is where this device's tokens go: its projects over every
// account, the most in the period first, the top ones unless every one is
// asked for.
func (p *page) projects() []chunks {
	if len(p.r.Projects) == 0 {
		return nil
	}
	g := p.g
	per := p.o.Period
	ps := append([]Project(nil), p.r.Projects...)
	sortProjects(ps, per)
	more := 0
	if !p.o.AllProjects && len(ps) > topProjects {
		ps, more = ps[:topProjects], len(ps)-topProjects
	}

	type column struct {
		head  string
		right bool
		cells []string
		w     int
	}
	cols := []*column{{head: strings.ToUpper(per.String()), right: true}}
	if per != Quarter {
		cols = append(cols, &column{head: "90D", right: true})
	}
	sess := &column{head: "SESS", right: true}
	via := &column{head: "VIA"}
	last := &column{head: "LAST", right: true}
	cols = append(cols, sess, via, last)
	pathW := width("PROJECT")
	paths := make([]string, len(ps))
	for i, pr := range ps {
		paths[i] = p.txt(p.path(pr.Path))
		pathW = max(pathW, min(width(paths[i]), maxPath))
		cols[0].cells = append(cols[0].cells, p.millions(per.Of(pr.Usage)))
		if per != Quarter {
			cols[1].cells = append(cols[1].cells, p.millions(pr.Usage.Quarter))
		}
		sess.cells = append(sess.cells, strconv.Itoa(pr.Sessions))
		via.cells = append(via.cells, strings.Join(pr.Providers, ", "))
		at := g.none
		if pr.LastActiveAt != nil {
			at = age(p.now.Sub(*pr.LastActiveAt))
		}
		last.cells = append(last.cells, at)
	}
	rest := 0
	for _, c := range cols {
		c.w = width(c.head)
		for _, s := range c.cells {
			c.w = max(c.w, width(s))
		}
		rest += 2 + c.w
	}
	if 2+pathW+rest > p.w {
		pathW = max(p.w-2-rest, minPath)
	}
	if 2+pathW+rest > p.w {
		// VIA goes before the paths are cut any further.
		for i, c := range cols {
			if c == via {
				cols = append(cols[:i], cols[i+1:]...)
				rest -= 2 + c.w
				break
			}
		}
		pathW = max(p.w-2-rest, minPath)
	}

	title := chunks{p.bold("PROJECTS"), p.plain("  "),
		p.muted(p.txt(p.r.Collector.DeviceLabel) + g.sep + "by " + per.String() + g.sep + "M tokens in+out")}
	head := chunks{p.space(2)}
	head = append(head, p.left(p.muted("PROJECT"), pathW)...)
	for i, c := range cols {
		h := p.muted(c.head)
		if i == 0 {
			// The period the table is sorted by.
			h = p.plain(c.head)
		}
		head = append(head, p.space(2))
		if c.right {
			head = append(head, p.right(h, c.w)...)
		} else {
			head = append(head, p.left(h, c.w)...)
		}
	}
	out := []chunks{title, head}
	for i := range ps {
		line := chunks{p.space(2)}
		line = append(line, p.left(p.plain(truncPath(paths[i], pathW, g.ell)), pathW)...)
		for k, c := range cols {
			s := c.cells[i]
			line = append(line, p.space(2))
			switch {
			case c == via:
				line = append(line, p.left(p.muted(truncEnd(s, c.w, g.ell)), c.w)...)
			case c == last:
				if s == g.none {
					p.mark("none")
				}
				line = append(line, p.right(p.muted(s), c.w)...)
			case k < 2 && c != sess:
				line = append(line, p.right(p.cell(s), c.w)...)
			default:
				line = append(line, p.right(p.plain(s), c.w)...)
			}
		}
		out = append(out, line)
	}
	if more > 0 {
		out = append(out, chunks{p.space(2), p.muted("+ " + strconv.Itoa(more) + " more")})
	}
	return out
}

const (
	// minPath is the narrowest a project's path is cut to, and maxPath the
	// widest a long one takes, so it does not push the numbers away.
	minPath = 16
	maxPath = 48
)

// legend is one dim line of the marks on screen, or more of even length
// where one does not fit in limit.
func (p *page) legend(limit int) string {
	g := p.g
	var items []string
	add := func(on bool, s string) {
		if on {
			items = append(items, s)
		}
	}
	s := p.seen
	add(s["used"], g.used+" used")
	add(s["left"], g.left+" left")
	switch {
	case s["tick"] && s["tickIn"]:
		items = append(items, g.tick+g.tickIn+" even use by now")
	case s["tick"]:
		items = append(items, g.tick+" even use by now")
	case s["tickIn"]:
		items = append(items, g.tickIn+" even use by now")
	}
	add(s["dotted"], g.unread+" no reading")
	switch {
	case s["stale"] && s["silent"]:
		items = append(items, g.silent+" old reading, or silent")
	case s["stale"]:
		items = append(items, g.silent+" old reading")
	case s["silent"]:
		items = append(items, g.silent+" silent")
	}
	switch {
	case s["unknown"] && s["unread"]:
		items = append(items, "? not known, or not read since a refusal")
	case s["unknown"]:
		items = append(items, "? not known")
	case s["unread"]:
		items = append(items, "? not read since refusal")
	}
	add(s["dash"], g.dash+" no forecast")
	switch {
	case s["here"] && s["this"]:
		items = append(items, g.here+" this device, or logged in here")
	case s["here"]:
		items = append(items, g.here+" logged in here")
	case s["this"]:
		items = append(items, g.here+" this device")
	}
	add(s["fail"], g.fail+" error")
	add(s["old"], g.old+" old release")
	add(s["none"], g.none+" none")
	add(s["chosen"], g.open+g.shut+" chosen")
	if len(items) == 0 {
		return ""
	}
	wrap := func(limit int) []string {
		var lines []string
		cur := ""
		for _, it := range items {
			switch {
			case cur == "":
				cur = it
			case width(cur)+2+width(it) <= limit:
				cur += "  " + it
			default:
				lines = append(lines, cur)
				cur = it
			}
		}
		return append(lines, cur)
	}
	widest := 0
	for _, it := range items {
		widest = max(widest, width(it))
	}
	limit = min(max(limit, widest), p.w)
	// The narrowest limit that needs no more lines, so the last line is not
	// a lone item.
	n := len(wrap(limit))
	for limit > widest && len(wrap(limit-1)) == n {
		limit--
	}
	lines := wrap(limit)
	for i, l := range lines {
		lines[i] = chunks{p.muted(truncEnd(l, p.w, p.g.ell))}.String()
	}
	return strings.Join(lines, "\n")
}
