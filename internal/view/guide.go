package view

import (
	"strings"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// guideCmd is the width of the guide's command column.
const guideCmd = 26

// Guide is the few lines a new device prints once, under the first report a
// person sees: how it collects from now on, what the team sees, and the
// commands worth knowing. scheduler names the system scheduler, such as cron.
func Guide(r Report, scheduler string, o Options) string {
	u := newUI(&r, o)
	g := u.g
	c := r.Collector
	u.emit(line{{"HOW IT WORKS", bold}, {"  shown once" + g.sep + "every command: ai-usage help", gray}})
	if c.Schedule.Foreground {
		scheduler = "`ai-usage schedule run`"
	}
	if c.Schedule.Registered {
		u.para(line{{g.ok + " ", green}}, "collects by itself every 15 minutes, started by "+scheduler, plain)
	} else {
		// The same words as the header's, which say what to do.
		h := u.scheduleHealth()
		u.para(line{{h.glyph + " ", h.st}}, h.detail, h.st)
	}
	team := []string{"no relay, so no team sees this device yet: ai-usage relay set URL"}
	if c.Relay.URL != nil {
		// Sealing hides these from the relay, not from the team.
		team = []string{
			"the team sees this device as " + c.DeviceLabel + ", with accounts, emails, and projects",
			"the relay sees only numbers: names and paths are sealed with the team key",
		}
	}
	for _, s := range team {
		u.para(line{{"  ", plain}}, s, plain)
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
	if c.Schedule.Registered && !c.Schedule.Foreground {
		cmds = append(cmds, [2]string{"ai-usage schedule remove", "pause collecting; ai-usage schedule install resumes"})
	}
	for _, cmd := range cmds {
		u.para(line{{"  " + padRight(cmd[0], guideCmd), plain}}, cmd[1], gray)
	}
	u.emit(line{{"  uninstall: https://github.com/" + selfupdate.Repo + "#uninstall", gray}})
	return u.String()
}

// para prints text after lead, wrapping it at spaces under itself.
func (u *ui) para(lead line, text string, st style) {
	pad := line{{strings.Repeat(" ", lead.width()), plain}}
	for i, part := range wrapWords(text, u.w-lead.width()) {
		if i > 0 {
			lead = pad
		}
		u.emit(append(lead, seg{part, st}))
	}
}
