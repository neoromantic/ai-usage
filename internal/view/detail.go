package view

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Pick is a row of the page the interactive view can open in detail: an
// account, by its provider and label, or a device, by its ID.
type Pick struct {
	// Line is the row's line in Page.Body.
	Line     int
	Provider string
	Label    string
	Device   string
}

// Same says two picks open the same detail, wherever their rows are.
func (k Pick) Same(o Pick) bool {
	return k.Provider == o.Provider && k.Label == o.Label && k.Device == o.Device
}

// pick notes that line i of the section being drawn opens k.
func (p *page) pick(i int, k Pick) {
	k.Line = i
	p.picks = append(p.picks, k)
}

// attentionPick is what an ATTENTION line opens: the account it is about,
// or its one device; none for a line about several devices.
func (p *page) attentionPick(a Attention) (Pick, bool) {
	if a.Account != "" {
		return Pick{Provider: a.Provider, Label: a.Account}, p.account(a.Provider, a.Account) != nil || p.r.Cost.Subscription(a.Provider, a.Account) != nil
	}
	if a.DeviceID == "" && len(a.Devices) != 1 {
		return Pick{}, false
	}
	for _, d := range p.r.Team.Devices {
		if aboutDevice(a, d) {
			return Pick{Device: d.Device}, true
		}
	}
	return Pick{}, false
}

// aboutDevice says an ATTENTION entry is about device d, alone or among
// the devices on an old release: by its ID where it names one, else by its
// name and the state its kind reports, as two devices can go by one name.
// A report saved by a release before names named its devices by their
// labels.
func aboutDevice(a Attention, d TeamDevice) bool {
	switch {
	case a.Account != "":
		return false
	case a.DeviceID != "":
		return a.DeviceID == d.Device
	case !slices.Contains(a.Devices, d.Name) && !slices.Contains(a.Devices, d.Label):
		return false
	case a.Kind == AttentionSilent:
		return d.Silent
	case a.Kind == AttentionError:
		return !d.Silent
	case a.Kind == AttentionOld:
		return d.Old
	}
	return true
}

// Detail is the page of what k opens, under the page's header: an account's
// windows, its history, the devices that spent it, and this device's
// projects on it; or a device's collector, the accounts it spent, and, for
// this device, its projects. Each starts with its own ATTENTION lines. It
// is false when the report no longer has what k names.
func Detail(r Report, o Options, k Pick) (Page, bool) {
	p := newPage(&r, o)
	var sections [][]chunks
	switch {
	case k.Device != "":
		d := p.device(k.Device, "")
		if d == nil {
			return Page{}, false
		}
		sections = p.deviceDetail(d)
	default:
		a := p.account(k.Provider, k.Label)
		if s := p.r.Cost.Subscription(k.Provider, k.Label); a == nil && s != nil {
			a = costStub(s)
		}
		if a == nil {
			return Page{}, false
		}
		sections = p.accountDetail(k.Provider, a)
	}
	var body []chunks
	for _, s := range sections {
		if len(s) > 0 {
			body = append(body, nil)
			body = append(body, s...)
		}
	}
	return p.finish(body), true
}

// accountDetail is the sections of an account's detail. Its ATTENTION
// leaves out why it has no reading, which its head says in full.
func (p *page) accountDetail(provider string, a *TeamAccount) [][]chunks {
	about := func(at Attention) bool {
		return at.Provider == provider && at.Account == a.Label && at.Kind != AttentionNoReading
	}
	var consumers []chunks
	if s := p.r.Cost.Subscription(provider, a.Label); s != nil {
		consumers = p.costConsumers(s)
	}
	return [][]chunks{p.accountFacts(provider, a), p.attentionOf(about), p.windows(a), p.history(a), p.spenders(provider, a), consumers, p.accountProjects(provider, a)}
}

