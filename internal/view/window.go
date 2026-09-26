package view

import (
	"math"
	"strconv"
	"strings"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

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
	case w != nil && w.Unread:
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

// barSplit is how many of a window's n cells are used, and the cell of its
// tick: -1 when there is none, as when how much of the window had passed is
// not known.
func barSplit(w Window, n int) (used, tick int) {
	used = int(math.Round(w.Percent / 100 * float64(n)))
	switch {
	case w.Percent >= 100:
		used = n
	case w.Percent > 0 && used == 0:
		used = 1
	case used >= n:
		used = n - 1
	}
	used = max(used, 0)
	tick = -1
	if e, ok := elapsed(w); ok {
		tick = min(int(e*float64(n)), n-1)
		// The used part and the tick are rounded to cells, so where both
		// fall in one cell the tick goes by the forecast: inside the used
		// part when the window is out or over, after it otherwise.
		if w.State == StateOut || w.State == StateOver {
			tick = min(tick, used-1)
		} else {
			tick = min(max(tick, used), n-1)
		}
	}
	return used, tick
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
	used, tick := barSplit(*w, n)
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
