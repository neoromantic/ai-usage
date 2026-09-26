package view

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// guideCmd is the width of the guide's command column.
const guideCmd = 26

// Guide is the few lines a new device prints once, under the first report a
// person sees: how it collects from now on, what the team sees, and the
// commands worth knowing. scheduler names the system scheduler, such as cron.
func Guide(r Report, scheduler string, o Options) string {
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
	cmds := [][2]string{
		{"ai-usage", "collect now and show this report"},
		{"ai-usage status", "the collector's health, with full errors"},
		{"ai-usage name set NAME", "rename this device for the team"},
		{"ai-usage team key", "invite a colleague: they install with this key as AI_USAGE_TEAM_KEY; share it like a password"},
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
