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
	open, shut, times, atLeast       string // ‹ › × ≥
}

var utf8Marks = marks{
	used: "━", left: "─", tick: "┃", tickIn: "╋", unread: "┈",
	here: "●", fail: "×", silent: "~", old: "↓",
	none: "·", sep: " · ", dash: "—", ell: "…", rule: "─",
	open: "‹", shut: "›", times: "×", atLeast: "≥",
}

var asciiMarks = marks{
	used: "=", left: "-", tick: "|", tickIn: "+", unread: ".",
	here: "*", fail: "x", silent: "~", old: "v",
	none: ".", sep: " . ", dash: "-", ell: "...", rule: "-",
	open: "[", shut: "]", times: "x", atLeast: ">=",
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

// inState is text in the color of a forecast state.
func (p *page) inState(s, state string) chunk { return p.paint(s, p.th.State(state), false, false) }

// badge is a word in reverse video, in the color of what it reports.
func (p *page) badge(s string, c color.Color) chunk { return p.paint(s, c, false, true) }

// space is n blanks.
func (p *page) space(n int) chunk { return chunk{text: strings.Repeat(" ", max(n, 0))} }

// left puts c at the start of a cell w wide, right at its end.
func (p *page) left(c chunk, w int) chunks  { return chunks{c}.padTo(w) }
func (p *page) right(c chunk, w int) chunks { return chunks{p.space(w - width(c.text)), c} }
