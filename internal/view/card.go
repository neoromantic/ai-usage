package view

import (
	"image/color"
	"strings"
	"time"
)

// card is `ai-usage status` or the guide while it is drawn: lines of text in
// the 16 base colors, on the page's width, clock, and marks.
type card struct {
	*page
	lines []chunks
}

// newCard is the card of r. Two of its marks are its own: ✕ for a failure,
// and " - " between items in ASCII.
func newCard(r *Report, o Options) *card {
	c := &card{page: newPage(r, o)}
	if o.ASCII {
		c.g.sep = " - "
	} else {
		c.g.fail = "✕"
	}
	return c
}

func (c *card) emit(l chunks) { c.lines = append(c.lines, l) }
func (c *card) blank()        { c.emit(nil) }

func (c *card) String() string {
	var b strings.Builder
	for _, l := range c.lines {
		b.WriteString(l.String())
		b.WriteByte('\n')
	}
	return b.String()
}

// clockAt is t in the card's zone, in layout.
func (c *card) clockAt(t time.Time, layout string) string { return t.In(c.loc).Format(layout) }

// hang prints parts in ink, one a line, the first after lead and the rest
// under the first.
func (c *card) hang(lead chunks, parts []string, ink color.Color) {
	pad := chunks{c.space(lead.width())}
	for i, part := range parts {
		if i > 0 {
			lead = pad
		}
		c.emit(append(lead, c.ink(part, ink, false)))
	}
}

// wrapWords wraps s at spaces within w columns. A word wider than that is
// broken after a slash, or else where it has to be, and nothing is lost.
func wrapWords(s string, w int) []string {
	var out []string
	cur := ""
	for word := range strings.SplitSeq(s, " ") {
		switch {
		case cur == "":
			cur = word
		case width(cur+" "+word) <= w:
			cur += " " + word
		default:
			out = append(out, cur)
			cur = word
		}
		if width(cur) > w {
			parts := wrapAfter(cur, "/", w)
			out = append(out, parts[:len(parts)-1]...)
			cur = parts[len(parts)-1]
		}
	}
	return append(out, cur)
}

// wrapAfter splits s after each sep so that every piece fits w columns;
// a piece still too wide is broken at w.
func wrapAfter(s, sep string, w int) []string {
	var out []string
	cur := ""
	for _, piece := range strings.SplitAfter(s, sep) {
		if cur != "" && width(cur+piece) > w {
			out = append(out, cur)
			cur = ""
		}
		cur += piece
		for width(cur) > w && w > 0 {
			head := prefix(cur, w)
			out = append(out, head)
			cur = cur[len(head):]
		}
	}
	return append(out, cur)
}