// deviceDetail is the sections of a device's detail. Its ATTENTION leaves
// out what its head says in full: that it is silent or reinstalled, and
// its errors.
func (p *page) deviceDetail(d *TeamDevice) [][]chunks {
	head := deviceErrors(d)
	if d.Error != nil {
		head = append(head, *d.Error)
	}
	if d.UpdateError != nil {
		head = append(head, sourceError("update", *d.UpdateError))
	}
	about := func(at Attention) bool {
		switch {
		case !aboutDevice(at, *d), at.Kind == AttentionDuplicate:
			return false
		case at.Kind == AttentionSilent, at.Kind == AttentionError:
			return at.Message != "" && !slices.Contains(head, at.Message)
		}
		return true
	}
	out := [][]chunks{p.deviceFacts(d), p.attentionOf(about), p.deviceAccounts(d), p.deviceCost(d)}
	if d.This {
		out = append(out, p.projects())
	}
	return out
}

// attentionOf is ATTENTION with only the lines about one account or device.
func (p *page) attentionOf(about func(Attention) bool) []chunks {
	var as []Attention
	for _, a := range p.r.Attention {
		if about(a) {
			as = append(as, a)
		}
	}
	return p.attentionLines(as)
}

// fact is a line of a detail's head: a dim key, and what it says.
type fact struct {
	key string
	val chunks
}

// factLines are a title and its facts, each value after its key, a value
// of one style that does not fit wrapped under itself.
func (p *page) factLines(title chunks, facts []fact) []chunks {
	keyW := 0
	for _, f := range facts {
		keyW = max(keyW, width(f.key))
	}
	out := []chunks{title}
	for _, f := range facts {
		lead := append(chunks{p.space(2)}, p.left(p.muted(f.key), keyW+2)...)
		room := p.w - lead.width()
		if len(f.val) != 1 || f.val.width() <= room || room < 12 {
			out = append(out, append(lead, f.val...))
			continue
		}
		for i, part := range wrapWords(f.val[0].text, room) {
			c := f.val[0]
			c.text = part
			if i > 0 {
				lead = chunks{p.space(lead.width())}
			}
			out = append(out, append(lead, c))
		}
	}
	return out
}

// when is how long ago t was, with its clock.
func (p *page) when(t time.Time) string {
	a := age(p.now.Sub(t))
	if a != "now" {
		a += " ago"
	}
	return a + p.g.sep + p.clock(t)
}

// accountFacts is an account's title, its provider and its label, with what
// the report knows of it in a line each.
func (p *page) accountFacts(provider string, a *TeamAccount) []chunks {
	g := p.g
	title := chunks{p.bold(strings.ToUpper(p.txt(provider))), p.plain("  "), p.bold(p.txt(a.Label))}
	if a.Current {
		p.mark("here")
		title = append(title, p.plain("  "), p.accent(g.here), p.muted(" logged in here"))
	}
	var facts []fact
	add := func(key string, val ...chunk) { facts = append(facts, fact{key, val}) }
	if a.Alias != nil {
		add("ALIAS", p.plain(p.txt(*a.Alias)))
	}
	if a.Plan != nil {
		add("PLAN", p.plain(p.txt(*a.Plan)))
	}
	if s := p.r.Cost.Subscription(provider, a.Label); s != nil {
		facts = append(facts, p.costFacts(s)...)
	}
	if q := a.Quota; q != nil {
		s := p.when(q.ObservedAt)
		if q.Device != "" {
			s += g.sep + "on " + p.txt(q.Device)
		}
		if q.From != "" {
			s += g.sep + "through " + p.txt(q.From)
		}
		if src := sourceWords[q.Source]; src != "" {
			s += g.sep + src
		}
		if q.Stale {
			p.mark("stale")
			add("READING", p.paint(g.silent+s, p.th.Tight, false, false))
		} else {
			add("READING", p.plain(s))
		}
	}
	if a.NoReading != nil {
		add("NO READING", p.muted(p.txt(*a.NoReading)))
	}
	if a.LastActiveAt != nil {
		add("LAST USE", p.plain(p.when(*a.LastActiveAt)))
	}
	if o := a.Outside; o != nil {
		add("OUTSIDE", p.paint("+"+wholePercent(o.Points)+" points since "+p.clock(o.Since)+" while the team spent next to nothing", p.th.Tight, false, false))
	}
	for _, l := range a.LinkedUsage {
		s := p.txt(l.Provider)
		if l.Label != "" {
			s += " " + p.txt(shortID(l.Label))
		}
		switch n := len(l.Devices); {
		case n > 3:
			s += " on " + strconv.Itoa(n) + " devices"
		case n > 0:
			s += " on " + p.txt(strings.Join(l.Devices, ", "))
		}
		add("LINKED", p.plain(s+g.sep+strconv.Itoa(l.Sessions)+" sessions"))
	}
	return p.factLines(title, facts)
}

