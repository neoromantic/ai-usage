package view

import (
	"encoding/json"
	"math"
	"strconv"
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

// Known says the period's tokens in u are all known.
func (p Period) Known(u Usage) bool { return u.Unknown&p.bit() == 0 }

// days is how many UTC days the period spans, the report's own last.
func (p Period) days() int {
	switch p {
	case Today:
		return 1
	case Month:
		return 30
	case Quarter:
		return 90
	default:
		return 7
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

func (p Period) bit() PeriodSet { return 1 << p }

// PeriodSet is a set of periods. In JSON it is the list of their names, in
// the order `p` steps through them.
type PeriodSet uint8

func (s PeriodSet) MarshalJSON() ([]byte, error) {
	names := []string{}
	for _, p := range Periods {
		if s&p.bit() != 0 {
			names = append(names, p.String())
		}
	}
	return json.Marshal(names)
}

func (s *PeriodSet) UnmarshalJSON(b []byte) error {
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return err
	}
	*s = 0
	for _, n := range names {
		for _, p := range Periods {
			if p.String() == n {
				*s |= p.bit()
			}
		}
	}
	return nil
}

// Options say how the console draws the report.
type Options struct {
	// Width is the terminal's width. The page is laid out for 80 to 160
	// columns, and a width outside that is clamped; 0 is 80. No line is
	// wider than the clamped width minus one.
	Width int
	// Color styles the page with the theme. Without it, the page still uses
	// bold and reverse video, which are not colors: section titles, the
	// largest value in each matrix column, and the ATTENTION badges. Words
	// and marks carry the meaning, so a writer that strips every escape, as
	// the CLI's does for a pipe, loses nothing that matters.
	Color bool
	// Dark picks the theme's values for a dark background.
	Dark bool
	// ASCII swaps every glyph for a plain one, and every other rune of the
	// data, such as a path's, for "?".
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
	// DeviceStatus shows DEVICES as its status table, a row per device with
	// its release, when it last reported, what it reads, and its tokens in
	// every period, instead of the matrix. A team of one device shows USAGE
	// either way.
	DeviceStatus bool
	// Interactive draws the page for the interactive view: every ATTENTION
	// line and no legend, which is in its help.
	Interactive bool
	// MatrixScroll is how many subscription columns the matrix skips from
	// the left, with the device column and the headers kept.
	MatrixScroll int
	// Busy is the frame of the spinner the header shows while a collection
	// the interactive view started runs; empty when none runs. It should be
	// ASCII when ASCII is.
	Busy string
}

// Page is the report drawn for a terminal: a header line that stays at the
// top, and the body that scrolls under it. The lines carry escapes as
// Options.Color says, and none ends in a space.
type Page struct {
	Header string
	// Body is the page under the header, one entry per line: a blank line
	// under the header, then the sections with a blank line between two, and
	// none after the last.
	Body []string
	// Legend is the marks on screen, in one line, or more where one does
	// not fit the width. It is empty when there are none, and always in the
	// interactive view.
	Legend string
	// MatrixColumns is how many subscription columns the matrix has, and
	// MatrixShown how many of them fit from MatrixScroll on. Both are 0 on a
	// page with no matrix, as with a single device or in the status view.
	MatrixColumns, MatrixShown int
	// DeviceViews says DEVICES has two views, the matrix and the status
	// table, as on a team of more than one device.
	DeviceViews bool
	// Width is how wide the widest line of the header and the body is. The
	// header's right side ends there.
	Width int
}

// Render draws the report as one page. It reads no clock: now is the
// report's GeneratedAt.
func Render(r Report, o Options) Page {
	p := newPage(&r, o)
	var body []chunks
	for _, s := range [][]chunks{p.attention(), p.subscriptions(), p.devices(), p.projects()} {
		if len(s) == 0 {
			continue
		}
		// A blank line under the header, and between two sections.
		body = append(body, nil)
		body = append(body, s...)
	}
	width := 0
	for i, l := range body {
		body[i] = l.cut(p.w, p.g.ell)
		width = max(width, body[i].width())
	}
	head := p.header(width)
	width = max(width, head.width())
	out := Page{Header: head.String(), Body: make([]string, len(body)), Width: width,
		MatrixColumns: p.matrixColumns, MatrixShown: p.matrixShown, DeviceViews: p.deviceViews()}
	for i, l := range body {
		out.Body[i] = l.String()
	}
	if !o.Interactive {
		out.Legend = p.legend()
	}
	return out
}

// Text is the static report: the header, the page, and the legend, with a
// blank line between them.
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

const (
	// attentionLines is how many ATTENTION lines the static page shows.
	attentionLines = 6
	// topProjects is how many projects PROJECTS shows without AllProjects.
	topProjects = 10
)

// page is the report while it is drawn.
type page struct {
	r    *Report
	o    Options
	w    int // the widest a line may be: the width minus one
	g    marks
	th   Theme
	now  time.Time
	loc  *time.Location
	home string
	// seen are the marks on screen, for the legend.
	seen map[string]bool

	matrixColumns, matrixShown int
}

func newPage(r *Report, o Options) *page {
	t := o.Width
	if t == 0 {
		t = minWidth
	}
	p := &page{r: r, o: o, w: clamp(t, minWidth, maxWidth) - 1, g: utf8Marks, th: NewTheme(o.Dark),
		now: r.GeneratedAt, loc: o.Loc, home: homeOf(r), seen: map[string]bool{}}
	if o.ASCII {
		p.g = asciiMarks
	}
	if p.loc == nil {
		p.loc = time.Local
	}
	return p
}

// txt is text from the data. In ASCII mode, a rune past ASCII is "?".
func (p *page) txt(s string) string {
	if !p.o.ASCII {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r >= 0x80 {
			return '?'
		}
		return r
	}, s)
}

// mark notes a mark on screen, for the legend.
func (p *page) mark(k string) { p.seen[k] = true }

// clock is a time as the page shows it: a weekday and a 24-hour clock
// within six days, else a date.
func (p *page) clock(t time.Time) string {
	if d := t.Sub(p.now); d < 6*24*time.Hour && d > -6*24*time.Hour {
		return t.In(p.loc).Format("Mon 15:04")
	}
	return t.In(p.loc).Format("2 Jan 15:04")
}

// path prints a path under the user's home with ~.
func (p *page) path(s string) string {
	if p.home != "" {
		if s == p.home {
			return "~"
		}
		for _, sep := range []string{"/", `\`} {
			if strings.HasPrefix(s, p.home+sep) {
				return "~" + strings.TrimPrefix(s, p.home)
			}
		}
	}
	return s
}

// dur prints a duration in at most two units: 34m, 7h 5m, 1d 23h, 12d.
func dur(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	m := int(d / time.Minute)
	days, hours, mins := m/(60*24), m/60%24, m%60
	switch {
	case days > 0 && hours > 0:
		return strconv.Itoa(days) + "d " + strconv.Itoa(hours) + "h"
	case days > 0:
		return strconv.Itoa(days) + "d"
	case hours > 0 && mins > 0:
		return strconv.Itoa(hours) + "h " + strconv.Itoa(mins) + "m"
	case hours > 0:
		return strconv.Itoa(hours) + "h"
	default:
		return strconv.Itoa(mins) + "m"
	}
}

// millions prints tokens in whole millions: 603, 1210, <1 under a million,
// and the none mark for nothing.
func (p *page) millions(n int64) string {
	switch {
	case n <= 0:
		return p.g.none
	case n < 1_000_000:
		return "<1"
	default:
		return strconv.FormatInt(int64(math.Round(float64(n)/1e6)), 10)
	}
}

// tokens prints a period's tokens in u as millions does, when they are all
// known. When some are not, as a device's on a collector older than v0.2.0,
// it prints ≥ before the whole millions of those that are, rounded down so
// the bound holds, or ? when they are under a million, so a sum with a part
// missing never reads as exact.
func (p *page) tokens(per Period, u Usage) string {
	n := per.Of(u)
	switch {
	case per.Known(u):
		return p.millions(n)
	case n < 1_000_000:
		return "?"
	}
	return p.g.atLeast + strconv.FormatInt(n/1_000_000, 10)
}

// percent prints a share of a window in whole percents, <1 under one, and
// the none mark for nothing.
func (p *page) percent(v *float64) string {
	switch {
	case v == nil || *v <= 0:
		return p.g.none
	case *v < 1:
		return "<1"
	default:
		return strconv.Itoa(int(math.Round(*v)))
	}
}

// shortID is a label that is a UUID by its first 8 hex digits, as a short
// git hash is known; any other label as it is.
func shortID(label string) string {
	if uuidRe.MatchString(label) {
		return label[:8]
	}
	return label
}

// account is the team account of provider with label, or nil.
func (p *page) account(provider, label string) *TeamAccount {
	for i := range p.r.Team.Providers {
		tp := &p.r.Team.Providers[i]
		if tp.Provider != provider {
			continue
		}
		for j := range tp.Accounts {
			if tp.Accounts[j].Label == label {
				return &tp.Accounts[j]
			}
		}
	}
	return nil
}

// device is the team device with the ID or label, or nil.
func (p *page) device(id, label string) *TeamDevice {
	for i := range p.r.Team.Devices {
		d := &p.r.Team.Devices[i]
		if (id != "" && d.Device == id) || (id == "" && d.Label == label) {
			return d
		}
	}
	return nil
}
