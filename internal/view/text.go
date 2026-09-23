package view

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// ConsoleProjects is how many projects each account lists in the console.
const ConsoleProjects = 8

// Text renders the report for a person.
func Text(r Report) string {
	var b strings.Builder
	now := r.GeneratedAt
	c := r.Collector
	fmt.Fprintf(&b, "ai-usage %s · %s (%s) · team %s\n", c.Version, c.DeviceLabel, c.OSUser, shortFP(c.Team))
	fmt.Fprintf(&b, "last run %s · last success %s\n", ago(c.LastRunAt, now), ago(c.LastSuccessAt, now))
	b.WriteString(relayLine(c.Relay, now))
	if c.LastError != nil {
		fmt.Fprintf(&b, "last error (%s): %s\n", ago(c.LastErrorAt, now), *c.LastError)
	}
	if !c.Schedule.Registered {
		msg := "not registered with the system scheduler"
		if c.Schedule.Error != nil {
			msg += ": " + *c.Schedule.Error
		}
		fmt.Fprintf(&b, "schedule: %s\n", msg)
	}
	if c.Update.Staged != nil {
		fmt.Fprintf(&b, "update: %s is installed and runs next time\n", *c.Update.Staged)
	} else if c.Update.Error != nil {
		fmt.Fprintf(&b, "update: %s\n", *c.Update.Error)
	}

	for _, p := range r.Providers {
		b.WriteByte('\n')
		writeProvider(&b, p, now)
	}
	writeTeam(&b, r.Team, now)
	return b.String()
}

func relayLine(rl Relay, now time.Time) string {
	if rl.URL == nil {
		return "relay: not configured (set one with `ai-usage relay set <url>`)\n"
	}
	parts := []string{"pushed " + ago(rl.LastPushAt, now), "pulled " + ago(rl.LastPullAt, now)}
	if rl.Pending {
		parts = append(parts, "newest snapshot not sent yet")
	}
	line := "relay: " + strings.Join(parts, " · ") + "\n"
	if rl.LastError != nil {
		line += "relay error: " + *rl.LastError + "\n"
	}
	return line
}

func writeProvider(b *strings.Builder, p Provider, now time.Time) {
	fmt.Fprintf(b, "%s  %s\n", strings.ToUpper(p.Provider), p.Status)
	if p.Status == "skipped" {
		b.WriteString("  not installed\n")
		return
	}
	if p.Error != nil {
		fmt.Fprintf(b, "  problem: %s\n", *p.Error)
	}
	if len(p.Accounts) == 0 {
		b.WriteString("  no accounts and no usage yet\n")
	}
	for _, a := range p.Accounts {
		head := "  " + a.Label
		if a.Plan != nil {
			head += "  " + *a.Plan
		}
		if a.Current {
			head += "  (logged in)"
		}
		b.WriteString(head + "\n")
		writeQuota(b, "    ", a.Quota, a.HeadlinePercent, a.Level, now)
		fmt.Fprintf(b, "    90 days: %s · %s\n", plural(a.Sessions, "session"), tokens(a.Tokens))
		if a.LastActiveAt != nil {
			fmt.Fprintf(b, "    last active %s\n", ago(a.LastActiveAt, now))
		}
		for i, pr := range a.Projects {
			if i == ConsoleProjects {
				more := len(a.Projects) - ConsoleProjects
				word := "projects"
				if more == 1 {
					word = "project"
				}
				fmt.Fprintf(b, "      %d more %s\n", more, word)
				break
			}
			fmt.Fprintf(b, "      %s  %s · %s\n", pr.Path, plural(pr.Sessions, "session"), tokens(pr.Tokens))
		}
	}
}

func writeQuota(b *strings.Builder, indent string, q *Quota, head *float64, lvl string, now time.Time) {
	if q == nil || len(q.Windows) == 0 {
		b.WriteString(indent + "quota unknown\n")
		return
	}
	fmt.Fprintf(b, "%s%s\n", indent, quotaLine(q, head, lvl, now))
	for _, w := range q.Windows {
		line := fmt.Sprintf("%s  %-12s %5s%s", indent, w.Name, pct(w.Percent), mark(w.Level))
		if w.ResetsAt != nil {
			if w.ResetsAt.After(now) {
				line += "  resets in " + dur(w.ResetsAt.Sub(now))
			} else {
				line += "  reset " + dur(now.Sub(*w.ResetsAt)) + " ago"
			}
		}
		if w.Pace != nil && w.Pace.FillsAt != nil {
			if w.Pace.FillsAt.After(now) {
				line += "  · at this pace full in " + dur(w.Pace.FillsAt.Sub(now)) + ", before reset"
			} else {
				line += "  · at this pace already full"
			}
		}
		b.WriteString(line + "\n")
	}
}

