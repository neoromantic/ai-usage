package view

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
)

// acctCols are the account table's column widths for one terminal width.
// plan and used are 0 when the column is not shown.
type acctCols struct {
	label, plan, bar, used int
}

const (
	minLabel = 16
	maxLabel = 36
	maxPlan  = 14
	winCell  = 11
	readCell = 4
)

// acctLayout sizes ACCOUNT to its widest shown label, up to a cap: all the
// room there is on one device, half of it beside USED BY on a team.
func (u *ui) acctLayout(rows []arow) acctCols {
	c := acctCols{bar: 6}
	if u.wide {
		c.bar = 10
		for _, a := range rows {
			if a.plan != "" {
				c.plan = max(c.plan, width("PLAN"), min(width(a.plan), maxPlan))
			}
		}
	}
	fixed := 2 + 2 + c.bar + 1 + 4 + 3 + 2 + winCell + 1 + winCell + 2 + readCell
	if c.plan > 0 {
		fixed += c.plan + 1
	}
	rest := u.w - fixed
	limit := min(rest, maxLabel)
	if u.multi {
		rest -= 2
		limit = clamp(rest/2, minLabel, maxLabel)
	}
	c.label = width("ACCOUNT")
	for _, a := range rows {
		c.label = max(c.label, width(truncLabel(a.label, limit, u.g.ell)))
	}
	if u.multi {
		c.used = rest - c.label
	}
	return c
}

// arow is one row of the account table: a team account, with what this
// device knows about it.
type arow struct {
	provider, label, plan string
	head                  *float64
	level                 string
	q                     *Quota
	here                  string // "here", "seen", or ""
	consumers             []string
	link                  *Link
	linked                []LinkedUsage
}

// mirror is a row whose reading is its linked account's.
func (a arow) mirror() bool { return a.q != nil && a.q.From != "" }

func (u *ui) accountRows(provider string) []arow {
	local := map[string]Account{}
	if p := u.provider(provider); p != nil {
		for _, a := range p.Accounts {
			local[a.Label] = a
		}
	}
	var rows []arow
	for _, tp := range u.r.Team.Providers {
		if tp.Provider != provider {
			continue
		}
		for _, a := range tp.Accounts {
			row := arow{provider: provider, label: a.Label, head: a.HeadlinePercent, level: a.Level, q: a.Quota,
				link: a.Link, linked: a.LinkedUsage}
			if a.Plan != nil {
				row.plan = *a.Plan
			}
			if la, ok := local[a.Label]; ok {
				row.here = "seen"
				if la.Current {
					row.here = "here"
				}
				if row.plan == "" && la.Plan != nil {
					row.plan = *la.Plan
				}
				if row.link == nil {
					row.link = la.Link
				}
				// Only this device knows where its own reading came from.
				if row.q != nil && la.Quota != nil && row.q.Source == "" && la.Quota.ObservedAt.Equal(row.q.ObservedAt) {
					q := *row.q
					q.Source = la.Quota.Source
					row.q = &q
				}
			}
			row.consumers = u.consumerNames(a)
			rows = append(rows, row)
		}
	}
	// Fullest headline first; no headline last.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.head == nil) != (b.head == nil) {
			return a.head != nil
		}
		if a.head != nil && *a.head != *b.head {
			return *a.head > *b.head
		}
		return a.label < b.label
	})
	return rows
}

// consumerNames orders the devices that used an account by their tokens.
func (u *ui) consumerNames(a TeamAccount) []string {
	var names []string
	if len(a.PerDevice) > 0 {
		ds := append([]DeviceUsage(nil), a.PerDevice...)
		sort.SliceStable(ds, func(i, j int) bool { return ds[i].Tokens.Total() > ds[j].Tokens.Total() })
		for _, d := range ds {
			names = append(names, u.shortDevice(d.Device))
		}
		return names
	}
	for _, d := range a.Devices {
		names = append(names, u.shortDevice(d))
	}
	return names
}

// splitHostUser undoes hostUser. The OS user is the last parenthesis, since a
// name given with `ai-usage name set` may have one of its own.
func splitHostUser(s string) (host, user string) {
	i := strings.LastIndex(s, " (")
	if i < 0 || !strings.HasSuffix(s, ")") {
		return s, ""
	}
	return s[:i], s[i+2 : len(s)-1]
}

// shortDevice is the host, or host/user when the team has two users on it.
func (u *ui) shortDevice(hostUser string) string {
	host, user := splitHostUser(hostUser)
	n := 0
	for _, d := range u.r.Team.Devices {
		if d.Label == host {
			n++
		}
	}
	if n > 1 {
		return host + "/" + user
	}
	return host
}

