package view

import (
	"strings"
	"time"
)

// Period is the span of tokens the matrix, USAGE, and PROJECTS show.
type Period int

const (
	Week    Period = iota // 7d, the default
	Today                 // the report's UTC day
	Month                 // 30d
	Quarter               // 90d
)

// Periods are the periods in the order `p` steps through them.
var Periods = []Period{Today, Week, Month, Quarter}

func (p Period) String() string {
	switch p {
	case Today:
		return "today"
	case Month:
		return "30d"
	case Quarter:
		return "90d"
	default:
		return "7d"
	}
}

// Of is the period's tokens in u.
func (p Period) Of(u Usage) int64 {
	switch p {
	case Today:
		return u.Today
	case Month:
		return u.Month
	case Quarter:
		return u.Quarter
	default:
		return u.Week
	}
}

// Next is the period after p, back to today after 90d.
func (p Period) Next() Period {
	for i, q := range Periods {
		if q == p {
			return Periods[(i+1)%len(Periods)]
		}
	}
	return Week
}

// Options say how the console draws the report.
type Options struct {
	// Width is the terminal's width. The page is laid out for 80 to 160
	// columns; 0 is 80.
	Width int
	// Color styles the page with the theme. Without it, words, marks, and
	// bold carry the meaning.
	Color bool
	// Dark picks the theme's values for a dark background.
	Dark bool
	// ASCII swaps every glyph for a plain one.
	ASCII bool
	// Loc is the time zone clocks are shown in; nil is local.
	Loc *time.Location
	// Period is the span of the matrix, USAGE, and PROJECTS.
	Period Period
	// Share shows each device's share of a subscription's window in the
	// matrix, instead of its tokens.
	Share bool
	// AllProjects lists every project, not the top 10.
	AllProjects bool
	// Interactive draws the page for the interactive view: every ATTENTION
	// line and no legend, which is in its help.
	Interactive bool
	// MatrixScroll is how many subscription columns the matrix skips from
	// the left, with the device column and the headers kept.
	MatrixScroll int
	// Busy is the frame of the spinner the header shows while a collection
	// the interactive view started runs; empty when none runs.
	Busy string
}

// Page is the report drawn for a terminal: a header line that stays at the
// top, and the body that scrolls under it.
type Page struct {
	Header string
	// Body is the page under the header, one entry per line.
	Body []string
	// Legend is the one line of marks on screen, empty when there are none.
	Legend string
	// MatrixColumns is how many subscription columns the matrix has, and
	// MatrixShown how many of them fit from MatrixScroll on.
	MatrixColumns, MatrixShown int
}

// Render draws the report as one page.
func Render(r Report, o Options) Page {
	return Page{Header: "ai-usage · " + r.Collector.DeviceLabel}
}

// Text is the static report: the header, the page, and the legend.
func Text(r Report, o Options) string {
	p := Render(r, o)
	var b strings.Builder
	b.WriteString(p.Header + "\n")
	for _, l := range p.Body {
		b.WriteString(l + "\n")
	}
	if p.Legend != "" {
		b.WriteString("\n" + p.Legend + "\n")
	}
	return b.String()
}