func writeTeam(b *strings.Builder, t Team, now time.Time) {
	b.WriteString("\nTEAM")
	if t.PulledAt == nil {
		b.WriteString("  not read from the relay yet; this device only\n")
	} else {
		fmt.Fprintf(b, "  read %s\n", ago(t.PulledAt, now))
	}
	for _, d := range t.Devices {
		this := ""
		if d.This {
			this = "  (this device)"
		}
		health := []string{}
		for _, s := range d.Sources {
			if s.Status != "ok" && s.Status != "skipped" {
				health = append(health, s.Provider+" "+s.Status)
			}
		}
		h := "ok"
		if len(health) > 0 {
			h = strings.Join(health, ", ")
		}
		fmt.Fprintf(b, "  %s (%s)%s  collected %s · %s · %s\n", d.Label, d.OSUser, this, ago(&d.CollectedAt, now), d.CollectorVersion, h)
	}
	for _, p := range t.Providers {
		fmt.Fprintf(b, "  %s\n", p.Provider)
		for _, a := range p.Accounts {
			quota := "quota unknown"
			if a.Quota != nil && len(a.Quota.Windows) > 0 {
				quota = quotaLine(a.Quota, a.HeadlinePercent, a.Level, now)
			}
			fmt.Fprintf(b, "    %s  %s · %s on %s\n", a.Label, quota, tokens(a.Tokens), plural(len(a.Devices), "device"))
		}
	}
}

// quotaLine is the headline of a reading, with where it came from and its age.
func quotaLine(q *Quota, head *float64, lvl string, now time.Time) string {
	src := ago(&q.ObservedAt, now)
	if q.Device != "" {
		src = "from " + q.Device + ", " + src
	} else if q.Source != "" {
		src = q.Source + ", " + src
	}
	if q.Stale {
		src += " · stale"
	}
	if head == nil {
		return "quota unknown, every window reset since the reading (" + src + ")"
	}
	return fmt.Sprintf("quota %s%s (%s)", pct(*head), mark(lvl), src)
}

func mark(lvl string) string {
	switch lvl {
	case "critical":
		return " !!"
	case "warning":
		return " !"
	default:
		return ""
	}
}

func shortFP(fp string) string {
	if len(fp) <= 12 {
		return fp
	}
	return fp[:12] + "…"
}

func pct(p float64) string {
	if p == math.Trunc(p) {
		return strconv.FormatFloat(p, 'f', 0, 64) + "%"
	}
	return strconv.FormatFloat(p, 'f', 1, 64) + "%"
}

func tokens(t snapshot.Tokens) string {
	return fmt.Sprintf("in %s out %s cache read %s write %s", human(t.Input), human(t.Output), human(t.CacheRead), human(t.CacheWrite))
}

// human prints 1234567 as 1.2M.
func human(n int64) string {
	f := float64(n)
	for _, u := range []struct {
		v float64
		s string
	}{{1e12, "T"}, {1e9, "G"}, {1e6, "M"}, {1e3, "K"}} {
		if f >= u.v {
			x := f / u.v
			if x >= 100 {
				return strconv.FormatFloat(x, 'f', 0, 64) + u.s
			}
			return strconv.FormatFloat(x, 'f', 1, 64) + u.s
		}
	}
	return strconv.FormatInt(n, 10)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func ago(t *time.Time, now time.Time) string {
	if t == nil || t.IsZero() {
		return "never"
	}
	d := now.Sub(*t)
	if d < 0 {
		return "just now"
	}
	if d < time.Minute {
		return "just now"
	}
	return dur(d) + " ago"
}

// dur prints a duration in the two largest units: 3d 4h, 2h 5m, 12m.
func dur(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	m := int(d.Minutes())
	days, hours, mins := m/(60*24), (m/60)%24, m%60
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && mins > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}