func (u *ui) accounts() {
	var all []arow
	for _, p := range collect.Providers {
		all = append(all, u.accountRows(p)...)
	}
	c := u.acctLayout(all)
	crit, warn, pace, stale, unknown := 0, 0, 0, 0, 0
	for _, a := range all {
		if a.mirror() || a.noSource() {
			continue
		}
		switch {
		case a.head == nil:
			unknown++
		case a.level == "critical":
			crit++
		case a.level == "warning":
			warn++
		}
		if a.q != nil && a.q.Stale && a.head != nil {
			stale++
		}
		if a.q != nil && u.fillsEarly(a.q) != nil {
			pace++
		}
	}
	title := line{{"ACCOUNTS", bold}, {"  " + strconv.Itoa(len(all)), plain}}
	add := func(n int, text string, st style) {
		if n > 0 {
			title = append(title, seg{u.g.sep, gray}, seg{strconv.Itoa(n) + " " + text, st})
		}
	}
	add(crit, "critical", red)
	add(warn, "warning", yellow)
	add(pace, "fills before reset", magenta)
	add(stale, "stale", yellow)
	add(unknown, "unknown", gray)
	if title.width() > u.w {
		for i := range title {
			title[i].text = strings.Replace(title[i].text, "fills before reset", "fills early", 1)
		}
	}
	u.titleWrap(title, width("ACCOUNTS  "))

	hdr := "  " + padRight("ACCOUNT", c.label) + "  "
	if c.plan > 0 {
		hdr += padRight("PLAN", c.plan) + " "
	}
	hdr += padRight("QUOTA", c.bar+1+4+3) + "  " +
		padLeft("5h", 4) + "  " + padLeft("reset", 5) + " " +
		padLeft("7d", 4) + "  " + padLeft("reset", 5) + "  " +
		padLeft("READ", readCell)
	if c.used > 0 {
		hdr += "  USED BY"
	}
	u.emit(line{{hdr, gray}})

	for _, p := range collect.Providers {
		rows := u.accountRows(p)
		u.emit(u.providerRule(p, len(rows)))
		var quiet []string
		for _, a := range rows {
			if a.noSource() {
				quiet = append(quiet, a.label)
				continue
			}
			u.accountRow(c, a)
		}
		// Accounts that can never have a quota take one line, not a row
		// each; their tokens are in THIS DEVICE and --tokens.
		if len(quiet) > 0 {
			u.emit(line{{"  " + strings.Join(quiet, ", ") + u.g.sep + "no quota source, tokens only", gray}}.cut(u.w, u.g.ell))
		}
	}
}

// noSource is an account no harness reports a quota for: Hermes on an API
// key or a route it does not bill through another harness's login.
func (a arow) noSource() bool { return a.provider == "hermes" && a.q == nil && a.link == nil }

func (u *ui) providerRule(p string, n int) line {
	note := strconv.Itoa(n) + " accounts"
	switch n {
	case 0:
		note = "no accounts"
	case 1:
		note = "1 account"
	}
	if src := u.provider(p); src != nil {
		switch src.Status {
		case "skipped":
			note += u.g.sep + "not installed here"
		case "error", "partial":
			note += u.g.sep + u.g.fail + " " + src.Status + " here"
		}
	}
	head := line{{"  ", plain}, {p, bold}, {" ", plain}}
	tail := line{{" " + note, gray}}
	fill := u.page - head.width() - tail.width()
	return append(append(head, seg{strings.Repeat(u.g.rule, max(fill, 3)), gray}), tail...)
}

func (u *ui) accountRow(c acctCols, a arow) {
	g := u.g
	l := line{}
	switch a.here {
	case "here":
		l = append(l, seg{g.here + " ", cyan})
		u.legend["here"] = true
	case "seen":
		l = append(l, seg{g.seen + " ", gray})
		u.legend["seen"] = true
	default:
		l = append(l, seg{"  ", plain})
	}
	l = append(l, seg{padRight(truncLabel(a.label, c.label, g.ell), c.label) + "  ", plain})
	if c.plan > 0 {
		l = append(l, seg{padRight(truncEnd(a.plan, c.plan, g.ell), c.plan) + " ", gray})
	}
	// A borrowed reading is drawn in gray: the same quota already has its
	// row. An old reading keeps its colors, since a window only fills until
	// it resets; READ and the note say how old it is.
	borrowed := a.mirror()
	l = append(l, u.headlineCell(a.head, a.level, borrowed, c.bar)...)
	l = append(l, seg{"  ", plain})

	var short, long *Window
	var extra []Window
	if a.q != nil {
		short, long, extra = slots(a.q.Windows)
	}
	l = append(l, u.winCell(short, borrowed)...)
	l = append(l, seg{" ", plain})
	l = append(l, u.winCell(long, borrowed)...)
	l = append(l, seg{"  ", plain})

	switch {
	case a.q == nil:
		l = append(l, seg{padLeft(g.dash, readCell), gray})
		u.legend["dash"] = true
	case a.q.Stale && u.now.Sub(a.q.ObservedAt) >= 100*24*time.Hour:
		l = append(l, seg{padLeft(">99d", readCell), yellow})
	case a.q.Stale:
		l = append(l, seg{padLeft(age(u.now.Sub(a.q.ObservedAt))+g.stale, readCell), yellow})
		u.legend["oldReading"] = true
	default:
		l = append(l, seg{padLeft(age(u.now.Sub(a.q.ObservedAt)), readCell), plain})
	}
	if c.used > 0 {
		l = append(l, seg{"  " + nameList(a.consumers, c.used, g.ell), plain})
	}
	u.emit(l)

	for _, n := range u.notes(a, extra) {
		u.emit(n)
	}
}

