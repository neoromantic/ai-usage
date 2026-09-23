package view

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// problem is one thing wrong with a device: a short phrase for the NOTE
// column and, for errors, the full text for a line under the row.
type problem struct {
	kind   string // "error", "silent", "outdated"
	short  string
	detail string
	st     style
}

var severity = map[string]int{"error": 3, "silent": 2, "outdated": 1}

type drow struct {
	d        TeamDevice
	sev      int // 3 error, 2 silent, 1 outdated, 0 fine
	marker   seg
	problems []problem
	uses     []string
	inout    int64
	hasUsage bool
}

type devCols struct{ label, version, inout, note int }

func (u *ui) devLayout(rows []drow) devCols {
	c := devCols{label: min(24+(u.w+1-80)/4, 32), version: 7}
	fit := width("DEVICE")
	for _, r := range rows {
		fit = max(fit, width(hostUser(r.d)))
		c.version = max(c.version, min(width(r.d.CollectorVersion), 10))
	}
	c.label = min(c.label, fit)
	if u.wide {
		c.inout = 7
	}
	c.note = u.w - devFixed(c)
	return c
}

// devFixed is everything but NOTE: marker, DEVICE, VERSION, SEEN, the grid.
func devFixed(c devCols) int {
	n := 2 + c.label + 2 + c.version + 2 + 5 + 2 + 11 + 2
	if c.inout > 0 {
		n += c.inout + 2
	}
	return n
}

func hostUser(d TeamDevice) string { return d.Label + " (" + d.OSUser + ")" }

// latestVersion is the newest release the update check or any device knows.
func (u *ui) latestVersion() string {
	best := ""
	consider := func(v string) {
		if !selfupdate.Dev(v) && (best == "" || selfupdate.Newer(v, best)) {
			best = v
		}
	}
	if u.r.Collector.Update.Latest != nil {
		consider(*u.r.Collector.Update.Latest)
	}
	for _, d := range u.r.Team.Devices {
		consider(d.CollectorVersion)
	}
	return best
}

// shortLabel is an account label for a list: a UUID shows its first 8 digits.
func (u *ui) shortLabel(s string) string {
	if uuidRe.MatchString(s) {
		return s[:8] + u.g.ell
	}
	return s
}

func (u *ui) deviceRows() []drow {
	g := u.g
	latest := u.latestVersion()
	type use struct {
		label string
		t     int64
	}
	uses := map[string][]use{}
	inout := map[string]int64{}
	usage := map[string]bool{}
	for _, tp := range u.r.Team.Providers {
		for _, a := range tp.Accounts {
			for _, c := range a.PerDevice {
				uses[c.DeviceID] = append(uses[c.DeviceID], use{u.shortLabel(a.Label), c.Tokens.Total()})
				inout[c.DeviceID] += c.Tokens.Input + c.Tokens.Output
				usage[c.DeviceID] = true
			}
		}
	}
	var rows []drow
	for _, d := range u.r.Team.Devices {
		row := drow{d: d, marker: seg{"  ", plain}, inout: inout[d.Device], hasUsage: usage[d.Device]}
		ls := uses[d.Device]
		sort.SliceStable(ls, func(i, j int) bool { return ls[i].t > ls[j].t })
		seen := map[string]bool{}
		for _, x := range ls {
			if !seen[x.label] {
				seen[x.label] = true
				row.uses = append(row.uses, x.label)
			}
		}
		row.problems = u.problems(d, latest)
		for _, p := range row.problems {
			row.sev = max(row.sev, severity[p.kind])
		}
		switch {
		case d.This:
			row.marker = seg{g.here + " ", cyan}
			u.legend["here"] = true
		case row.sev == 3:
			row.marker = seg{g.fail + " ", red}
		case row.sev == 2:
			row.marker = seg{g.stale + " ", yellow}
			u.legend["oldDevice"] = true
		case row.sev == 1:
			row.marker = seg{g.old + " ", yellow}
			u.legend["outdated"] = true
		}
		rows = append(rows, row)
	}
	// This device, then errors, silent (longest first), outdated, the rest.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.d.This != b.d.This {
			return a.d.This
		}
		if a.sev != b.sev {
			return a.sev > b.sev
		}
		if a.sev == 2 && !a.d.CollectedAt.Equal(b.d.CollectedAt) {
			return a.d.CollectedAt.Before(b.d.CollectedAt)
		}
		if a.d.Label != b.d.Label {
			return a.d.Label < b.d.Label
		}
		return a.d.OSUser < b.d.OSUser
	})
	return rows
}

