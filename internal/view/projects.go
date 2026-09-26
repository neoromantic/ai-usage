package view

import (
	"slices"
	"strconv"
	"strings"

	"github.com/neoromantic/ai-usage/internal/snapshot"
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

	cols, via := p.projectCols(ps, per)
	pathW := width("PROJECT")
	paths := make([]string, len(ps))
	for i, pr := range ps {
		// A path is as a harness logged it, and a folder's name may hold any
		// byte but NUL and a slash. Its control characters are spaces here,
		// so none reaches the terminal.
		paths[i] = p.txt(snapshot.Printable(p.path(pr.Path)))
		pathW = max(pathW, min(width(paths[i]), maxPath))
	}
	rest := 0
	for _, c := range cols {
		rest += 2 + c.w
	}
	if 2+pathW+rest > p.w {
		pathW = max(p.w-2-rest, minPath)
	}
	if 2+pathW+rest > p.w {
		// VIA goes before the paths are cut any further.
		cols = slices.DeleteFunc(cols, func(c *projCol) bool { return c == via })
		rest -= 2 + via.w
		pathW = max(p.w-2-rest, minPath)
	}

	title := chunks{p.bold("PROJECTS"), p.plain("  "),
		p.muted(p.txt(p.r.Collector.DeviceLabel) + g.sep + "by " + per.String() + g.sep + "M tokens in+out")}
	head := chunks{p.space(2)}
	head = append(head, p.left(p.muted("PROJECT"), pathW)...)
	// Every header is dim, the period the table is sorted by too: the title
	// names it.
	for _, c := range cols {
		head = append(head, p.space(2))
		head = append(head, p.align(chunks{p.muted(c.head)}, c.w, c.right)...)
	}
	out := []chunks{title, head}
	for i := range ps {
		line := chunks{p.space(2)}
		line = append(line, p.left(p.plain(truncPath(paths[i], pathW, g.ell)), pathW)...)
		for _, c := range cols {
			line = append(line, p.space(2))
			line = append(line, p.align(chunks{c.ink(c.cells[i], c.w)}, c.w, c.right)...)
		}
		out = append(out, line)
	}
	if more > 0 {
		out = append(out, chunks{p.space(2), p.muted("+ " + strconv.Itoa(more) + " more")})
	}
	return out
}

// projCol is a column of PROJECTS right of the path: its heading, a cell
// for each project, how wide it is, and how a cell is drawn in it w wide.
type projCol struct {
	head  string
	right bool
	cells []string
	w     int
	ink   func(s string, w int) chunk
}

// projectCols are the columns of PROJECTS right of the path, with a cell
// for each of ps in the period per, each as wide as its heading and its
// widest cell. via is VIA, which goes first where the page is narrow.
func (p *page) projectCols(ps []Project, per Period) (cols []*projCol, via *projCol) {
	g := p.g
	tokens := func(s string, _ int) chunk { return p.cell(s) }
	cols = []*projCol{{head: strings.ToUpper(per.String()), right: true, ink: tokens}}
	if per != Quarter {
		cols = append(cols, &projCol{head: "90D", right: true, ink: tokens})
	}
	sess := &projCol{head: "SESS", right: true, ink: func(s string, _ int) chunk { return p.plain(s) }}
	via = &projCol{head: "VIA", ink: func(s string, w int) chunk { return p.muted(truncEnd(s, w, g.ell)) }}
	last := &projCol{head: "LAST", right: true, ink: func(s string, _ int) chunk {
		if s == g.none {
			p.mark("none")
		}
		return p.muted(s)
	}}
	cols = append(cols, sess, via, last)
	for _, pr := range ps {
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
	for _, c := range cols {
		c.w = width(c.head)
		for _, s := range c.cells {
			c.w = max(c.w, width(s))
		}
	}
	return cols, via
}

const (
	// minPath is the narrowest a project's path is cut to, and maxPath the
	// widest a long one takes, so it does not push the numbers away.
	minPath = 16
	maxPath = 48
	// topProjects is how many projects PROJECTS shows without AllProjects.
	topProjects = 10
)