// sourceWords say where a reading came from.
var sourceWords = map[string]string{
	"harness":   "the tool answered",
	"cache":     "from the tool's cache",
	"log":       "from the tool's logs",
	"rejection": "from refused requests",
}

// windows is each window of an account's reading: how much is used and
// left, when it resets, how full it ends, at what pace, and when it runs
// out at that pace.
func (p *page) windows(a *TeamAccount) []chunks {
	if a.Quota == nil || len(a.Quota.Windows) == 0 {
		return nil
	}
	g := p.g
	// The main window first, then the others in the reading's order.
	ws := slices.Clone(a.Quota.Windows)
	slices.SortStableFunc(ws, func(x, y Window) int { return btoi(y.Main) - btoi(x.Main) })
	main := mainWindow(ws)
	// PACE goes first, then the bar shrinks to half, then RUNS OUT goes.
	build := func(bar int) ([]chunk, []*dcol) {
		name := &dcol{head: "WINDOW"}
		used := &dcol{head: "USED"}
		left := &dcol{head: "LEFT", right: true}
		resets := &dcol{head: "RESETS"}
		clock := &dcol{}
		at := &dcol{head: "AT RESET", right: true}
		pace := &dcol{head: "PACE", drop: 1}
		out := &dcol{head: "RUNS OUT", drop: 3}
		var marks []chunk
		for i := range ws {
			w := &ws[i]
			n := w.Name
			if main != nil && w != main {
				n = trimLength(w.Name, main.Name)
			}
			marks = append(marks, p.space(1))
			name.cells = append(name.cells, chunks{p.plain(p.txt(n))})
			used.cells = append(used.cells, p.bar(w, bar))
			left.cells = append(left.cells, chunks{p.leftCell(w)})
			cd, when := p.resetsText(w)
			resets.cells = append(resets.cells, chunks{p.plain(cd)})
			clock.cells = append(clock.cells, chunks{p.muted(when)})
			at.cells = append(at.cells, chunks{p.atReset(w)})
			pace.cells = append(pace.cells, p.paceCell(w))
			out.cells = append(out.cells, p.runsOut(w))
		}
		return marks, []*dcol{name, used, left, resets, clock, at, pace, out}
	}
	marks, cols := build(barCells)
	_, narrow := build(narrowBar)
	cols[1].narrow, cols[1].narrowAt = narrow[1].cells, 2
	title := chunks{p.bold("WINDOWS"), p.plain("  "), p.muted(strconv.Itoa(len(ws)) + g.sep + "how full each is, and how full it ends")}
	return append([]chunks{title}, p.table(marks, cols)...)
}

// paceCell is a window's pace in points an hour, and which pace it is: the
// window's average, or the last day's, recent in a window shorter than a
// day.
func (p *page) paceCell(w *Window) chunks {
	if !known(w) || w.Forecast == nil {
		p.mark("none")
		return chunks{p.faint(p.g.none)}
	}
	f := w.Forecast
	basis := "avg"
	if f.Basis == BasisRecent {
		basis = "last day"
		if windowLength(*w) < 24*time.Hour {
			basis = "recent"
		}
	}
	return chunks{p.plain(strconv.FormatFloat(f.Pace, 'f', 1, 64) + "%/h"), p.muted(" " + basis)}
}