func (u *ui) problems(d TeamDevice, latest string) []problem {
	var out []problem
	for _, s := range d.Sources {
		if s.Status == "error" || s.Status == "partial" {
			detail := ""
			if s.Error != nil {
				detail = sourceError(s.Provider, *s.Error)
			}
			out = append(out, problem{"error", s.Provider + " " + s.Status, detail, red})
		}
	}
	if d.LastError != nil && (d.LastSuccessAt == nil || d.CollectedAt.After(*d.LastSuccessAt)) {
		out = append(out, problem{"error", "last run failed", "error: " + *d.LastError, red})
	}
	if u.now.Sub(d.CollectedAt) > silentDevice {
		out = append(out, problem{"silent", "silent since " + u.clock(d.CollectedAt, "Jan 2 15:04"), "", yellow})
	}
	if latest != "" && selfupdate.Newer(latest, d.CollectorVersion) {
		out = append(out, problem{"outdated", "outdated; latest " + latest, "", yellow})
	}
	return out
}

// sourceError names the provider once before a source's error. A harness's
// own messages already start with its name, as "codex: not logged in" does.
func sourceError(p, msg string) string {
	if strings.HasPrefix(msg, p+":") || strings.HasPrefix(msg, p+" ") {
		return msg
	}
	return p + ": " + msg
}

func (r drow) has(kind string) bool {
	for _, p := range r.problems {
		if p.kind == kind {
			return true
		}
	}
	return false
}

func (u *ui) devices() {
	g := u.g
	rows := u.deviceRows()
	count := map[string]int{}
	for _, r := range rows {
		for k := range severity {
			if r.has(k) {
				count[k]++
			}
		}
	}
	title := line{{"DEVICES", bold}, {"  " + strconv.Itoa(len(rows)), plain}}
	if u.r.Team.PulledAt == nil {
		title = append(title, seg{g.sep, gray}, seg{"relay not read yet", gray})
	} else {
		title = append(title, seg{g.sep, gray}, seg{"read " + ago(u.now.Sub(*u.r.Team.PulledAt)), gray})
	}
	add := func(n int, text string, st style) {
		if n > 0 {
			title = append(title, seg{g.sep, gray}, seg{strconv.Itoa(n) + " " + text, st})
		}
	}
	add(count["error"], "with errors", red)
	add(count["silent"], "silent over 1d", yellow)
	add(count["outdated"], "outdated", yellow)
	u.titleWrap(title, width("DEVICES  "))

	// Past foldDevices rows, healthy devices fold into one list.
	var shown, folded []drow
	for _, r := range rows {
		if u.allDevices || len(rows) <= foldDevices || r.d.This || r.sev > 0 {
			shown = append(shown, r)
		} else {
			folded = append(folded, r)
		}
	}
	c := u.devLayout(shown)
	hdr := "  " + padRight("DEVICE", c.label) + "  " + padRight("VERSION", c.version) + "  " + padLeft("SEEN", 5) + "  " + "cl cx gk hm" + "  "
	if c.inout > 0 {
		hdr += padLeft("IN+OUT", c.inout) + "  "
	}
	u.emit(line{{hdr + "NOTE", gray}})
	u.legend["grid"] = true

	for _, r := range shown {
		d := r.d
		l := line{r.marker}
		l = append(l, seg{padRight(truncEnd(hostUser(d), c.label, g.ell), c.label) + "  ", plain})
		vst := plain
		if r.has("outdated") {
			vst = yellow
		} else if selfupdate.Dev(d.CollectorVersion) {
			vst = gray
		}
		l = append(l, seg{padRight(truncEnd(d.CollectorVersion, c.version, g.ell), c.version), vst}, seg{"  ", plain})
		sst := plain
		if r.has("silent") {
			sst = yellow
		}
		l = append(l, seg{padLeft(span(u.now.Sub(d.CollectedAt)), 5), sst}, seg{"  ", plain})
		l = append(l, u.grid(d.Sources)...)
		l = append(l, seg{"   ", plain})
		if c.inout > 0 {
			v := g.dash
			if r.hasUsage {
				v = human(r.inout)
			} else {
				u.legend["dash"] = true
			}
			l = append(l, seg{padLeft(v, c.inout) + "  ", plain})
		}
		note, details := u.note(r, c.note)
		u.emit(append(l, note...))
		for _, dt := range details {
			u.emit(dt)
		}
	}
	if len(folded) > 0 {
		u.foldLine(folded)
	}
}

