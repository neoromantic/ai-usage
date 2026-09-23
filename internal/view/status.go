package view

import (
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

const statusKey = 14

// StatusText prints collector health as a key/value card. dir is the
// collector's directory. Nothing is cut: long values wrap under their
// column, since this is where full errors live.
func StatusText(r Report, dir string, o Options) string {
	u := newUI(&r, o)
	u.page = u.w
	u.status(dir)
	return u.String()
}

func (u *ui) status(dir string) {
	g := u.g
	c := u.r.Collector
	u.emit(u.spread(line{{"ai-usage status", bold}}, line{{u.clock(u.now, "Mon 2 Jan 15:04"), gray}}))

	var problems []line
	for _, h := range u.healthItems() {
		if h.short != "" {
			problems = append(problems, line{{h.short, plain}})
		}
	}
	for _, p := range u.r.Providers {
		if p.Status == "error" || p.Status == "partial" {
			problems = append(problems, line{{p.Provider + " " + p.Status, plain}})
		}
	}
	if len(problems) == 0 {
		u.emit(line{{g.ok + " ", green}, {"healthy", plain}})
	} else {
		word := "problems"
		if len(problems) == 1 {
			word = "problem"
		}
		u.flow(line{{g.fail + " ", red}, {strconv.Itoa(len(problems)) + " " + word + ": ", plain}}, problems, ", ", 2)
	}
	u.blank()

	ok, fail, warn, info := seg{g.ok, green}, seg{g.fail, red}, seg{g.warn, yellow}, seg{" ", plain}
	u.kv("version", info, c.Version, plain)
	u.kv("device", info, c.DeviceLabel+" ("+c.OSUser+")"+g.sep+c.Device, plain)
	team := c.Team
	if n := len(u.r.Team.Devices); n > 0 && u.r.Team.PulledAt != nil {
		team += g.sep + strconv.Itoa(n) + " devices"
	}
	u.kv("team", info, team, plain)
	if dir != "" {
		u.kvPath("directory", info, u.path(dir))
	}
	u.blank()

	switch {
	case c.LastRunAt == nil:
		u.kv("last run", fail, "never", red)
	case failed(c):
		u.kv("last run", fail, u.stamp(c.LastRunAt)+", failed", red)
	default:
		u.kv("last run", ok, u.stamp(c.LastRunAt), plain)
	}
	if c.LastSuccessAt == nil {
		u.kv("last success", fail, "never", red)
	} else {
		u.kv("last success", ok, u.stamp(c.LastSuccessAt), plain)
	}
	switch {
	case c.LastError == nil:
		u.kv("last error", info, "none", gray)
	case failed(c):
		u.kv("last error", fail, u.stamp(c.LastErrorAt)+": "+*c.LastError, red)
	default:
		u.kv("last error", info, u.stamp(c.LastErrorAt)+": "+*c.LastError, gray)
	}

	rl := c.Relay
	switch {
	case rl.URL == nil:
		u.kv("relay", seg{g.skip, gray}, "not configured; the team view shows this device only", plain)
		u.kv("", info, "set one: ai-usage relay set URL", gray)
	default:
		gl := ok
		if rl.LastError != nil {
			gl = fail
		} else if rl.Pending {
			gl = warn
		}
		u.kv("relay", gl, *rl.URL, plain)
		u.kv("", info, "pushed "+u.stamp(rl.LastPushAt)+g.sep+"pulled "+u.stamp(rl.LastPullAt), gray)
		if rl.Pending {
			u.kv("", info, "the newest snapshot is not sent yet", yellow)
		}
		if rl.LastError != nil {
			u.kv("", info, *rl.LastError, red)
		}
	}
	dev := selfupdate.Dev(c.Version)
	switch {
	case c.Schedule.Registered:
		u.kv("schedule", ok, "registered with the system scheduler, every 15 minutes", plain)
	case c.Schedule.Error != nil:
		u.kv("schedule", fail, "not registered: "+*c.Schedule.Error, red)
	case dev:
		u.kv("schedule", fail, "not registered; dev builds do not register themselves", red)
	default:
		u.kv("schedule", fail, "not registered", red)
	}
	// An error that already says how to register needs no second line.
	if !c.Schedule.Registered && (c.Schedule.Error == nil || !strings.Contains(*c.Schedule.Error, "ai-usage schedule install")) {
		u.kv("", info, "register: ai-usage schedule install", gray)
	}
	up := c.Update
	switch {
	case dev:
		u.kv("update", info, "dev build: no self-update", gray)
	case up.Staged != nil:
		u.kv("update", seg{g.staged, cyan}, *up.Staged+" is installed and runs next time", plain)
	case up.Error != nil:
		u.kv("update", warn, *up.Error, yellow)
	case up.Latest != nil && *up.Latest == c.Version:
		u.kv("update", ok, c.Version+" is the newest release", plain)
	default:
		latest := "unknown"
		if up.Latest != nil {
			latest = *up.Latest
		}
		u.kv("update", info, "newest release "+latest, plain)
	}
	if !dev {
		u.kv("", info, "checked "+u.stamp(up.CheckedAt), gray)
	}
	u.blank()
	u.sources()
}

func (u *ui) sources() {
	g := u.g
	for i, p := range u.r.Providers {
		key := ""
		if i == 0 {
			key = "sources"
		}
		gl, st := seg{g.ok, green}, plain
		switch p.Status {
		case "skipped":
			gl, st = seg{g.skip, gray}, gray
		case "partial":
			gl, st = seg{g.partial, yellow}, yellow
		case "error":
			gl, st = seg{g.fail, red}, red
		}
		val := padRight(p.Provider, 8)
		if p.Status == "skipped" {
			val += "not installed"
		} else {
			var hs []string
			for _, h := range p.Homes {
				hs = append(hs, u.homeName(h))
			}
			// A server can read dozens of homes; `ai-usage home` lists them.
			if len(hs) > shownHomes {
				hs = append(hs[:shownHomes-1:shownHomes-1], "+"+strconv.Itoa(len(hs)-shownHomes+1)+" more (ai-usage home)")
			}
			n := "no accounts yet"
			switch len(p.Accounts) {
			case 0:
			case 1:
				n = "1 account"
			default:
				n = strconv.Itoa(len(p.Accounts)) + " accounts"
			}
			if len(hs) > 0 {
				n = strings.Join(hs, ", ") + g.sep + n
			}
			val += n
		}
		u.kv(key, gl, val, st)
		if p.Error != nil {
			u.kvIndent(8, *p.Error, red)
		}
	}
}

// shownHomes is how many homes a source line names before it counts the rest.
const shownHomes = 3

// stamp is a time as a clock and how long ago: just the clock on the
// report's own day.
func (u *ui) stamp(t *time.Time) string {
	if t == nil {
		return "never"
	}
	layout := "15:04"
	if u.clock(u.now, "2006-01-02") != u.clock(*t, "2006-01-02") {
		layout = "2006-01-02 15:04"
	}
	return u.clock(*t, layout) + " (" + ago(u.now.Sub(*t)) + ")"
}

// kv prints "key  glyph value", wrapping value at spaces under itself.
func (u *ui) kv(key string, glyph seg, value string, st style) {
	lead := line{{padRight(key, statusKey), gray}, glyph, {" ", plain}}
	for i, part := range wrapWords(value, u.w-statusKey-2) {
		if i > 0 {
			lead = line{{strings.Repeat(" ", statusKey+2), plain}}
		}
		u.emit(append(lead, seg{part, st}))
	}
}

// kvIndent prints a sub-line indented under the value column.
func (u *ui) kvIndent(indent int, value string, st style) {
	pad := strings.Repeat(" ", statusKey+2+indent)
	for _, part := range wrapWords(value, u.w-width(pad)) {
		u.emit(line{{pad, plain}, {part, st}})
	}
}

// kvPath wraps a long path after a slash, so it can still be copied whole.
func (u *ui) kvPath(key string, glyph seg, p string) {
	room := u.w - statusKey - 2
	lead := line{{padRight(key, statusKey), gray}, glyph, {" ", plain}}
	for i, part := range wrapAfter(p, "/", room) {
		if i > 0 {
			lead = line{{strings.Repeat(" ", statusKey+2), plain}}
		}
		u.emit(append(lead, seg{part, plain}))
	}
}

// wrapWords wraps s at spaces within w columns. A word wider than that is
// broken after a slash, or else where it has to be, and nothing is lost.
func wrapWords(s string, w int) []string {
	var out []string
	cur := ""
	for _, word := range strings.Split(s, " ") {
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
