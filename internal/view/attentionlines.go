package view

import (
	"image/color"
	"math"
	"slices"
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
	case AttentionTight:
		return "TIGHT", t.Tight
	case AttentionOutside:
		return "OUTSIDE", t.Tight
	case AttentionNoReading:
		return "NO READING", t.Muted
	case AttentionDuplicate:
		return "DUPLICATE", t.Muted
	case AttentionError:
		return "ERROR", t.Out
	case AttentionSilent:
		return "SILENT", t.Tight
	case AttentionOld:
		return "OLD", t.Muted
	case AttentionUnder:
		return "UNDER", t.Under
	case AttentionIdle:
		return "IDLE", t.Muted
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
func (p *page) attention() []chunks { return p.attentionLines(p.r.Attention) }

// attentionLines is ATTENTION with the lines of as.
func (p *page) attentionLines(as []Attention) []chunks {
	if len(as) == 0 {
		return nil
	}
	var hidden []Attention
	if !p.o.Interactive && len(as) > attentionLines {
		as, hidden = as[:attentionLines], as[attentionLines:]
	}
	badgeW, subjW := 0, 0
	subjects := make([]chunks, len(as))
	for i, a := range as {
		word, _ := p.badgeOf(a.Kind)
		badgeW = max(badgeW, width(word)+2)
		subjects[i] = p.subject(a)
		if a.Kind == AttentionIdle {
			// A quiet line, dim throughout.
			for j := range subjects[i] {
				subjects[i][j] = p.muted(subjects[i][j].text)
			}
		}
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
		if k, ok := p.attentionPick(a); ok {
			p.pick(len(out), k)
		}
		out = append(out, append(line, text.cut(room, p.g.ell)...))
	}
	if len(hidden) > 0 {
		out = append(out, chunks{p.space(1), p.muted(p.moreText(hidden))})
	}
	return out
}

// moreText counts the ATTENTION lines the static page does not show, by
// kind where that fits, so a NO READING under them is still named.
func (p *page) moreText(hidden []Attention) string {
	more := "+" + strconv.Itoa(len(hidden)) + " more"
	n := map[string]int{}
	var kinds []string
	for _, a := range hidden {
		if n[a.Kind] == 0 {
			kinds = append(kinds, a.Kind)
		}
		n[a.Kind]++
	}
	var parts []string
	for _, k := range kinds {
		word, _ := p.badgeOf(k)
		parts = append(parts, strconv.Itoa(n[k])+" "+strings.ToLower(word))
	}
	if long := more + ": " + strings.Join(parts, ", "); width(long) < p.w {
		return long
	}
	return more
}

// subject is what an ATTENTION line is about: an account and its window, a
// device, or the devices on an old release.
func (p *page) subject(a Attention) chunks {
	switch a.Kind {
	case AttentionOut, AttentionOver, AttentionTight, AttentionUnder, AttentionOutside, AttentionNoReading, AttentionIdle:
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
// the line takes the first that fits in room. What to do stays in the
// shortest forms, and a path or label in the text is cut last.
func (p *page) attentionText(a Attention, room int) []chunks {
	// A stale reading's age is said shorter before a line drops it.
	var old, short string
	if a.ReadingAge > 0 {
		d := age(time.Duration(a.ReadingAge)*time.Second) + " old"
		old, short = p.g.sep+"reading "+d, p.g.sep+d
	}
	switch a.Kind {
	case AttentionOut:
		if a.At == nil {
			return p.forms("until its reset, at a time not known"+old, "until its reset"+old, "until its reset"+short)
		}
		back := "back " + p.clock(*a.At)
		s := back + ", in " + dur(a.At.Sub(p.now))
		return p.forms(s+old, back+old, back+short, s)
	case AttentionOver:
		return p.overForms(a, old, short)
	case AttentionTight:
		return p.tightForms(a, old, short)
	case AttentionOutside:
		return p.outsideForms(a)
	case AttentionUnder:
		return p.underForms(a)
	case AttentionIdle:
		return p.idleForms(a)
	case AttentionError:
		if a.At != nil {
			// How long ago, while the whole error fits beside it.
			return p.forms(p.txt(a.Message)+p.g.sep+age(p.now.Sub(*a.At))+" ago", p.txt(a.Message))
		}
		return p.forms(p.txt(a.Message))
	case AttentionNoReading:
		return p.noReadingForms(a, room)
	case AttentionSilent:
		return p.silentForms(a, room)
	case AttentionDuplicate:
		return p.duplicateForms(a)
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
// reset, or that would have run out had the team kept spending on it; old
// is how old its reading is, and short the same in fewer words, or both
// are empty.
func (p *page) overForms(a Attention, old, short string) []chunks {
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
		return p.forms(s+pace+old, s+old, s+short, s)
	}
	verb := "runs out ~"
	switch {
	case stopped(&p.r.Team, a):
		// Nobody spent on it since: it did not run out.
		verb = "would run out ~"
	case !at.After(p.now):
		verb = "ran out ~"
	}
	s := verb + p.clock(*at)
	// A short line drops the pace first, then the reading's age, said
	// shorter before it goes: when it runs out and how long before the
	// reset are what the line says.
	return p.forms(s+pace+before+old, s+before+old, s+before+short, s+before, s+old, s+short, s)
}

// tightForms is what to know about a window of an account logged in here
// that its forecast ends nearly full; old is how old its reading is, and
// short the same in fewer words, or both are empty.
func (p *page) tightForms(a Attention, old, short string) []chunks {
	s := "~" + strconv.Itoa(int(*a.Percent)) + "% at reset"
	if a.ResetsAt == nil {
		return p.forms(s+" at "+p.pace(a)+" pace"+old, s+old, s+short, s)
	}
	at := s + " " + p.clock(*a.ResetsAt)
	return p.forms(at+" at "+p.pace(a)+" pace"+old, at+old, at+short, at, s+short, s)
}

// outsideForms is what to know about a window that filled while the team
// spent next to nothing on it.
func (p *page) outsideForms(a Attention) []chunks {
	s := "+" + strconv.Itoa(int(math.Round(*a.Percent))) + " points"
	in := s
	if a.At != nil {
		in += " in " + age(p.now.Sub(*a.At))
	}
	const none = ", no team device spent"
	return p.forms(in+none, s+none, in, s)
}

// noReadingForms is why a subscription in use has no reading. What to do
// comes after the first ": ", as in "annbook runs v0.2.0: reinstall with
// install.sh", so a short line cuts what comes before it first.
func (p *page) noReadingForms(a Attention, room int) []chunks {
	msg := p.txt(a.Message)
	out := p.forms(msg)
	short := msg
	if head, act, ok := strings.Cut(msg, ": "); ok {
		if _, version, ok := strings.Cut(head, " runs "); ok {
			// OLD names the device.
			out = append(out, p.forms("on "+version+": "+act)...)
		}
		// Of the unknown account's, where to log in is what to keep.
		if where, ok := strings.CutPrefix(head, "not logged in on "); ok {
			short = "on " + where + ": " + act
			out = append(out, p.forms(short)...)
		}
	}
	return append(out, p.forms(cutReason(short, room, p.g.ell))...)
}

// cutReason is a reason, "what: what to do", cut to w columns: what to do
// stays whole while what is wrong keeps a few columns, and is cut last. What
// to do that does not fit alone keeps its first steps, "a; b; c", whole.
func cutReason(msg string, w int, ell string) string {
	if width(msg) <= w {
		return msg
	}
	head, act, ok := strings.Cut(msg, ": ")
	if !ok {
		return truncEnd(msg, w, ell)
	}
	if left := w - width(": "+act); left >= 12 {
		head = truncEnd(head, left, ell)
		if cut, ok := strings.CutSuffix(head, ell); ok {
			head = strings.TrimRight(cut, " ") + ell
		}
		return head + ": " + act
	}
	if width(act) <= w {
		return act
	}
	steps := strings.Split(act, "; ")
	n := 0
	for n < len(steps)-1 && width(strings.Join(steps[:n+1], "; ")+"; "+ell) <= w {
		n++
	}
	if n == 0 {
		return truncEnd(act, w, ell)
	}
	return strings.Join(steps[:n], "; ") + "; " + ell
}

// duplicateForms is what to know about a device that was reinstalled: the
// command that forgets its old record, after the device that replaced it.
func (p *page) duplicateForms(a Attention) []chunks {
	by := "reinstalled as " + p.txt(a.Message)
	if a.DeviceID == "" {
		return p.forms(by)
	}
	cmd := "ai-usage team forget-device " + shortDevice(a.DeviceID)
	return p.forms(by+": "+cmd, cmd)
}

// shortDevice is a device id as a command takes it: its first 8 hex digits.
func shortDevice(id string) string {
	h := strings.TrimPrefix(id, "d-")
	if len(h) > 8 {
		h = h[:8]
	}
	return h
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

// idleForms is what to know about a paid subscription no team device has
// used for a week or more, dim, as it asks for nothing soon.
func (p *page) idleForms(a Attention) []chunks {
	s := "idle"
	if a.At != nil {
		s += " " + age(p.now.Sub(*a.At))
	}
	return []chunks{{p.muted(s + ", paid and unused")}, {p.muted(s)}}
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

// oldForms is what to know about the devices on an old release: what their
// release cannot do and the reinstall it takes, when it lacks something.
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
	if lack, short := limitText(a.Limitations); lack != "" {
		const fix = "; reinstall with install.sh"
		return p.forms(list(g.sep+lack+fix), lack+fix, short+fix, short+"; reinstall", strings.TrimPrefix(fix, "; "))
	}
	if a.At != nil && p.now.Sub(*a.At) >= BehindAfter {
		up := ""
		if len(a.Devices) > 1 {
			up = "up to "
		}
		return p.forms(list(latest+g.sep+"not updated for "+up+age(p.now.Sub(*a.At))), list(latest))
	}
	return p.forms(list(latest))
}

// limitText is the first of a release's limitations in words, in a long and
// a short form, or empty.
func limitText(ls []string) (long, short string) {
	switch {
	case slices.Contains(ls, LimitNoClaudeQuota):
		return "cannot read Claude quota", "no Claude quota"
	case slices.Contains(ls, LimitNoUpdateErrors):
		return "does not report update errors", "no update errors"
	}
	return "", ""
}

// pace names the pace an over, tight, or under line forecasts at: the last
// day's, when the forecast takes that, or the recent one in a window
// shorter than a day, whose readings go back only to its reset; else this
// week's, or this window's when it is not a week long.
func (p *page) pace(a Attention) string {
	w := p.window(a.Provider, a.Account, a.Window)
	if w != nil && w.Forecast != nil && w.Forecast.Basis == BasisRecent {
		if windowLength(*w) < 24*time.Hour {
			return "the recent"
		}
		return "the last day's"
	}
	if w == nil || windowLength(*w) == week {
		return "this week's"
	}
	return "this window's"
}

// window is the named window of an account, or its main window when name
// is empty; nil when the report does not hold it.
func (p *page) window(provider, label, name string) *Window {
	return windowOf(&p.r.Team, provider, label, name)
}

// windowOf is the named window of a team account, or its main window when
// name is empty; nil when the team does not hold it.
func windowOf(t *Team, provider, label, name string) *Window {
	acct := accountOf(t, provider, label)
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

// stopped says an over entry is of a window whose forecast is Stopped: it
// would have run out at its pace, but the team stopped spending before.
func stopped(t *Team, a Attention) bool {
	if a.Kind != AttentionOver {
		return false
	}
	w := windowOf(t, a.Provider, a.Account, a.Window)
	return w != nil && w.Forecast != nil && w.Forecast.Stopped
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
