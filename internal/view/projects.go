package view

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// projects is where this device's tokens go: its projects over every
// account, in weighted tokens in the local periods, the most in the period
// first, the top ones unless every one is asked for. Where the ledger holds
// less than 90 days of a provider, the title says since when, and the 90D
// of the projects it worked on is dim.
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

	short := p.shortCoverage(ps)
	cols, via := p.projectCols(ps, per, short)
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

	title := p.projectsTitle(per, short)
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
			line = append(line, p.align(chunks{c.ink(c.cells[i], i, c.w)}, c.w, c.right)...)
		}
		out = append(out, line)
	}
	if more > 0 {
		out = append(out, chunks{p.space(2), p.muted("+ " + strconv.Itoa(more) + " more")})
	}
	return out
}

// projectsTitle is the title of PROJECTS in the period per, with since when
// the ledger holds each provider of short. Where it is too wide, the
// coverage goes first, then this device's label.
func (p *page) projectsTitle(per Period, short []string) chunks {
	g := p.g
	unit := "by " + per.String() + g.sep + "M weighted tokens"
	label := p.txt(p.r.Collector.DeviceLabel) + g.sep
	forms := []string{label + unit, unit}
	if len(short) > 0 {
		var since []string
		for _, prov := range short {
			since = append(since, prov+" since "+p.coveredSince(prov).In(p.loc).Format("Jan 2"))
		}
		forms = slices.Insert(forms, 0, label+unit+g.sep+strings.Join(since, g.sep))
	}
	lead := chunks{p.bold("PROJECTS"), p.plain("  ")}
	for _, t := range forms {
		if line := append(lead, p.muted(t)); line.width() <= p.w {
			return line
		}
	}
	return append(lead, p.muted(unit))
}

// coverageFull is how far back the ledger must hold a provider's hours for
// PROJECTS to take its 90 days as whole. A ledger begun a few days short
// changes no number much.
const coverageFull = 85 * 24 * time.Hour

// shortCoverage are the providers of ps whose hours on this device the
// ledger holds for less than coverageFull, in the report's order.
func (p *page) shortCoverage(ps []Project) []string {
	var out []string
	for _, pv := range p.r.Providers {
		c := pv.CoveredSince
		if c == nil || p.now.Sub(*c) >= coverageFull {
			continue
		}
		if slices.ContainsFunc(ps, func(pr Project) bool { return slices.Contains(pr.Providers, pv.Provider) }) {
			out = append(out, pv.Provider)
		}
	}
	return out
}

// coveredSince is when the ledger's hours of provider begin.
func (p *page) coveredSince(provider string) time.Time {
	for _, pv := range p.r.Providers {
		if pv.Provider == provider && pv.CoveredSince != nil {
			return *pv.CoveredSince
		}
	}
	return time.Time{}
}

// projCol is a column of PROJECTS right of the path: its heading, a cell
// for each project, how wide it is, and how the cell s of the i-th project
// is drawn in it w wide.
type projCol struct {
	head  string
	right bool
	cells []string
	w     int
	ink   func(s string, i, w int) chunk
}

// projectCols are the columns of PROJECTS right of the path, with a cell
// for each of ps in the period per, each as wide as its heading and its
// widest cell. via is VIA, which goes first where the page is narrow. The
// 90D of a project a provider of short worked on is dim: it counts only
// the days the ledger holds.
func (p *page) projectCols(ps []Project, per Period, short []string) (cols []*projCol, via *projCol) {
	g := p.g
	tokens := func(s string, _, _ int) chunk { return p.cell(s) }
	quarter := func(s string, i, _ int) chunk {
		if s != g.none && slices.ContainsFunc(ps[i].Providers, func(v string) bool { return slices.Contains(short, v) }) {
			return p.muted(s)
		}
		return p.cell(s)
	}
	cols = []*projCol{{head: strings.ToUpper(per.String()), right: true, ink: tokens}}
	if per != Quarter {
		cols = append(cols, &projCol{head: "90D", right: true, ink: quarter})
	} else {
		cols[0].ink = quarter
	}
	sess := &projCol{head: "SESS", right: true, ink: tokens}
	via = &projCol{head: "VIA", ink: func(s string, _, w int) chunk { return p.muted(truncEnd(s, w, g.ell)) }}
	last := &projCol{head: "LAST", right: true, ink: func(s string, _, _ int) chunk {
		if s == g.none {
			p.mark("none")
		}
		return p.muted(s)
	}}
	cols = append(cols, sess, via, last)
	for _, pr := range ps {
		cols[0].cells = append(cols[0].cells, p.millions(per.Of(pr.Burn)))
		if per != Quarter {
			cols[1].cells = append(cols[1].cells, p.millions(pr.Burn.Quarter))
		}
		n := strconv.Itoa(*per.count(&pr.Active))
		if n == "0" {
			n = g.none
		}
		sess.cells = append(sess.cells, n)
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