// runsOut is when a window runs out at its pace, and whether that forecast
// stopped, as nobody spent on it since.
func (p *page) runsOut(w *Window) chunks {
	if !known(w) || w.Forecast == nil || w.Forecast.RunsOutAt == nil {
		p.mark("none")
		return chunks{p.faint(p.g.none)}
	}
	out := chunks{p.inState("~"+p.clock(*w.Forecast.RunsOutAt), w.State)}
	if w.Forecast.Stopped {
		out = append(out, p.muted(" stopped"))
	}
	return out
}

// history is UTILIZATION for one account: a row for each of its windows,
// with how full each period got before its reset, as many as fit.
func (p *page) history(a *TeamAccount) []chunks {
	if len(a.Utilization) == 0 {
		return nil
	}
	grp := utilGroup{provider: "window"}
	most := 0
	main := a.Utilization[0].Window
	for i := range a.Utilization {
		u := &a.Utilization[i]
		name := u.Window
		if i > 0 {
			name = trimLength(name, main)
		}
		grp.rows = append(grp.rows, utilRow{name: p.txt(name), u: u})
		most = max(most, len(u.Cycles))
	}
	l := p.layoutUtil([]utilGroup{grp}, most)
	head := p.utilHeader(l, grp.provider)
	// Every header of a detail is dim.
	head[1] = p.muted(head[1].text)
	out := []chunks{{p.bold("UTILIZATION"), p.plain("  "), p.muted("fullest % before each reset")}, head}
	for _, r := range grp.rows {
		out = append(out, p.utilLine(l, r))
	}
	return out
}

