package view

import (
	"regexp"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// Mode is which console view to draw.
type Mode int

const (
	Default Mode = iota
	Projects
	Tokens
	Devices
)

// Options say how the console looks: the terminal width, color, the glyph
// set, the view, and the time zone clocks are shown in.
type Options struct {
	Width int
	Color bool
	ASCII bool
	Mode  Mode
	Loc   *time.Location
}

const (
	minWidth     = 80
	maxWidth     = 160
	wideFrom     = 100
	silentDevice = 24 * time.Hour
	topProjects  = 3
	foldDevices  = 12
)

type ui struct {
	w          int  // layout width: the terminal width minus one
	page       int  // where the header, rules and legend end: w, or less on one device
	wide       bool // terminal width 100 or more
	multi      bool // the team view holds more than this device
	allDevices bool
	g          glyphs
	color      bool
	loc        *time.Location
	now        time.Time
	home       string
	legend     map[string]bool
	lines      []line
	r          *Report
}

func newUI(r *Report, o Options) *ui {
	t := o.Width
	if t == 0 {
		t = minWidth
	}
	t = clamp(t, minWidth, maxWidth)
	u := &ui{w: t - 1, wide: t >= wideFrom, g: utf8Glyphs, color: o.Color, loc: o.Loc,
		now: r.GeneratedAt, legend: map[string]bool{}, r: r, multi: len(r.Team.Devices) > 1}
	if o.ASCII {
		u.g = asciiGlyphs
	}
	if u.loc == nil {
		u.loc = time.Local
	}
	u.home = homeOf(r)
	u.page = u.w
	if !u.multi {
		// One device has no USED BY or NOTE column to fill a wide
		// terminal, so the page ends where its widest table does.
		u.page = min(u.w, maxLocalLabel+u.localNums())
	}
	return u
}

// Text renders the report for a person.
func Text(r Report, o Options) string {
	u := newUI(&r, o)
	switch o.Mode {
	case Tokens:
		u.header()
		u.blank()
		u.teamTokens()
	case Projects:
		u.header()
		u.blank()
		u.thisDevice(true)
		u.footer(false)
	case Devices:
		u.allDevices = true
		u.header()
		u.blank()
		u.devices()
		u.footer(false)
	default:
		u.header()
		u.blank()
		u.accounts()
		if u.multi {
			u.blank()
			u.devices()
		}
		u.blank()
		u.thisDevice(false)
		u.footer(true)
	}
	return u.String()
}

// defaultHomes are the harness homes that sit directly in the user's home.
var defaultHomes = []string{".claude", ".codex", ".grok", ".hermes"}

// homeOf is the user's home folder, taken from a default harness home, so
// paths print with ~ without the renderer asking the OS.
func homeOf(r *Report) string {
	for _, p := range r.Providers {
		for _, h := range p.Homes {
			for _, d := range defaultHomes {
				for _, sep := range []string{"/", `\`} {
					if strings.HasSuffix(h, sep+d) {
						return strings.TrimSuffix(h, sep+d)
					}
				}
			}
		}
	}
	return ""
}

func (u *ui) emit(l line) { u.lines = append(u.lines, l) }
func (u *ui) blank()      { u.emit(nil) }

func (u *ui) String() string {
	var b strings.Builder
	for _, l := range u.lines {
		for _, s := range trimRight(l) {
			if u.color && s.st != plain && strings.TrimSpace(s.text) != "" {
				b.WriteString("\x1b[" + string(s.st) + "m" + s.text + "\x1b[0m")
			} else {
				b.WriteString(s.text)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func trimRight(l line) line {
	out := append(line(nil), l...)
	for len(out) > 0 {
		last := &out[len(out)-1]
		last.text = strings.TrimRight(last.text, " ")
		if last.text != "" {
			break
		}
		out = out[:len(out)-1]
	}
	return out
}

// path prints a path under the user's home with ~.
func (u *ui) path(p string) string {
	if u.home == "" {
		return p
	}
	if p == u.home {
		return "~"
	}
	for _, sep := range []string{"/", `\`} {
		if strings.HasPrefix(p, u.home+sep) {
			return "~" + strings.TrimPrefix(p, u.home)
		}
	}
	return p
}

var managedHome = regexp.MustCompile(`[/\\](orca)[/\\]codex-accounts[/\\]([^/\\]+)[/\\]home$`)

// homeName is how a harness home is printed: an app's per-account home by
// the app and the account id, anything else as a path. An id that is a UUID
// is known by its first 8 hex digits.
func (u *ui) homeName(p string) string {
	if m := managedHome.FindStringSubmatch(p); m != nil {
		id := m[2]
		if uuidRe.MatchString(id) {
			id = id[:8]
		}
		return m[1] + " " + id
	}
	return u.path(p)
}

func (u *ui) clock(t time.Time, layout string) string { return t.In(u.loc).Format(layout) }

// spread puts right at the page's right edge after left, when both fit.
func (u *ui) spread(left, right line) line {
	left = left.cut(u.w, u.g.ell)
	// A left side too long for a one-device page, such as a long host
	// name, runs past the page rather than lose the right side.
	gap := max(u.page-left.width()-right.width(), 2)
	if left.width()+gap+right.width() > u.w {
		return left
	}
	return append(append(left, seg{strings.Repeat(" ", gap), plain}), right...)
}

// flow joins items with sep and wraps them at the page width, indenting
// the continuation lines.
func (u *ui) flow(first line, items []line, sep string, indent int) {
	for _, l := range u.wrap(first, items, sep, indent, u.page) {
		u.emit(l)
	}
}

// flowEven is flow with lines of even length: it wraps at the narrowest
// width that needs no more lines, so the last line is not a lone item.
func (u *ui) flowEven(items []line, sep string) {
	n := len(u.wrap(nil, items, sep, 0, u.page))
	lim := 0
	for _, it := range items {
		lim = max(lim, it.width())
	}
	for lim < u.page && len(u.wrap(nil, items, sep, 0, lim)) > n {
		lim++
	}
	for _, l := range u.wrap(nil, items, sep, 0, lim) {
		u.emit(l)
	}
}

func (u *ui) wrap(first line, items []line, sep string, indent, limit int) []line {
	var out []line
	cur := first
	fresh := true
	for _, it := range items {
		add := it.width()
		if !fresh {
			add += width(sep)
		}
		if cur.width()+add > limit && !fresh {
			out = append(out, cur.cut(u.w, u.g.ell))
			cur = line{{strings.Repeat(" ", indent), plain}}
			fresh = true
		}
		if !fresh {
			cur = append(cur, seg{sep, plain})
		}
		cur = append(cur, it...)
		fresh = false
	}
	return append(out, cur.cut(u.w, u.g.ell))
}

// titleWrap emits a section title and its counts, wrapping before a count
// that does not fit, under the first one.
func (u *ui) titleWrap(title line, indent int) {
	cur := append(line(nil), title[:2]...)
	for i := 2; i+1 < len(title); i += 2 {
		if cur.width()+width(title[i].text)+width(title[i+1].text) > u.w {
			u.emit(cur)
			cur = line{{strings.Repeat(" ", indent), plain}, title[i+1]}
			continue
		}
		cur = append(cur, title[i], title[i+1])
	}
	u.emit(cur.cut(u.w, u.g.ell))
}

// health is one item of the header strip. short names it in the status
// verdict; detail is a full error or a fix for a line of its own.
type health struct {
	glyph, text string
	st          style
	detail      string
	short       string
}

func (u *ui) header() {
	c := u.r.Collector
	g := u.g
	left := line{{"ai-usage", bold}, {" " + c.Version + g.sep + c.DeviceLabel + " (" + c.OSUser + ")" + g.sep + "team " + shortFP(c.Team, 8, g.ell), plain}}
	u.emit(u.spread(left, line{{u.clock(u.now, "Mon 2 Jan 15:04"), gray}}))

	var items []line
	var details []health
	for _, h := range u.healthItems() {
		items = append(items, line{{h.glyph + " ", h.st}, {h.text, plain}})
		if h.detail != "" {
			details = append(details, h)
		}
	}
	u.flow(nil, items, "  ", 0)
	for _, d := range details {
		u.emit(line{{"  " + d.glyph + " ", d.st}, {truncEnd(d.detail, u.w-4, g.ell), d.st}})
	}
}

// failed is whether the last run ended in an error: the error is newer than
// the last success.
func failed(c Collector) bool {
	return c.LastError != nil && c.LastErrorAt != nil && (c.LastSuccessAt == nil || c.LastErrorAt.After(*c.LastSuccessAt))
}

func (u *ui) healthItems() []health {
	c := u.r.Collector
	g := u.g
	dev := selfupdate.Dev(c.Version)
	var out []health
	since := func(t *time.Time) string {
		if t == nil {
			return "never"
		}
		return ago(u.now.Sub(*t))
	}
	switch {
	case c.LastRunAt == nil:
		out = append(out, health{g.fail, "never collected", red, "", "never collected"})
	case failed(c):
		out = append(out, health{g.fail, "last run failed " + since(c.LastErrorAt) + g.sep + "last success " + since(c.LastSuccessAt), red, "error: " + *c.LastError, "last run failed"})
	default:
		out = append(out, health{g.ok, "collected " + since(c.LastSuccessAt), green, "", ""})
	}
	rl := c.Relay
	switch {
	case rl.URL == nil:
		out = append(out, health{g.skip, "no relay", gray, "", ""})
	case rl.LastError != nil:
		out = append(out, health{g.fail, "relay failing" + g.sep + "last push " + since(rl.LastPushAt), red, "relay: " + *rl.LastError, "relay failing"})
	case rl.Pending:
		out = append(out, health{g.warn, "relay pending" + g.sep + "last push " + since(rl.LastPushAt), yellow, "", "relay pending"})
	default:
		out = append(out, health{g.ok, "relay synced " + since(rl.LastPushAt), green, "", ""})
	}
	switch {
	case c.Schedule.Registered:
		out = append(out, health{g.ok, "scheduled", green, "", ""})
	case c.Schedule.Error != nil:
		out = append(out, health{g.fail, "not scheduled", red, "schedule: " + *c.Schedule.Error, "not scheduled"})
	case dev:
		out = append(out, health{g.fail, "not scheduled", red, "schedule: dev builds do not register themselves" + g.sep + "ai-usage schedule install", "not scheduled"})
	default:
		out = append(out, health{g.fail, "not scheduled", red, "schedule: not registered" + g.sep + "ai-usage schedule install", "not scheduled"})
	}
	up := c.Update
	switch {
	case dev:
		out = append(out, health{g.skip, "dev build, no self-update", gray, "", ""})
	case up.Staged != nil:
		out = append(out, health{g.staged, *up.Staged + " runs next time", cyan, "", ""})
	case up.Error != nil:
		out = append(out, health{g.warn, "update check failed", yellow, "update: " + *up.Error, "update check failed"})
	case up.CheckedAt == nil:
		out = append(out, health{g.skip, "update not checked yet", gray, "", ""})
	case up.Latest != nil && selfupdate.Newer(*up.Latest, c.Version):
		out = append(out, health{g.warn, *up.Latest + " available", yellow, "", ""})
	default:
		out = append(out, health{g.ok, "up to date", green, "", ""})
	}
	return out
}

func (u *ui) footer(more bool) {
	g := u.g
	entries := []struct{ key, text string }{
		{"here", g.here + " logged in here"},
		{"seen", g.seen + " used here before"},
		{"crit", g.crit + " " + g.ge + "90%"},
		{"warn", g.warnMark + " " + g.ge + "75%"},
		{"pace", g.pace + " fills before reset"},
		{"old", g.stale + " old: " + u.oldLegend()},
		{"reset", g.question + " reset since reading"},
		{"dash", g.dash + " no window"},
		{"outdated", g.old + " outdated"},
	}
	u.legend["old"] = u.legend["oldReading"] || u.legend["oldDevice"]
	var items []line
	for _, e := range entries {
		if u.legend[e.key] {
			items = append(items, line{{e.text, gray}})
		}
	}
	grid := u.legend["grid"]
	if len(items) == 0 && !grid && !more {
		return
	}
	u.blank()
	if len(items) > 0 {
		u.flowEven(items, "  ")
	}
	// The grid key has glyphs of its own, so it keeps a line to itself.
	if grid {
		u.emit(line{{"cl cx gk hm = claude codex grok hermes: " + g.ok + " ok " + g.partial + " partial " + g.fail + " error " + g.skip + " not installed", gray}}.cut(u.page, g.ell))
	}
	if more {
		flags := "--projects  "
		if u.multi {
			flags += "--tokens  --devices  "
		}
		u.emit(line{{"more: ai-usage " + flags + "--json" + g.sep + "ai-usage status", gray}})
	}
}

// oldLegend explains ~ for what it marks on screen: an old reading, a
// silent device, or both.
func (u *ui) oldLegend() string {
	var parts []string
	if u.legend["oldReading"] {
		parts = append(parts, "reading 6h+")
	}
	if u.legend["oldDevice"] {
		parts = append(parts, "device 1d+")
	}
	return strings.Join(parts, ", ")
}

// provider is this device's source for p.
func (u *ui) provider(p string) *Provider {
	for i := range u.r.Providers {
		if u.r.Providers[i].Provider == p {
			return &u.r.Providers[i]
		}
	}
	return nil
}
