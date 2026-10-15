package view

import (
	"image/color"
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// guideCmd is the width of the guide's command column.
const guideCmd = 26

// Guide is the few lines a new device prints once, under the first report a
// person sees: how it collects from now on, what the team sees, and the
// commands worth knowing. scheduler names the system scheduler, such as cron.
// app says the guide offers the macOS menu bar app, which is not installed.
func Guide(r Report, scheduler string, app bool, o Options) string {
	c := newCard(&r, o)
	g := c.g
	col := r.Collector
	c.emit(chunks{c.ink("HOW IT WORKS", nil, true), c.ink("  shown once"+g.sep+"every command: ai-usage help", lipgloss.BrightBlack, false)})
	if col.Schedule.Foreground {
		scheduler = "`ai-usage schedule run`"
	}
	if col.Schedule.Registered {
		c.para(chunks{c.ink(g.ok+" ", lipgloss.Green, false)}, "collects by itself every 15 minutes, started by "+scheduler, nil)
	} else {
		c.para(chunks{c.ink(g.fail+" ", lipgloss.Red, false)}, scheduleFix(col, g.sep), lipgloss.Red)
	}
	team := []string{"no relay, so no team sees this device yet: ai-usage relay set URL"}
	if col.Relay.URL != nil {
		// Sealing hides these from the relay, not from the team.
		team = []string{
			"the team sees this device as " + col.DeviceLabel + ", with accounts, emails, and projects",
			"the relay sees tools, plans, and counts; the team key seals names and paths",
		}
	}
	for _, s := range team {
		c.para(chunks{c.plain("  ")}, s, nil)
	}
	c.para(chunks{c.plain("  ")}, weightsNote(o.ASCII), lipgloss.BrightBlack)
	for _, s := range guideNotes(r) {
		c.para(chunks{c.plain("  ")}, s, nil)
	}
	if r.Team.Estimated && r.Team.EstimatedReason != nil {
		c.para(chunks{c.plain("  ")}, g.silent+" team totals estimated: "+strings.ReplaceAll(*r.Team.EstimatedReason, "…", g.ell),
			lipgloss.BrightBlack)
	}
	if s := transcriptNote(r); s != "" {
		c.emit(chunks{c.plain("  "), c.ink(truncEnd(s, c.w-2, g.ell), lipgloss.BrightBlack, false)})
	}
	cmds := [][2]string{
		{"ai-usage", "collect now and show this report"},
		{"ai-usage status", "the collector's health, with full errors"},
		{"ai-usage name set NAME", "rename this device for the team"},
		{"ai-usage team key", "invite a colleague: they run ai-usage team join and paste it, or install with it as AI_USAGE_TEAM_KEY; share it like a password"},
	}
	if app {
		cmds = append(cmds, [2]string{"ai-usage app install", "add the menu bar app, which shows this report"})
	}
	// Only a system scheduler's entry is paused this way. `schedule run`
	// keeps collecting until it is stopped, and where nothing is registered
	// nothing collects by itself.
	if col.Schedule.Registered && !col.Schedule.Foreground {
		cmds = append(cmds, [2]string{"ai-usage schedule remove", "pause collecting; ai-usage schedule install resumes"})
	}
	for _, cmd := range cmds {
		c.para(chunks{c.plain("  " + padRight(cmd[0], guideCmd))}, cmd[1], lipgloss.BrightBlack)
	}
	c.emit(chunks{c.ink("  uninstall: https://github.com/"+selfupdate.Repo+"#uninstall", lipgloss.BrightBlack, false)})
	return c.String()
}

// weightsNote says what a weighted token is.
func weightsNote(ascii bool) string {
	to := "–"
	if ascii {
		to = "-"
	}
	return "weighted tokens: input 1, output 5 (codex 8), cache write 1" + to + "2, cache read 0.1" + to + "0.25, " +
		"from API prices; a proxy for quota, not its formula"
}

// guideNotes are what the guide says of r's data: that the team's tables
// count other days than this device's own, that the ledger holds less
// than 90 days of Claude, and how to name a Grok account known by its id.
func guideNotes(r Report) []string {
	var out []string
	switch {
	case len(r.Team.Devices) > 1 && r.Team.TodayLocal:
		out = append(out, "USAGE and PROJECTS count your local days and rolling 7/30/90 days; "+
			"team tables count today from your local midnight, and 7/30/90 days as UTC days, as devices send them")
	case len(r.Team.Devices) > 1:
		out = append(out, "USAGE and PROJECTS count your local days and rolling 7/30/90 days; "+
			"team tables count UTC days, as devices send them")
	}
	for _, pv := range r.Providers {
		switch {
		case pv.Provider == "claude" && pv.CoveredSince != nil && r.GeneratedAt.Sub(*pv.CoveredSince) < coverageFull:
			out = append(out, `Claude Code deletes transcripts after 30 days: set "cleanupPeriodDays": 90 in `+
				`~/.claude/settings.json to keep 90D whole`)
		case pv.Provider == "grok":
			for _, a := range pv.Accounts {
				if hexName.MatchString(a.Name) {
					out = append(out, "name grok "+a.Name+" for the team: ai-usage alias grok:"+a.Name+" NAME")
				}
			}
		}
	}
	return out
}

// hexName is an account's name that is the start of its id.
var hexName = regexp.MustCompile(`^[0-9a-f]{8}$`)

// transcriptNote says where this device's Claude transcripts miss tokens,
// when they do.
func transcriptNote(r Report) string {
	var nulls, over int
	for _, pv := range r.Providers {
		for _, a := range pv.Accounts {
			if pv.Provider == "claude" && a.TranscriptGaps != nil {
				nulls += a.TranscriptGaps.NullSnapshots
				over += a.TranscriptGaps.OverTracked
			}
		}
	}
	var parts []string
	if nulls > 0 {
		parts = append(parts, plural(nulls, "reply ends without a final token count", "replies end without a final token count"))
	}
	if over > 0 {
		parts = append(parts, plural(over, "session reports over twice its transcript's output",
			"sessions report over twice their transcript's output"))
	}
	switch len(parts) {
	case 0:
		return ""
	case 2:
		parts = []string{parts[0] + " and " + parts[1]}
	}
	return "claude: " + parts[0] + "; ai-usage uses Claude Code's own count where it wrote one"
}

// plural is n followed by one where n is 1, else by many.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// scheduleFix says why nothing is registered, in the same words as status:
// the scheduler's error, or the reason and the command that registers.
func scheduleFix(c Collector, sep string) string {
	switch {
	case c.Schedule.Error != nil:
		return "schedule: " + *c.Schedule.Error
	case selfupdate.Dev(c.Version):
		return "schedule: dev builds do not register themselves" + sep + "ai-usage schedule install"
	default:
		return "schedule: not registered" + sep + "ai-usage schedule install"
	}
}

// para prints text after lead, wrapping it at spaces under itself.
func (c *card) para(lead chunks, text string, ink color.Color) {
	c.hang(lead, wrapWords(text, c.w-lead.width()), ink)
}
