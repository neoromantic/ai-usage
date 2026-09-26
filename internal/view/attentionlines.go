package view

import (
	"image/color"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// badges are the ATTENTION words and their colors.
func (p *page) badgeOf(kind string) (string, color.Color) {
	t := p.th
	switch kind {
	case AttentionOut:
		return "OUT", t.Out
	case AttentionOver:
		return "OVER", t.Over
	case AttentionError:
		return "ERROR", t.Out
	case AttentionSilent:
		return "SILENT", t.Tight
	case AttentionOld:
		return "OLD", t.Muted
	case AttentionUnder:
		return "UNDER", t.Under
	}
	return strings.ToUpper(kind), t.Muted
}

const (
	// attentionLines is how many ATTENTION lines the static page shows.
	attentionLines = 6
	// attentionSubject caps the subject column, so the text keeps its room.
	attentionSubject = 30
)

// attention is what needs attention now, one line each: a badge, what it
// is about, and what to know. The static page shows the first few.
func (p *page) attention() []chunks {
	as := p.r.Attention
	if len(as) == 0 {
		return nil
	}
	more := 0
	if !p.o.Interactive && len(as) > attentionLines {
		as, more = as[:attentionLines], len(as)-attentionLines
	}
	badgeW, subjW := 0, 0
	subjects := make([]chunks, len(as))
	for i, a := range as {
		word, _ := p.badgeOf(a.Kind)
		badgeW = max(badgeW, width(word)+2)
		subjects[i] = p.subject(a)
		subjW = max(subjW, subjects[i].width())
	}
	subjW = min(subjW, attentionSubject, p.w/3)
	out := []chunks{{p.bold("ATTENTION")}}
	for i, a := range as {
		word, c := p.badgeOf(a.Kind)
		line := chunks{p.badge(" "+padRight(word, badgeW-1), c), p.plain(" ")}
		line = append(line, subjects[i].cut(subjW, p.g.ell).padTo(subjW)...)
		line = append(line, p.space(2))
		room := p.w - line.width()
		forms := p.attentionText(a, room)
		text := forms[len(forms)-1]
		for _, f := range forms {
			if f.width() <= room {
				text = f
				break
			}
		}
		out = append(out, append(line, text.cut(room, p.g.ell)...))
	}
	if more > 0 {
		out = append(out, chunks{p.space(1), p.muted("+" + strconv.Itoa(more) + " more")})
	}
	return out
}

// subject is what an ATTENTION line is about: an account and its window, a
// device, or the devices on an old release.
func (p *page) subject(a Attention) chunks {
	switch a.Kind {
	case AttentionOut, AttentionOver, AttentionUnder:
		label := shortID(a.Account)
		if acct := p.account(a.Provider, a.Account); acct != nil {
			label = shownLabel(acct)
		}
		if a.Window != "" && a.Name != "" {
			// The short name leaves room for the window.
			label = a.Name
		}
		out := chunks{p.muted(a.Provider + " "), p.plain(p.txt(label))}
		if a.Window != "" {
			out = append(out, p.muted(p.g.sep+p.txt(p.windowName(a.Provider, a.Account, a.Window))))
		}
		return out
	case AttentionOld:
		n := len(a.Devices)
		word := "devices"
		if n == 1 {
			word = "device"
		}
		// "12 devices on old releases" fits the subject's cap at 80 columns.
		on := "old releases"
		if vs := p.versions(a.Devices); len(vs) == 1 {
			on = p.txt(vs[0])
		}
		return chunks{p.plain(strconv.Itoa(n) + " " + word + " on " + on)}
	default:
		if len(a.Devices) == 0 {
			return nil
		}
		return chunks{p.plain(p.txt(a.Devices[0]))}
	}
}

// versions are the releases the named devices run, sorted.
func (p *page) versions(devices []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range devices {
		if d := p.device("", name); d != nil && !seen[d.CollectorVersion] {
			seen[d.CollectorVersion] = true
			out = append(out, d.CollectorVersion)
		}
	}
	sort.Strings(out)
	return out
}

// attentionText is what to know, from the fullest form to the shortest;
// the line takes the first that fits in room.
func (p *page) attentionText(a Attention, room int) []chunks {
	old := ""
	if a.ReadingAge > 0 {
		old = p.g.sep + "reading " + age(time.Duration(a.ReadingAge)*time.Second) + " old"
	}
	switch a.Kind {
	case AttentionOut:
		if a.At == nil {
			return p.forms("until its reset, at a time not known" + old)
		}
		s := "back " + p.clock(*a.At) + ", in " + dur(a.At.Sub(p.now))
		return p.forms(s+old, s)
	case AttentionOver:
		return p.overForms(a, old)
	case AttentionUnder:
		return p.underForms(a)
	case AttentionError:
		return p.forms(p.txt(a.Message))
	case AttentionSilent:
		return p.silentForms(a, room)
	case AttentionOld:
		return p.oldForms(a, room)
	}
	return p.forms(p.txt(a.Message))
}

// forms is an ATTENTION text's forms, one plain line each.
func (p *page) forms(parts ...string) []chunks {
	var out []chunks
	for _, s := range parts {
		out = append(out, chunks{p.plain(s)})
	}
	return out
}

// overForms is what to know about a window forecast to run out before its
// reset; old is how old its reading is, or empty.
func (p *page) overForms(a Attention, old string) []chunks {
	pace := " at " + p.pace(a) + " pace"
	// At a forecast of exactly 100% the window runs out at its reset,
	// and has no time of its own to run out; one that runs out less
	// than a minute before the reset runs out at it too.
	at, before := a.At, ""
	switch {
	case a.ResetsAt == nil:
	case at == nil || a.ResetsAt.Sub(*at) < time.Minute:
		at, before = a.ResetsAt, ", at its reset"
	default:
		before = ", " + dur(a.ResetsAt.Sub(*at)) + " before reset"
	}
	if at == nil {
		s := "runs out at its reset"
		return p.forms(s+pace+old, s+old, s)
	}
	verb := "runs out ~"
	if !at.After(p.now) {
		verb = "ran out ~"
	}
	s := verb + p.clock(*at)
	// A short line drops the pace first, then the reading's age: when it
	// runs out and how long before the reset are what the line says.
	return p.forms(s+pace+before+old, s+before+old, s+before, s+old, s)
}

// underForms is what to know about a window forecast to leave some of it
// unused at its reset.
func (p *page) underForms(a Attention) []chunks {
	unused := "some of it"
	if a.Percent != nil {
		unused = "~" + strconv.Itoa(max(100-int(*a.Percent), 0)) + "%"
	}
	s := "leaves " + unused + " unused at " + p.pace(a) + " pace"
	if a.ResetsAt == nil {
		return p.forms(s, "leaves "+unused+" unused")
	}
	return p.forms(s+", resets "+p.clock(*a.ResetsAt)+", in "+dur(a.ResetsAt.Sub(p.now)), s, "leaves "+unused+" unused")
}

// silentForms is what to know about a device that has not reported.
func (p *page) silentForms(a Attention, room int) []chunks {
	g := p.g
	s, long := "no report for a day", "no report for a day"
	if a.At != nil {
		s = "no report since " + p.clock(*a.At)
		long = s + ", " + dur(p.now.Sub(*a.At)) + " ago"
	}
	if a.Message == "" {
		return p.forms(long, s)
	}
	// The error it last reported comes after. A short line drops how
	// long ago first, then cuts the error, then drops it.
	last := g.sep + "last error: "
	msg := p.txt(a.Message)
	out := p.forms(long+last+msg, s+last+msg)
	if left := room - width(s+last); left >= 12 {
		out = append(out, p.forms(s+last+truncEnd(msg, left, g.ell))...)
	}
	return append(out, p.forms(long, s)...)
}

// oldForms is what to know about the devices on an old release.
func (p *page) oldForms(a Attention, room int) []chunks {
	g := p.g
	latest := ""
	if a.Message != "" {
		latest = g.sep + "latest " + p.txt(a.Message)
	}
	var names []string
	for _, d := range a.Devices {
		names = append(names, p.txt(d))
	}
	// The names give way to a count, so the latest release keeps its
	// place, and so does how long the device longest on an old release
	// has not updated, once that is longer than updating takes, until
	// even the count leaves no room for it.
	list := func(tail string) string { return nameList(names, max(room-width(tail), 12), g.ell) + tail }
	if a.At != nil && p.now.Sub(*a.At) >= BehindAfter {
		up := ""
		if len(a.Devices) > 1 {
			up = "up to "
		}
		return p.forms(list(latest+g.sep+"not updated for "+up+age(p.now.Sub(*a.At))), list(latest))
	}
	return p.forms(list(latest))
}

// pace names the window an over or under line forecasts: this week's, or
// this window's when it is not a week long.
func (p *page) pace(a Attention) string {
	w := p.window(a.Provider, a.Account, a.Window)
	if w == nil || windowLength(*w) == week {
		return "this week's"
	}
	return "this window's"
}

// window is the named window of an account, or its main window when name
// is empty; nil when the report does not hold it.
func (p *page) window(provider, label, name string) *Window {
	acct := p.account(provider, label)
	if acct == nil || acct.Quota == nil {
		return nil
	}
	if name == "" {
		return mainWindow(acct.Quota.Windows)
	}
	for i := range acct.Quota.Windows {
		if acct.Quota.Windows[i].Name == name {
			return &acct.Quota.Windows[i]
		}
	}
	return nil
}

// windowName is how a window other than the main one is named on the page:
// a model window as its model, "Fable" for "7d Fable" beside a weekly main
// window; any other by its name, such as "5h".
func (p *page) windowName(provider, label, name string) string {
	mainName := "7d"
	if acct := p.account(provider, label); acct != nil && acct.Quota != nil {
		if m := mainWindow(acct.Quota.Windows); m != nil {
			mainName = m.Name
		}
	}
	return trimLength(name, mainName)
}

// windowLength is a window's length, from the harness or its name.
func windowLength(w Window) time.Duration {
	return snapshot.Window{Name: w.Name, Minutes: w.Minutes}.Length()
}