// foldLine lists healthy devices by name in at most two lines; what does
// not fit ends the second line as "+N more".
func (u *ui) foldLine(rows []drow) {
	g := u.g
	var oldest time.Duration
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		oldest = max(oldest, u.now.Sub(r.d.CollectedAt))
		names = append(names, truncEnd(u.shortDevice(hostUser(r.d)), 32, g.ell))
	}
	cur := line{{"  " + g.ok + " ", green}, {strconv.Itoa(len(rows)) + " more ok, seen within " + span(oldest) + ": ", plain}}
	lines, fresh := 1, true
	for i, n := range names {
		sep := ", "
		if fresh {
			sep = ""
		}
		// Line one keeps room for the comma it ends with; line two for
		// how many are left.
		reserve := 1
		if left := len(names) - i - 1; lines == 2 {
			reserve = 0
			if left > 0 {
				reserve = width(", +" + strconv.Itoa(left) + " more")
			}
		}
		if cur.width()+width(sep+n)+reserve > u.w {
			if lines == 2 {
				cur = append(cur, seg{", +" + strconv.Itoa(len(names)-i) + " more", gray})
				break
			}
			u.emit(append(cur, seg{",", gray}))
			cur, lines, sep = line{{"    ", plain}}, 2, ""
		}
		cur = append(cur, seg{sep + n, gray})
		fresh = false
	}
	u.emit(cur)
}

// grid is one status glyph per provider under "cl cx gk hm": "✓  ✓  ·  ·".
func (u *ui) grid(src []Source) line {
	g := u.g
	var l line
	for i, p := range collect.Providers {
		cell := seg{g.skip, gray}
		for _, s := range src {
			if s.Provider != p {
				continue
			}
			switch s.Status {
			case "ok":
				cell = seg{g.ok, green}
			case "partial":
				cell = seg{g.partial, yellow}
			case "error":
				cell = seg{g.fail, red}
			}
		}
		l = append(l, cell)
		if i < len(collect.Providers)-1 {
			l = append(l, seg{"  ", plain})
		}
	}
	return l
}

// note is the device's problems, or else the accounts it used, most first.
// A full error that does not fit the column goes on its own line below.
func (u *ui) note(r drow, w int) (line, []line) {
	g := u.g
	if len(r.problems) == 0 {
		if len(r.uses) > 0 {
			return line{{nameList(r.uses, w, g.ell), gray}}, nil
		}
		return line{{"no usage yet", gray}}, nil
	}
	full, short := line{}, line{}
	for i, p := range r.problems {
		if i > 0 {
			full = append(full, seg{"; ", gray})
			short = append(short, seg{"; ", gray})
		}
		text := p.short
		if p.detail != "" {
			text = p.detail
		}
		full = append(full, seg{text, p.st})
		short = append(short, seg{p.short, p.st})
	}
	if full.width() <= w {
		return full, nil
	}
	var details []line
	lead := "  " + g.branch + " "
	for _, p := range r.problems {
		if p.detail != "" {
			details = append(details, line{{lead, gray}, {truncEnd(p.detail, u.w-width(lead), g.ell), p.st}})
		}
	}
	return short.cut(w, g.ell), details
}