// headlineCell is the bar, the percent, and the level mark: bar+1+4+3 columns.
func (u *ui) headlineCell(p *float64, level string, borrowed bool, barW int) line {
	g := u.g
	if p == nil {
		return line{{strings.Repeat(g.none, barW) + " ", gray}, {"unknown", gray}}
	}
	st := levelStyle(level, green)
	pst := map[string]style{"warning": yellow, "critical": redBold}[level]
	if borrowed {
		st, pst = gray, gray
	}
	mark := "   "
	switch level {
	case "critical":
		mark = " " + g.crit
		u.legend["crit"] = true
	case "warning":
		mark = " " + g.warnMark + " "
		u.legend["warn"] = true
	}
	fill, track := u.bar(*p, barW)
	return line{{fill, st}, {track, gray}, {" " + padLeft(pctText(*p), 4), pst}, {mark, pst}}
}

// levelStyle colors a level: yellow warning, red critical, ok as given.
func levelStyle(level string, ok style) style {
	switch level {
	case "critical":
		return red
	case "warning":
		return yellow
	}
	return ok
}

// bar draws p percent in w cells. It is never empty above 0% and never
// full below 100%.
func (u *ui) bar(p float64, w int) (fill, track string) {
	g := u.g
	p = math.Max(0, math.Min(100, p))
	if len(g.eighths) == 0 {
		n := int(math.Floor(p / 100 * float64(w)))
		if n == 0 && p > 0 {
			n = 1
		}
		if n == w && p < 100 {
			n = w - 1
		}
		return strings.Repeat(g.full, n), strings.Repeat(g.track, w-n)
	}
	e := int(math.Floor(p / 100 * float64(w*8)))
	if e == 0 && p > 0 {
		e = 1
	}
	if e == w*8 && p < 100 {
		e = w*8 - 1
	}
	full, rem := e/8, e%8
	fill = strings.Repeat(g.full, full)
	cells := full
	if rem > 0 {
		fill += g.eighths[rem]
		cells++
	}
	return fill, strings.Repeat(g.track, w-cells)
}

// slots puts each window in the short (a day or less) or the weekly column.
// A window named exactly 5h or 7d takes its column over from another name.
// The rest, such as "7d Opus", are extras for a note line, in report order.
func slots(ws []Window) (short, long *Window, extra []Window) {
	si, li := -1, -1
	for i, w := range ws {
		slot, canon := &li, "7d"
		if isShort(w) {
			slot, canon = &si, "5h"
		}
		if *slot < 0 || (w.Name == canon && ws[*slot].Name != canon) {
			*slot = i
		}
	}
	for i := range ws {
		switch i {
		case si:
			short = &ws[i]
		case li:
			long = &ws[i]
		default:
			extra = append(extra, ws[i])
		}
	}
	return short, long, extra
}

// isShort is a window of a day or less; without minutes, one whose name
// starts with a count of hours.
func isShort(w Window) bool {
	if w.Minutes > 0 {
		return w.Minutes <= 24*60
	}
	return strings.HasSuffix(strings.Fields(w.Name + " x")[0], "h")
}

// hasReset is whether a window's reset time has passed since the reading.
func (u *ui) hasReset(w Window) bool { return w.ResetsAt != nil && !w.ResetsAt.After(u.now) }

// winCell is 11 columns: percent 4, pace flag 1, space, reset countdown 5.
func (u *ui) winCell(w *Window, borrowed bool) line {
	g := u.g
	if w == nil {
		u.legend["dash"] = true
		return line{{padLeft(g.dash, 4) + "       ", gray}}
	}
	if u.hasReset(*w) {
		u.legend["reset"] = true
		return line{{padLeft(g.question, 4) + "  reset", gray}}
	}
	st := levelStyle(w.Level, plain)
	if borrowed {
		st = gray
	}
	flag := seg{" ", plain}
	if w.Pace != nil && w.Pace.FillsAt != nil && w.Pace.FillsAt.After(u.now) {
		flag = seg{g.pace, magenta}
		if borrowed {
			flag.st = gray
		}
		u.legend["pace"] = true
	}
	reset := g.dash
	if w.ResetsAt != nil {
		reset = span(w.ResetsAt.Sub(u.now))
	} else {
		u.legend["dash"] = true
	}
	return line{{padLeft(pctText(w.Percent), 4), st}, flag, {" " + padLeft(reset, 5), gray}}
}