// spenders are the devices that spent an account: in its current window,
// in each period, their part of its tokens in the chosen period, and when
// they last used it, the most in the chosen period first.
func (p *page) spenders(provider string, a *TeamAccount) []chunks {
	m := p.r.Team.Matrix
	col := slices.IndexFunc(m.Columns, func(c Column) bool {
		return !c.NoQuota && c.Provider == provider && c.Label == a.Label
	})
	if col < 0 {
		return nil
	}
	c := m.Columns[col]
	var rows []Row
	for _, r := range m.Rows {
		if col < len(r.Cells) && (r.Cells[col].Burn.Quarter > 0 || r.Cells[col].WindowBurn > 0) {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	per := p.o.Period
	slices.SortStableFunc(rows, byBurn(per, func(r Row) Usage { return r.Cells[col].Burn }, func(r Row) string { return r.Device }))
	last := func(id string) *time.Time {
		for _, d := range a.PerDevice {
			if d.DeviceID == id {
				return d.LastActiveAt
			}
		}
		return nil
	}
	// window is tokens since the window began, when it is known.
	window := func(n int64) int64 {
		if c.WindowStart == nil {
			return -1
		}
		return n
	}
	t := newTokenTable(p, "DEVICE")
	for _, r := range rows {
		cell := r.Cells[col]
		t.row(p.rowMark(r), chunks{p.plain(p.txt(r.Device))}, window(cell.WindowBurn), cell.Burn, per.share(cell.BurnShare), last(r.DeviceID))
	}
	// TOTAL is the whole of the account's tokens, when it has any.
	var whole *float64
	if per.Of(c.Burn) > 0 {
		hundred := 100.0
		whole = &hundred
	}
	t.row(p.space(1), chunks{p.muted("TOTAL")}, window(c.WindowBurn), c.Burn, whole, nil)
	title := chunks{p.bold("DEVICES"), p.plain("  "), p.muted(strconv.Itoa(len(rows)) + p.g.sep + "by " + p.teamPeriod() + p.g.sep + "M weighted tokens")}
	if c.WindowStart != nil {
		title = append(title, p.muted(p.g.sep+"window since "+c.WindowStart.In(p.loc).Format("Jan 2 15:04")))
	}
	return append([]chunks{title}, p.table(t.marks, t.cols())...)
}

// accountProjects is PROJECTS of this device's tokens on an account.
func (p *page) accountProjects(provider string, a *TeamAccount) []chunks {
	for _, pv := range p.r.Providers {
		if pv.Provider != provider {
			continue
		}
		for _, acct := range pv.Accounts {
			if acct.Label == a.Label {
				p.r.Projects = acct.Projects
				return p.projects()
			}
		}
	}
	return nil
}

// deviceFacts is a device's title, its name, with its collector's state in
// a line each: its user, when its name does not say it, its release, when it reported, what it reads, and
// what fails on it.
func (p *page) deviceFacts(d *TeamDevice) []chunks {
	g := p.g
	title := chunks{p.bold("DEVICE"), p.plain("  "), p.bold(p.txt(p.deviceName(d)))}
	if d.This {
		p.mark("this")
		title = append(title, p.plain("  "), p.accent(g.here), p.muted(" this device"))
	}
	var facts []fact
	add := func(key string, val ...chunk) { facts = append(facts, fact{key, val}) }
	if d.Name == d.Label {
		// A name without its user, as when the user is not known.
		add("USER", p.plain(p.txt(d.OSUser)))
	}
	version := chunks{p.plain(p.txt(d.CollectorVersion))}
	if d.Old {
		p.mark("old")
		version = chunks{p.muted(p.txt(d.CollectorVersion) + " " + g.old)}
		if l := p.r.Team.Latest; l != nil {
			version = append(version, p.muted(g.sep+"latest "+p.txt(*l)))
		}
		if notUpdating(*d) {
			version = append(version, p.muted(g.sep), p.paint("not updated for "+age(p.now.Sub(*d.BehindSince)), p.th.Tight, false, false))
		}
	}
	facts = append(facts, fact{"VERSION", version})
	if d.Silent {
		p.mark("silent")
		add("SEEN", p.paint("silent since "+p.clock(d.CollectedAt)+g.sep+age(p.now.Sub(d.CollectedAt))+" ago", p.th.Tight, false, false))
	} else {
		add("SEEN", p.plain(p.when(d.CollectedAt)))
	}
	if d.LastSuccessAt != nil && !d.LastSuccessAt.Equal(d.CollectedAt) {
		add("LAST OK", p.plain(p.when(*d.LastSuccessAt)))
	}
	if d.ReplacedBy != nil {
		add("REINSTALLED", p.muted("as "+p.txt(*d.ReplacedBy)))
	}
	facts = append(facts, fact{"VIA", p.harnesses(*d)})
	// Each harness that fails, else the run's own error.
	errs := deviceErrors(d)
	if len(errs) == 0 && d.Error != nil {
		errs = []string{*d.Error}
	}
	key := "ERROR"
	if d.Silent {
		key = "LAST ERROR"
	}
	for _, e := range errs {
		p.mark("fail")
		add(key, p.paint(p.txt(e), p.th.Out, false, false))
	}
	if d.UpdateError != nil {
		// A failed release check matters on an old release.
		ink := p.th.Muted
		if d.Old {
			ink = p.th.Out
		}
		add("UPDATE", p.paint(p.txt(*d.UpdateError), ink, false, false))
	}
	if long, _ := limitText(d.Limitations); long != "" {
		add("RELEASE", p.muted(long))
	}
	if d.Gap != nil {
		add("GAP", p.muted(age(time.Duration(d.Gap.Seconds)*time.Second)+" without a report at "+p.clock(d.Gap.At)+", the longest in 2 days"))
	}
	return p.factLines(title, facts)
}

// deviceErrors are the errors of each harness that fails on a device; a
// silent device's are the ones it last reported.
func deviceErrors(d *TeamDevice) []string {
	var errs []string
	for _, s := range d.Sources {
		if s.Error != nil && (s.Status == "error" || s.Status == "partial") {
			errs = append(errs, sourceError(s.Provider, *s.Error))
		}
	}
	return errs
}

// deviceName is a device's name as DEVICES shows it.
func (p *page) deviceName(d *TeamDevice) string {
	for _, r := range p.r.Team.Matrix.Rows {
		if r.DeviceID == d.Device {
			return r.Device
		}
	}
	return cmp.Or(d.Name, d.Label)
}

// deviceAccounts are the accounts a device spent: in their current
// windows, in each period, its part of each account's tokens in the chosen
// period, and when it last used each, the most in the chosen period first.
func (p *page) deviceAccounts(d *TeamDevice) []chunks {
	m := p.r.Team.Matrix
	i := slices.IndexFunc(m.Rows, func(r Row) bool { return r.DeviceID == d.Device })
	if i < 0 {
		return nil
	}
	row := m.Rows[i]
	var cols []int
	for k, c := range row.Cells {
		if k < len(m.Columns) && (c.Burn.Quarter > 0 || c.WindowBurn > 0) {
			cols = append(cols, k)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	per := p.o.Period
	name := func(k int) string { return m.Columns[k].Provider + " " + m.Columns[k].Name }
	slices.SortStableFunc(cols, byBurn(per, func(k int) Usage { return row.Cells[k].Burn }, name))
	t := newTokenTable(p, "ACCOUNT")
	for _, k := range cols {
		c, cell := m.Columns[k], row.Cells[k]
		label := chunks{p.muted(p.txt(c.Provider) + " ")}
		var last *time.Time
		if c.NoQuota {
			label = append(label, p.muted("no quota"))
		} else {
			acct := p.account(c.Provider, c.Label)
			s := shortID(c.Label)
			if acct != nil {
				s = shownLabel(acct)
				for _, u := range acct.PerDevice {
					if u.DeviceID == d.Device {
						last = u.LastActiveAt
					}
				}
			}
			label = append(label, p.plain(p.txt(s)))
		}
		var win int64 = -1
		if c.WindowStart != nil {
			win = cell.WindowBurn
		}
		t.row(p.space(1), label, win, cell.Burn, per.share(cell.BurnShare), last)
	}
	// SHARE is the device's part of each account; its part of the team's
	// tokens is in the title, not in TOTAL.
	t.row(p.space(1), chunks{p.muted("TOTAL")}, -1, row.Burn, nil, nil)
	if row.BurnEstimated {
		t.estimated()
	}
	title := chunks{p.bold("ACCOUNTS"), p.plain("  "), p.muted(strconv.Itoa(len(cols)) + p.g.sep + "by " + p.teamPeriod() + p.g.sep + "M weighted tokens")}
	if s := per.share(row.BurnShare); s != nil && len(m.Rows) > 1 {
		title = append(title, p.muted(p.g.sep+p.percent(s)+"% of the team"))
	}
	return append([]chunks{title}, p.table(t.marks, t.cols())...)
}

// tokenTable is a detail's table of weighted tokens: a name, the tokens in
// the current window, in each period, a share in the chosen period, and the
// last use.
type tokenTable struct {
	p                         *page
	marks                     []chunk
	name, window, share, last *dcol
	periods                   []*dcol
	// windows says a row has a current window, and lasts that a row's last
	// use is known: a column with no value in any row is left out.
	windows, lasts bool
}

func newTokenTable(p *page, name string) *tokenTable {
	t := &tokenTable{p: p, name: &dcol{head: name}, window: &dcol{head: "WINDOW", right: true, drop: 3},
		share: &dcol{head: "SHARE", right: true, drop: 2}, last: &dcol{head: "LAST", right: true, drop: 1}}
	for _, per := range Periods {
		drop := 0
		if per != p.o.Period && per != Quarter {
			drop = 4
		}
		t.periods = append(t.periods, &dcol{head: strings.ToUpper(per.String()), right: true, drop: drop})
	}
	return t
}

// row adds a row: win below 0 is no window, share nil none, and last nil
// no known use.
func (t *tokenTable) row(mark chunk, name chunks, win int64, u Usage, share *float64, last *time.Time) {
	p := t.p
	t.marks = append(t.marks, mark)
	t.name.cells = append(t.name.cells, name)
	if win < 0 {
		t.window.cells = append(t.window.cells, chunks{p.cell(p.g.none)})
	} else {
		t.windows = true
		t.window.cells = append(t.window.cells, chunks{p.cell(p.millions(win))})
	}
	for i, per := range Periods {
		t.periods[i].cells = append(t.periods[i].cells, chunks{p.cell(p.millions(per.Of(u)))})
	}
	s := p.percent(share)
	if s != p.g.none {
		s += "%"
	}
	t.share.cells = append(t.share.cells, chunks{p.cell(s)})
	at := chunks{p.cell(p.g.none)}
	if last != nil {
		t.lasts = true
		at = chunks{p.muted(age(p.now.Sub(*last)))}
	}
	t.last.cells = append(t.last.cells, at)
}

// estimated marks the last row's tokens as estimated from input and output.
func (t *tokenTable) estimated() {
	t.p.mark("estimated")
	for _, c := range t.periods {
		if cell := c.cells[len(c.cells)-1]; cell.text() != t.p.g.none {
			c.cells[len(c.cells)-1] = append(chunks{t.p.plain(t.p.g.silent)}, cell...)
		}
	}
}

func (t *tokenTable) cols() []*dcol {
	out := []*dcol{t.name}
	if t.windows {
		out = append(out, t.window)
	}
	out = append(append(out, t.periods...), t.share)
	if t.lasts {
		out = append(out, t.last)
	}
	return out
}

// dcol is a column of a detail's table: its heading, a cell for each row,
// and when it goes as the page narrows: 0 never, else the lowest first.
type dcol struct {
	head  string
	right bool
	cells []chunks
	drop  int
	// narrow are its cells in fewer columns, which it takes at narrowAt in
	// the order of drop.
	narrow   []chunks
	narrowAt int
	w        int
}

// tableWidth is how wide a table of cols is, each as wide as its heading
// and its widest cell, two columns of marks before them, two spaces apart.
func tableWidth(cols []*dcol) int {
	n := 2 + 2*(len(cols)-1)
	for _, c := range cols {
		c.w = width(c.head)
		for _, cell := range c.cells {
			c.w = max(c.w, cell.width())
		}
		n += c.w
	}
	return n
}

// table is a detail's table: the dim headings, then a row for each of
// marks, the mark before its cells. Where it is wider than the page, the
// columns go, or take their narrow cells, in their order of drop, then the
// first column is cut.
func (p *page) table(marks []chunk, cols []*dcol) []chunks {
	for tableWidth(cols) > p.w {
		k, step, narrow := -1, 0, false
		for i, c := range cols {
			if c.drop > 0 && (k < 0 || c.drop < step) {
				k, step, narrow = i, c.drop, false
			}
			if c.narrow != nil && (k < 0 || c.narrowAt < step) {
				k, step, narrow = i, c.narrowAt, true
			}
		}
		if k < 0 {
			break
		}
		if narrow {
			cols[k].cells, cols[k].narrow = cols[k].narrow, nil
			continue
		}
		cols = slices.Delete(slices.Clone(cols), k, k+1)
	}
	if over := tableWidth(cols) - p.w; over > 0 {
		cols[0].w = max(cols[0].w-over, minAccount)
	}
	line := func(lead chunks, cell func(c *dcol) chunks) chunks {
		for i, c := range cols {
			if i > 0 {
				lead = append(lead, p.space(2))
			}
			lead = append(lead, p.align(cell(c).cut(c.w, p.g.ell), c.w, c.right)...)
		}
		return lead
	}
	out := []chunks{line(chunks{p.space(2)}, func(c *dcol) chunks { return chunks{p.muted(c.head)} })}
	for r, m := range marks {
		out = append(out, line(chunks{m, p.space(1)}, func(c *dcol) chunks { return c.cells[r] }))
	}
	return out
}
