package view

import (
	"image/color"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// healthItem is one item on the header's right: a colored dot and a few
// words, in a long and a short form.
type healthItem struct {
	dot         color.Color
	long, short string
	busy        bool
}

// header is `ai-usage`, this device, and the team on the left, and the
// collector's health on the right, ending where the page does. A failure
// turns its dot red; the error itself is in ATTENTION.
func (p *page) header(pageWidth int) chunks {
	c := p.r.Collector
	g := p.g
	device := p.txt(c.DeviceLabel)
	left := func(dev string, team bool) chunks {
		out := chunks{p.bold("ai-usage"), p.faint(g.sep), p.plain(dev)}
		if team && c.Team != "" {
			out = append(out, p.faint(g.sep), p.muted("team "+p.txt(prefix(c.Team, 8))))
		}
		return out
	}
	right := func(short bool) chunks {
		var out chunks
		for i, h := range p.health() {
			if i > 0 {
				out = append(out, p.space(2))
			}
			text := h.long
			if short {
				text = h.short
			}
			if h.busy {
				out = append(out, p.accent(p.txt(p.o.Busy)), p.plain(" "), p.accent(text))
				continue
			}
			out = append(out, p.paint(g.here, h.dot, false, false), p.plain(" "), p.muted(text))
		}
		return out
	}
	// The health words shorten first, then the team goes, then the device
	// is cut.
	l, r := left(device, true), right(false)
	fits := func() bool { return l.width()+2+r.width() <= p.w }
	if !fits() {
		r = right(true)
	}
	if !fits() {
		l = left(device, false)
	}
	if over := l.width() + 2 + r.width() - p.w; over > 0 {
		l = left(truncEnd(device, max(width(device)-over, 6), g.ell), false)
	}
	edge := min(max(pageWidth, l.width()+2+r.width()), p.w)
	return append(l, append(chunks{p.space(max(edge-l.width()-r.width(), 2))}, r...)...).cut(p.w, g.ell)
}

// health is the collection, the relay, and the update, in the header.
func (p *page) health() []healthItem {
	c := p.r.Collector
	t := p.th
	// ago is "7m ago", or "just now"; short is "7m", or "now".
	short := func(at *time.Time) string {
		if at == nil {
			return "never"
		}
		return age(p.now.Sub(*at))
	}
	ago := func(at *time.Time) string {
		if s := short(at); s != "now" && s != "never" {
			return s + " ago"
		}
		if at == nil {
			return "never"
		}
		return "just now"
	}
	var out []healthItem
	switch {
	case p.o.Busy != "":
		out = append(out, healthItem{busy: true, long: "collecting", short: "collecting"})
	case c.LastRunAt == nil:
		out = append(out, healthItem{t.Out, "never collected", "never collected", false})
	case failed(c):
		out = append(out, healthItem{t.Out, "last run failed " + ago(c.LastErrorAt), "run failed", false})
	case !c.Schedule.Registered:
		out = append(out, healthItem{t.Tight, "collected " + ago(c.LastSuccessAt) + ", not scheduled", "not scheduled", false})
	default:
		out = append(out, healthItem{t.OK, "collected " + ago(c.LastSuccessAt), "collected " + short(c.LastSuccessAt), false})
	}
	rl := c.Relay
	switch {
	case rl.URL == nil:
		out = append(out, healthItem{t.Faint, "no relay", "no relay", false})
	case rl.LastError != nil:
		out = append(out, healthItem{t.Out, "relay failing", "relay failing", false})
	case rl.Pending || rl.LastPushAt == nil:
		out = append(out, healthItem{t.Tight, "relay pending", "relay pending", false})
	default:
		out = append(out, healthItem{t.OK, "relay " + ago(rl.LastPushAt), "relay " + short(rl.LastPushAt), false})
	}
	up := c.Update
	switch {
	case selfupdate.Dev(c.Version):
		out = append(out, healthItem{t.Faint, "dev build", "dev build", false})
	case up.Staged != nil:
		v := p.txt(*up.Staged)
		out = append(out, healthItem{t.Accent, v + " runs next time", v + " next run", false})
	case up.Error != nil:
		out = append(out, healthItem{t.Tight, "update check failed", "update failed", false})
	case up.CheckedAt == nil:
		out = append(out, healthItem{t.Faint, "update not checked", "not checked", false})
	case up.Latest != nil && selfupdate.Newer(*up.Latest, c.Version):
		v := p.txt(*up.Latest)
		out = append(out, healthItem{t.Tight, v + " available", v + " available", false})
	default:
		out = append(out, healthItem{t.OK, "up to date", "up to date", false})
	}
	return out
}

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

// attentionSubject caps the subject column, so the text keeps its room.
const attentionSubject = 30

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
		label := a.Account
		if acct := p.account(a.Provider, a.Account); acct != nil && acct.Alias != nil && acct.Name == *acct.Alias {
			label = *acct.Alias
		}
		label = shortID(label)
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
	g := p.g
	old := ""
	if a.ReadingAge > 0 {
		old = g.sep + "reading " + age(time.Duration(a.ReadingAge)*time.Second) + " old"
	}
	forms := func(parts ...string) []chunks {
		var out []chunks
		for _, s := range parts {
			out = append(out, chunks{p.plain(s)})
		}
		return out
	}
	in := func(t time.Time) string { return dur(t.Sub(p.now)) }
	switch a.Kind {
	case AttentionOut:
		if a.At == nil {
			return forms("until its reset, at a time not known" + old)
		}
		s := "back " + p.clock(*a.At) + ", in " + in(*a.At)
		return forms(s+old, s)
	case AttentionOver:
		pace := " at " + p.pace(a) + " pace"
		if a.At == nil {
			pct := ""
			if a.Percent != nil {
				pct = strconv.Itoa(int(*a.Percent)) + "% "
			}
			return forms("on"+pace+" for "+pct+"at its reset"+old, "over at its reset")
		}
		verb := "runs out ~"
		if !a.At.After(p.now) {
			verb = "ran out ~"
		}
		s := verb + p.clock(*a.At)
		before := ""
		if a.ResetsAt != nil && a.ResetsAt.After(*a.At) {
			before = ", " + dur(a.ResetsAt.Sub(*a.At)) + " before reset"
		}
		// A short line drops the pace first, then the reading's age: when it
		// runs out and how long before the reset are what the line says.
		return forms(s+pace+before+old, s+before+old, s+before, s+old, s)
	case AttentionUnder:
		unused := "some of it"
		if a.Percent != nil {
			unused = "~" + strconv.Itoa(max(100-int(*a.Percent), 0)) + "%"
		}
		s := "leaves " + unused + " unused at " + p.pace(a) + " pace"
		if a.ResetsAt == nil {
			return forms(s, "leaves "+unused+" unused")
		}
		return forms(s+", resets "+p.clock(*a.ResetsAt)+", in "+in(*a.ResetsAt), s, "leaves "+unused+" unused")
	case AttentionError:
		return forms(p.txt(a.Message))
	case AttentionSilent:
		if a.At == nil {
			return forms("no report for a day")
		}
		s := "no report since " + p.clock(*a.At)
		return forms(s+", "+dur(p.now.Sub(*a.At))+" ago", s)
	case AttentionOld:
		latest := ""
		if a.Message != "" {
			latest = g.sep + "latest " + p.txt(a.Message)
		}
		var names []string
		for _, d := range a.Devices {
			names = append(names, p.txt(d))
		}
		// The names give way to a count, so the latest release keeps its place.
		return forms(nameList(names, nil, max(room-width(latest), 12), g.ell) + latest)
	}
	return forms(p.txt(a.Message))
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

var lengthPrefix = regexp.MustCompile(`^(\d+[mhd]) (.+)$`)

// trimLength drops the length before a window's name when it is the main
// window's length: "7d Fable" is "Fable" beside "7d".
func trimLength(name, mainName string) string {
	m := lengthPrefix.FindStringSubmatch(name)
	if m == nil {
		return name
	}
	if mm := lengthPrefix.FindStringSubmatch(mainName + " x"); mm != nil && mm[1] == m[1] {
		return m[2]
	}
	return name
}

// windowLength is a window's length, from the harness or its name.
func windowLength(w Window) time.Duration {
	return snapshot.Window{Name: w.Name, Minutes: w.Minutes}.Length()
}