// fillsEarly is the first window that fills before it resets at this pace.
func (u *ui) fillsEarly(q *Quota) *Window {
	for i := range q.Windows {
		w := &q.Windows[i]
		if w.Pace != nil && w.Pace.FillsAt != nil && w.Pace.FillsAt.After(u.now) && !u.hasReset(*w) {
			return w
		}
	}
	return nil
}

// staleWhy says why a reading is old, when its source says.
var staleWhy = map[string]string{
	"cache": "Claude Code updates its usage cache only while it runs",
	"log":   "grok writes its usage log only while it runs",
}

// notes are the lines under an account row, in a fixed order: extra
// windows, pace, why there is no headline or why the reading is old, and the
// links between Hermes and the account it bills through. A borrowed reading
// leaves its extra windows and pace to the row it is borrowed from.
func (u *ui) notes(a arow, extra []Window) []line {
	g := u.g
	var out []line
	lead := "  " + g.branch + " "
	emit := func(parts line) {
		out = append(out, append(line{{lead, gray}}, parts...).cut(u.w, g.ell))
	}
	if len(extra) > 0 && !a.mirror() {
		parts := line{{"also ", gray}}
		for i, w := range extra {
			if i > 0 {
				parts = append(parts, seg{g.sep, gray})
			}
			parts = append(parts, u.windowPhrase(w)...)
		}
		emit(parts)
	}
	if a.q != nil && !a.mirror() {
		if w := u.fillsEarly(a.q); w != nil {
			text := g.pace + " " + w.Name + " full in " + span(w.Pace.FillsAt.Sub(u.now)) + " (" + u.clock(*w.Pace.FillsAt, "Mon 15:04") + ") at this pace"
			if w.ResetsAt != nil {
				text += ", " + span(w.ResetsAt.Sub(*w.Pace.FillsAt)) + " before it resets"
			}
			u.legend["pace"] = true
			emit(line{{text, magenta}})
		}
	}
	switch {
	case a.q == nil && a.link != nil:
		emit(line{{"quota of " + joinWords(a.link.Provider, a.link.Label) + ", which has no reading yet", gray}})
	case a.q == nil:
		emit(line{{"no reading yet", gray}})
	case a.head == nil:
		emit(line{{"every window reset since the reading " + ago(u.now.Sub(a.q.ObservedAt)), gray}})
	case a.q.Stale && staleWhy[a.q.Source] != "":
		u.legend["oldReading"] = true
		emit(line{{g.stale + " ", yellow}, {staleWhy[a.q.Source], gray}})
	case a.q.Stale && a.q.Device != "":
		u.legend["oldReading"] = true
		host, _ := splitHostUser(a.q.Device)
		emit(line{{g.stale + " ", yellow}, {"last read on " + host + " " + ago(u.now.Sub(a.q.ObservedAt)), gray}})
	}
	if a.mirror() {
		who := a.q.From
		if a.link != nil {
			who = joinWords(who, a.link.Label)
		}
		emit(line{{"quota of " + who + ", assumed the same account", gray}})
	}
	for _, l := range a.linked {
		var hosts []string
		for _, d := range l.Devices {
			hosts = append(hosts, u.shortDevice(d))
		}
		text := "also used by " + joinWords(l.Provider, l.Label)
		if len(hosts) > 0 {
			text += " on " + strings.Join(hosts, ", ")
		}
		emit(line{{text + ", assumed the same account", gray}})
	}
	return out
}

// joinWords joins the words that are not empty with spaces.
func joinWords(words ...string) string {
	var out []string
	for _, w := range words {
		if w != "" {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

func (u *ui) windowPhrase(w Window) line {
	g := u.g
	if u.hasReset(w) {
		u.legend["reset"] = true
		return line{{w.Name + " " + g.question + " reset", gray}}
	}
	st := levelStyle(w.Level, plain)
	mark := ""
	switch w.Level {
	case "critical":
		mark = " " + g.crit
		u.legend["crit"] = true
	case "warning":
		mark = " " + g.warnMark
		u.legend["warn"] = true
	}
	l := line{{w.Name + " ", gray}, {pctText(w.Percent) + mark, st}}
	if w.ResetsAt != nil {
		l = append(l, seg{", resets in " + span(w.ResetsAt.Sub(u.now)), gray})
	}
	return l
}
