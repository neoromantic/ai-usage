package view

import (
	"image/color"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

const statusKey = 14

// StatusText prints collector health as a key/value card. dir is the
// collector's directory. Nothing is cut: long values wrap under their
// column, since this is where full errors live.
func StatusText(r Report, dir string, o Options) string {
	c := newCard(&r, o)
	c.status(dir)
	return c.String()
}

func (c *card) status(dir string) {
	g := c.g
	col := c.r.Collector
	// The clock ends at the card's right edge.
	title, clock := c.ink("ai-usage status", nil, true), c.ink(c.clockAt(c.now, "Mon 2 Jan 15:04"), lipgloss.BrightBlack, false)
	c.emit(chunks{title, c.space(c.w - width(title.text) - width(clock.text)), clock})

	items := problems(col)
	for _, p := range c.r.Providers {
		if p.Status == "error" || p.Status == "partial" {
			items = append(items, p.Provider+" "+p.Status)
		}
	}
	if len(items) == 0 {
		c.emit(chunks{c.ink(g.ok+" ", lipgloss.Green, false), c.plain("healthy")})
	} else {
		word := "problems"
		if len(items) == 1 {
			word = "problem"
		}
		// The problems wrap at the card's width, and the lines after the
		// first are indented.
		lead := chunks{c.ink(g.fail+" ", lipgloss.Red, false), c.plain(strconv.Itoa(len(items)) + " " + word + ": ")}
		for i, l := range wrapItems(items, ", ", c.w-lead.width(), c.w-2) {
			if i > 0 {
				lead = chunks{c.space(2)}
			}
			c.emit(append(lead, c.plain(l)).cut(c.w, g.ell))
		}
	}
	c.blank()

	ok, fail, warn, info := c.ink(g.ok, lipgloss.Green, false), c.ink(g.fail, lipgloss.Red, false), c.ink(g.warn, lipgloss.Yellow, false), c.plain(" ")
	c.kv("version", info, col.Version, nil)
	c.kv("device", info, col.DeviceLabel+" ("+col.OSUser+")"+g.sep+col.Device, nil)
	team := col.Team
	if n := len(c.r.Team.Devices); n > 0 && c.r.Team.PulledAt != nil {
		team += g.sep + strconv.Itoa(n) + " devices"
	}
	c.kv("team", info, team, nil)
	if dir != "" {
		// A long path wraps after a slash, so it can still be copied whole.
		c.hang(c.kvLead("directory", info), wrapAfter(c.path(dir), "/", c.w-statusKey-2), nil)
	}
	c.blank()

	switch {
	case col.LastRunAt == nil:
		c.kv("last run", fail, "never", lipgloss.Red)
	case failed(col):
		c.kv("last run", fail, c.stamp(col.LastRunAt)+", failed", lipgloss.Red)
	default:
		c.kv("last run", ok, c.stamp(col.LastRunAt), nil)
	}
	if col.LastSuccessAt == nil {
		c.kv("last success", fail, "never", lipgloss.Red)
	} else {
		c.kv("last success", ok, c.stamp(col.LastSuccessAt), nil)
	}
	switch {
	case col.LastError == nil:
		c.kv("last error", info, "none", lipgloss.BrightBlack)
	case failed(col):
		c.kv("last error", fail, c.stamp(col.LastErrorAt)+": "+*col.LastError, lipgloss.Red)
	default:
		c.kv("last error", info, c.stamp(col.LastErrorAt)+": "+*col.LastError, lipgloss.BrightBlack)
	}

	rl := col.Relay
	switch {
	case rl.URL == nil:
		c.kv("relay", c.ink(g.none, lipgloss.BrightBlack, false), "not configured; the team view shows this device only", nil)
		c.kv("", info, "set one: ai-usage relay set URL", lipgloss.BrightBlack)
	default:
		gl := ok
		if rl.LastError != nil {
			gl = fail
		} else if rl.Pending {
			gl = warn
		}
		c.kv("relay", gl, *rl.URL, nil)
		c.kv("", info, "pushed "+c.stamp(rl.LastPushAt)+g.sep+"pulled "+c.stamp(rl.LastPullAt), lipgloss.BrightBlack)
		if rl.Pending {
			c.kv("", info, "the newest snapshot is not sent yet", lipgloss.Yellow)
		}
		if rl.LastError != nil {
			c.kv("", info, *rl.LastError, lipgloss.Red)
		}
	}
	dev := selfupdate.Dev(col.Version)
	switch {
	case col.Schedule.Foreground:
		c.kv("schedule", ok, "every 15 minutes by `ai-usage schedule run`", nil)
	case col.Schedule.Registered:
		c.kv("schedule", ok, "registered with the system scheduler, every 15 minutes", nil)
	case col.Schedule.Error != nil:
		c.kv("schedule", fail, "not registered: "+*col.Schedule.Error, lipgloss.Red)
	case dev:
		c.kv("schedule", fail, "not registered; dev builds do not register themselves", lipgloss.Red)
	default:
		c.kv("schedule", fail, "not registered", lipgloss.Red)
	}
	// An error that already says what to run needs no second line.
	if !col.Schedule.Registered && (col.Schedule.Error == nil || !strings.Contains(*col.Schedule.Error, "ai-usage schedule install") && !strings.Contains(*col.Schedule.Error, "ai-usage schedule run")) {
		c.kv("", info, "register: ai-usage schedule install", lipgloss.BrightBlack)
	}
	up := col.Update
	switch {
	case dev:
		c.kv("update", info, "dev build: no self-update", lipgloss.BrightBlack)
	case up.Staged != nil:
		c.kv("update", c.ink(g.staged, lipgloss.Cyan, false), *up.Staged+" is installed and runs next time", nil)
	case up.Error != nil:
		c.kv("update", warn, *up.Error, lipgloss.Yellow)
	case up.Latest != nil && (*up.Latest == col.Version || selfupdate.Newer(col.Version, *up.Latest)):
		// A release installed by hand can be newer than the last check saw.
		c.kv("update", ok, col.Version+" is the newest release", nil)
	default:
		latest := "unknown"
		if up.Latest != nil {
			latest = *up.Latest
		}
		c.kv("update", info, "newest release "+latest, nil)
	}
	if !dev {
		c.kv("", info, "checked "+c.stamp(up.CheckedAt), lipgloss.BrightBlack)
	}
	c.blank()
	c.sources()
}

// problems names what is wrong with the collection, the relay, the schedule
// and the update, for the status card's verdict.
func problems(c Collector) []string {
	var out []string
	switch {
	case c.LastRunAt == nil:
		out = append(out, "never collected")
	case failed(c):
		out = append(out, "last run failed")
	}
	if c.Relay.URL != nil {
		switch {
		case c.Relay.LastError != nil:
			out = append(out, "relay failing")
		case c.Relay.Pending:
			out = append(out, "relay pending")
		}
	}
	if !c.Schedule.Registered {
		out = append(out, "not scheduled")
	}
	// An update error counts only where the update line shows it: not on a
	// dev build, and not with a release staged.
	if !selfupdate.Dev(c.Version) && c.Update.Staged == nil && c.Update.Error != nil {
		out = append(out, "update check failed")
	}
	return out
}

func (c *card) sources() {
	g := c.g
	for i, p := range c.r.Providers {
		key := ""
		if i == 0 {
			key = "sources"
		}
		var fg color.Color
		gl := c.ink(g.ok, lipgloss.Green, false)
		switch p.Status {
		case "skipped":
			gl, fg = c.ink(g.none, lipgloss.BrightBlack, false), lipgloss.BrightBlack
		case "partial":
			gl, fg = c.ink(g.partial, lipgloss.Yellow, false), lipgloss.Yellow
		case "error":
			gl, fg = c.ink(g.fail, lipgloss.Red, false), lipgloss.Red
		}
		val := padRight(p.Provider, 8)
		if p.Status == "skipped" {
			val += "not installed"
		} else {
			var hs []string
			for _, h := range p.Homes {
				hs = append(hs, c.homeName(h))
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
		c.kv(key, gl, val, fg)
		if p.Error != nil {
			// The error goes under the homes, past the provider's name.
			pad := statusKey + 2 + 8
			c.hang(chunks{c.space(pad)}, wrapWords(*p.Error, c.w-pad), lipgloss.Red)
		}
	}
}

// shownHomes is how many homes a source line names before it counts the rest.
const shownHomes = 3

// appHomes are the homes an app keeps per account or per session, and the
// app's name.
var appHomes = []struct {
	re  *regexp.Regexp
	app string
}{
	{regexp.MustCompile(`[/\\]orca[/\\]codex-accounts[/\\]([^/\\]+)[/\\]home$`), "orca"},
	{claudeAppHome, "claude app"},
}

// homeName is how a harness home is printed: an app's home by the app and
// the account or session id, anything else as a path. An id that is a UUID
// is known by its first 8 hex digits.
func (c *card) homeName(p string) string {
	for _, a := range appHomes {
		if m := a.re.FindStringSubmatch(p); m != nil {
			id := m[1]
			if uuidRe.MatchString(id) {
				id = id[:8]
			}
			return a.app + " " + id
		}
	}
	return c.path(p)
}

// stamp is a time as a clock and how long ago: just the clock on the
// report's own day.
func (c *card) stamp(t *time.Time) string {
	if t == nil {
		return "never"
	}
	layout := "15:04"
	if c.clockAt(c.now, "2006-01-02") != c.clockAt(*t, "2006-01-02") {
		layout = "2006-01-02 15:04"
	}
	return c.clockAt(*t, layout) + " (" + ago(c.now.Sub(*t)) + ")"
}

// kv prints "key  glyph value", wrapping value at spaces under itself.
func (c *card) kv(key string, glyph chunk, value string, ink color.Color) {
	c.hang(c.kvLead(key, glyph), wrapWords(value, c.w-statusKey-2), ink)
}

// kvLead is the key column and the glyph before a value.
func (c *card) kvLead(key string, glyph chunk) chunks {
	return chunks{c.ink(padRight(key, statusKey), lipgloss.BrightBlack, false), glyph, c.plain(" ")}
}
