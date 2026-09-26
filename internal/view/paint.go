package view

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// marks are the glyphs the page draws, in UTF-8 or in ASCII.
type marks struct {
	used, left, tick, tickIn, unread string // a bar: ━ ─ ┃ ╋, and ┈ with no reading
	here, fail, silent, old          string // ● × ~ ↓
	none, sep, dash, ell, rule       string // · " · " — … ─
	open, shut, times                string // ‹ › ×
	ok, warn, partial, staged        string // ✓ ! ◐ ↑, on the card
}

var utf8Marks = marks{
	used: "━", left: "─", tick: "┃", tickIn: "╋", unread: "┈",
	here: "●", fail: "×", silent: "~", old: "↓",
	none: "·", sep: " · ", dash: "—", ell: "…", rule: "─",
	open: "‹", shut: "›", times: "×",
	ok: "✓", warn: "!", partial: "◐", staged: "↑",
}

var asciiMarks = marks{
	used: "=", left: "-", tick: "|", tickIn: "+", unread: ".",
	here: "*", fail: "x", silent: "~", old: "v",
	none: ".", sep: " . ", dash: "-", ell: "...", rule: "-",
	open: "[", shut: "]", times: "x",
	ok: "+", warn: "!", partial: "/", staged: "^",
}

// legend is one dim line of the marks on screen, a word or two each, so a
// page 120 wide that shows every mark has it in one line. Where one line
// does not fit the width, it takes as few lines as it can, of even length.
// The interactive view's help says more of each mark.
func (p *page) legend() string {
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
		items = append(items, g.tick+g.tickIn+" even use")
	case s["tick"]:
		items = append(items, g.tick+" even use")
	case s["tickIn"]:
		items = append(items, g.tickIn+" even use")
	}
	add(s["dotted"], g.unread+" no reading")
	// An old reading and a silent device are both stale.
	add(s["stale"] || s["silent"], g.silent+" stale")
	// A window not read since a refusal is not known either.
	add(s["unknown"] || s["unread"], "? unknown")
	add(s["dash"], g.dash+" no forecast")
	// An account logged in here, and this device, are both here.
	add(s["here"] || s["this"], g.here+" here")
	add(s["fail"], g.fail+" error")
	add(s["old"], g.old+" old")
	add(s["none"], g.none+" none")
	add(s["chosen"], g.open+g.shut+" chosen")
	if len(items) == 0 {
		return ""
	}
	wrap := func(limit int) []string { return wrapItems(items, "  ", limit, limit) }
	widest := 0
	for _, it := range items {
		widest = max(widest, width(it))
	}
	limit := p.w
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

// chunk is a run of text in one style. A chunk with no style is written as
// it is.
type chunk struct {
	text   string
	st     lipgloss.Style
	styled bool
}

// chunks is one line of the page.
type chunks []chunk

func (l chunks) width() int {
	n := 0
	for _, c := range l {
		n += width(c.text)
	}
	return n
}

// drawn is how much of the line String draws: how many chunks, and the text
// of the last of them without its trailing spaces. Spaces in reverse video
// show, so they are kept.
func (l chunks) drawn() (int, string) {
	for end := len(l); end > 0; end-- {
		c := l[end-1]
		last := c.text
		if !c.st.GetReverse() {
			last = strings.TrimRight(last, " ")
		}
		if last != "" {
			return end, last
		}
	}
	return 0, ""
}

// drawnWidth is how wide String draws the line: its width without the
// trailing spaces it leaves out, such as those after an unchosen pill.
func (l chunks) drawnWidth() int {
	end, last := l.drawn()
	if end == 0 {
		return 0
	}
	return l[:end-1].width() + width(last)
}

// String draws the line without its trailing spaces. Whitespace is never
// styled, except in reverse video, where it shows.
func (l chunks) String() string {
	end, last := l.drawn()
	var b strings.Builder
	for i := range end {
		c := l[i]
		t := c.text
		if i == end-1 {
			t = last
		}
		if c.styled && (strings.TrimSpace(t) != "" || c.st.GetReverse()) {
			b.WriteString(c.st.Render(t))
		} else {
			b.WriteString(t)
		}
	}
	return b.String()
}

// cut keeps the start of l within w columns.
func (l chunks) cut(w int, ell string) chunks {
	if l.width() <= w {
		return l
	}
	var out chunks
	left := w - width(ell)
	for _, c := range l {
		if width(c.text) <= left {
			out = append(out, c)
			left -= width(c.text)
			continue
		}
		c.text = prefix(c.text, max(left, 0)) + ell
		return append(out, c)
	}
	return out
}

// padTo pads l with spaces to w columns.
func (l chunks) padTo(w int) chunks {
	if d := w - l.width(); d > 0 {
		return append(l, chunk{text: strings.Repeat(" ", d)})
	}
	return l
}

// The page's styles. With color, each takes its token from the theme.
// Without it, only bold and reverse video are left: the page for a terminal
// that shows no color, as under NO_COLOR, where words, marks, and bold carry
// the meaning. Where there is no terminal at all, as in a pipe, the CLI's
// writer strips those too.

func (p *page) paint(text string, fg color.Color, bold, reverse bool) chunk {
	st := lipgloss.NewStyle()
	styled := false
	if p.o.Color && fg != nil {
		st, styled = st.Foreground(fg), true
	}
	if bold {
		st, styled = st.Bold(true), true
	}
	if reverse {
		st, styled = st.Reverse(true), true
	}
	return chunk{text: text, st: st, styled: styled}
}

func (p *page) plain(s string) chunk  { return chunk{text: s} }
func (p *page) bold(s string) chunk   { return p.paint(s, nil, true, false) }
func (p *page) muted(s string) chunk  { return p.paint(s, p.th.Muted, false, false) }
func (p *page) faint(s string) chunk  { return p.paint(s, p.th.Faint, false, false) }
func (p *page) accent(s string) chunk { return p.paint(s, p.th.Accent, false, false) }

// ink is s as the card draws it: in fg, one of the 16 base colors, which
// follow the terminal's light or dark theme, and bold where bold is set.
// Without color the card is plain text, with no bold either.
func (p *page) ink(s string, fg color.Color, bold bool) chunk {
	if !p.o.Color {
		return p.plain(s)
	}
	return p.paint(s, fg, bold, false)
}

// inState is text in the color of a forecast state.
func (p *page) inState(s, state string) chunk { return p.paint(s, p.th.State(state), false, false) }

// badge is a word in reverse video, in the color of what it reports.
func (p *page) badge(s string, c color.Color) chunk { return p.paint(s, c, false, true) }

// space is n blanks.
func (p *page) space(n int) chunk { return chunk{text: strings.Repeat(" ", max(n, 0))} }

// left puts c at the start of a cell w wide, right at its end.
func (p *page) left(c chunk, w int) chunks  { return chunks{c}.padTo(w) }
func (p *page) right(c chunk, w int) chunks { return chunks{p.space(w - width(c.text)), c} }

// align puts c at the end of a cell w wide where right is set, else at its
// start.
func (p *page) align(c chunks, w int, right bool) chunks {
	if right {
		return append(chunks{p.space(w - c.width())}, c...)
	}
	return c.padTo(w)
}
