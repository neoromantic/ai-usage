package view

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// pageSection is the plain lines of the body section whose title starts
// with title.
func pageSection(p Page, title string) []string {
	var out []string
	for _, l := range p.Body {
		l = sgr.ReplaceAllString(l, "")
		switch {
		case strings.HasPrefix(l, title):
			out = append(out, l)
		case len(out) > 0 && l == "":
			return out
		case len(out) > 0:
			out = append(out, l)
		}
	}
	return out
}

// bodyLine is the first line of p's body, as drawn, from the one whose
// text starts with from on, whose text matches.
func bodyLine(t *testing.T, p Page, from string, match func(text string) bool) string {
	t.Helper()
	in := false
	for _, l := range p.Body {
		text := sgr.ReplaceAllString(l, "")
		in = in || strings.HasPrefix(text, from)
		if in && match(text) {
			return l
		}
	}
	t.Fatalf("no line from %q on matches", from)
	return ""
}

func TestPageAttentionIsCut(t *testing.T) {
	// The team fixture with a silent device and an unused window as well,
	// 11 lines of ATTENTION.
	r := loadReport(t, "team")
	since := time.Date(2026, 9, 21, 11, 2, 0, 0, time.UTC)
	resets := time.Date(2026, 9, 29, 13, 30, 0, 0, time.UTC)
	pct := 33.0
	r.Attention = append(r.Attention,
		Attention{Kind: AttentionSilent, Devices: []string{"bot-e"}, At: &since},
		Attention{Kind: AttentionUnder, Provider: "codex", Account: "lee@corp.test", Name: "lee", ResetsAt: &resets, Percent: &pct})
	static := pageSection(Render(r, Options{Width: 160, Loc: sampleZone}), "ATTENTION")
	if len(static) != 1+attentionLines+1 || strings.TrimSpace(static[len(static)-1]) != "+5 more" {
		t.Errorf("static ATTENTION is not cut at %d lines:\n%s", attentionLines, strings.Join(static, "\n"))
	}
	p := Render(r, Options{Width: 160, Loc: sampleZone, Interactive: true})
	all := pageSection(p, "ATTENTION")
	if len(all) != 1+11 {
		t.Fatalf("the interactive view does not show every line:\n%s", strings.Join(all, "\n"))
	}
	for _, want := range []string{
		" SILENT      bot-e                      no report since Mon 14:02, 2d 3h ago",
		" UNDER       codex lee@corp.test        leaves ~67% unused at this week's pace, resets Tue 16:30, in 5d 22h",
	} {
		found := false
		for _, l := range all {
			found = found || l == want
		}
		if !found {
			t.Errorf("no line %q in\n%s", want, strings.Join(all, "\n"))
		}
	}
	if p.Legend != "" {
		t.Errorf("the interactive view has a legend: %q", p.Legend)
	}
}

// TestPageSilentDevice: a silent device whose last report had an error says
// since when, then the error where it fits, and has ~ in the matrix. A short
// line drops how long ago before it cuts the error.
func TestPageSilentDevice(t *testing.T) {
	r := loadReport(t, "team")
	since := time.Date(2026, 9, 21, 11, 2, 0, 0, time.UTC)
	msg := "codex: not logged in"
	for i := range r.Team.Devices {
		if d := &r.Team.Devices[i]; d.Label == "bot-e" {
			d.Silent, d.Error, d.CollectedAt = true, &msg, since
		}
	}
	// Beside the fixture's OUT, OVER, and ERROR lines, whose badges and
	// subjects leave it this room.
	r.Attention = slices.DeleteFunc(r.Attention, func(a Attention) bool {
		return a.Kind != AttentionOut && a.Kind != AttentionOver && a.Kind != AttentionError
	})
	r.Attention = append(r.Attention, Attention{Kind: AttentionSilent, Devices: []string{"bot-e"}, At: &since, Message: msg})
	for w, want := range map[int]string{
		80:  " SILENT  bot-e                no report since Mon 14:02, 2d 3h ago",
		84:  " SILENT  bot-e                no report since Mon 14:02 · last error: codex: not l…",
		92:  " SILENT  bot-e                no report since Mon 14:02 · last error: codex: not logged in",
		96:  " SILENT  bot-e                no report since Mon 14:02 · last error: codex: not logged in",
		120: " SILENT  bot-e                no report since Mon 14:02, 2d 3h ago · last error: codex: not logged in",
	} {
		p := Render(r, Options{Width: w, Loc: sampleZone, Interactive: true})
		if lines := pageSection(p, "ATTENTION"); lines[len(lines)-1] != want {
			t.Errorf("at %d: %q, want %q", w, lines[len(lines)-1], want)
		}
		found := false
		for _, l := range pageSection(p, "DEVICES") {
			found = found || strings.HasPrefix(l, "~ bot-e ")
		}
		if !found {
			t.Errorf("at %d: bot-e is not marked silent:\n%s", w, strings.Join(pageSection(p, "DEVICES"), "\n"))
		}
	}
}

// TestPageOverLine: an OVER line that is short of room drops the pace
// first, then the reading's age, and keeps how long before the reset.
func TestPageOverLine(t *testing.T) {
	r := loadReport(t, "team")
	for w, want := range map[int][]string{
		80: {
			" OVER     claude ann@acme.dev  runs out ~Thu 01:17, 2d 23h before reset",
			" OVER     codex sam@mail.test  runs out ~Sat 03:01, 2d 17h before reset",
		},
		100: {
			" OVER     claude ann@acme.dev  runs out ~Thu 01:17, 2d 23h before reset · reading 1d old",
			" OVER     codex sam@mail.test  runs out ~Sat 03:01 at the last day's pace, 2d 17h before reset",
		},
	} {
		lines := pageSection(Render(r, Options{Width: w, Loc: sampleZone}), "ATTENTION")
		if len(lines) < 5 || lines[3] != want[0] || lines[4] != want[1] {
			t.Errorf("at %d:\n%s\nwant\n%s", w, strings.Join(lines, "\n"), strings.Join(want, "\n"))
		}
	}
}

// TestPageOverAtReset: a window at 50% with half the week gone is on at this
// week's pace for 100%, and runs out at its reset; at 50.2% it is on for
// 100% too, and runs out 40 minutes before. Each OVER line says when, and
// how long before the reset.
func TestPageOverAtReset(t *testing.T) {
	for _, c := range []struct {
		used float64
		read time.Duration // how long ago the reading was taken
		want map[int]string
	}{
		{50, 0, map[int]string{
			80:  " OVER  codex kim@mail.test  runs out ~Sat 00:00, at its reset",
			120: " OVER  codex kim@mail.test  runs out ~Sat 00:00 at this week's pace, at its reset",
		}},
		{50, 30 * time.Hour, map[int]string{
			80:  " OVER  codex kim@mail.test  runs out ~Thu 18:00, at its reset · reading 1d old",
			120: " OVER  codex kim@mail.test  runs out ~Thu 18:00 at this week's pace, at its reset · reading 1d old",
		}},
		{50.2, 0, map[int]string{
			80:  " OVER  codex kim@mail.test  runs out ~Fri 23:19, 40m before reset",
			120: " OVER  codex kim@mail.test  runs out ~Fri 23:19 at this week's pace, 40m before reset",
		}},
	} {
		at := now.Add(-c.read)
		st := emptyState()
		addAccount(st, "codex", "kim@mail.test", true, &state.Quota{At: at, Windows: []snapshot.Window{week7(c.used, at.Add(week/2))}}, 10)
		r := Build(newFixture(t, st).in)
		if len(r.Attention) != 1 || r.Attention[0].Kind != AttentionOver || *r.Attention[0].Percent != 100 {
			t.Fatalf("%v%% read %v ago: attention %+v", c.used, c.read, r.Attention)
		}
		for w, want := range c.want {
			lines := pageSection(Render(r, Options{Width: w, Loc: time.UTC}), "ATTENTION")
			if len(lines) != 2 || lines[1] != want {
				t.Errorf("%v%% read %v ago, at %d:\n%s\nwant\n%s", c.used, c.read, w, strings.Join(lines, "\n"), want)
			}
		}
		// A report that does not say when the window resets says it all
		// the same.
		r.Attention[0].At, r.Attention[0].ResetsAt = nil, nil
		lines := pageSection(Render(r, Options{Width: 120, Loc: time.UTC}), "ATTENTION")
		if want := " OVER  codex kim@mail.test  runs out at its reset at this week's pace"; len(lines) != 2 || !strings.HasPrefix(lines[1], want) {
			t.Errorf("%v%% read %v ago, with no reset:\n%s\nwant\n%s", c.used, c.read, strings.Join(lines, "\n"), want)
		}
	}
}

// TestPageOldLine: on a team where every other device is behind, the OLD
// line's names give way to a count, so the latest release stays on it, and
// the subject of devices on several releases is not cut, from 80 columns.
func TestPageOldLine(t *testing.T) {
	names := regexp.MustCompile(`^ OLD +14 devices on (v1\.4\.0|old releases)  +(.+) \+(\d+) · latest v1\.4\.2$`)
	for _, mixed := range []bool{false, true} {
		r := loadReport(t, "team")
		var old []string
		for i := range r.Team.Devices {
			d := &r.Team.Devices[i]
			if d.This {
				continue
			}
			d.Old, d.CollectorVersion = true, "v1.4.0"
			if mixed && d.Label == "bot-a" {
				d.CollectorVersion = "v1.3.9"
			}
			old = append(old, d.Label)
		}
		sort.Strings(old)
		for i := range r.Attention {
			if r.Attention[i].Kind == AttentionOld {
				r.Attention[i].Devices = old
			}
		}
		for _, w := range []int{80, 100, 120} {
			lines := pageSection(Render(r, Options{Width: w, Loc: sampleZone, Interactive: true}), "ATTENTION")
			line := lines[len(lines)-1]
			m := names.FindStringSubmatch(line)
			if m == nil || (m[1] == "old releases") != mixed {
				t.Errorf("mixed %v at %d: %q", mixed, w, line)
				continue
			}
			shown := len(strings.Split(m[2], ", "))
			if more, _ := strconv.Atoi(m[3]); shown+more != len(old) {
				t.Errorf("mixed %v at %d: %d names and +%d of %d: %q", mixed, w, shown, more, len(old), line)
			}
		}
	}
}

func TestPageMatrixScroll(t *testing.T) {
	r := loadReport(t, "team")
	p := Render(r, Options{Width: 80, Loc: sampleZone})
	if p.MatrixColumns != 8 || p.MatrixShown != 4 {
		t.Errorf("columns %d, shown %d; want 8 and 4", p.MatrixColumns, p.MatrixShown)
	}
	p = Render(r, Options{Width: 80, Loc: sampleZone, Interactive: true, MatrixScroll: 5})
	m := pageSection(p, "DEVICES")
	if p.MatrixShown != 3 || !strings.Contains(m[1], "GROK") || !strings.Contains(m[1], "NO QUOTA") ||
		!strings.Contains(m[2], "hermes") || strings.Contains(m[2], "lee") {
		t.Errorf("scrolled by 5, shown %d:\n%s", p.MatrixShown, strings.Join(m, "\n"))
	}
	if !strings.HasPrefix(m[3], "  srv1 ") {
		t.Errorf("the device column is not kept: %q", m[3])
	}
	// Past the end, the last column still shows.
	p = Render(r, Options{Width: 80, Loc: sampleZone, MatrixScroll: 99})
	if p.MatrixShown != 1 {
		t.Errorf("scrolled past the end, shown %d", p.MatrixShown)
	}
	if single := Render(loadReport(t, "single"), Options{Width: 80}); single.MatrixColumns != 0 {
		t.Errorf("one device has a matrix of %d columns", single.MatrixColumns)
	}
}

// TestPageDevicesHead: the page says which of its lines head the rows of
// DEVICES, the title, the group headings, and the column headers, and where
// the section ends, after TOTAL, in both views and both modes, in every
// period, at every tier, scrolled sideways or not. USAGE, on a single
// device, has none.
func TestPageDevicesHead(t *testing.T) {
	for name, r := range map[string]Report{"team": loadReport(t, "team")} {
		for _, w := range []int{80, 120, 160} {
			for _, status := range []bool{false, true} {
				for _, share := range []bool{false, true} {
					for _, per := range Periods {
						for _, scroll := range []int{0, 2} {
							o := Options{Width: w, Loc: sampleZone, DeviceStatus: status, Share: share, Period: per,
								Interactive: scroll > 0, MatrixScroll: scroll}
							p := Render(r, o)
							h, end := p.DevicesHead, p.DevicesEnd
							sec := pageSection(p, "DEVICES")
							if h[1]-h[0] != 3 || end-h[0] != len(sec) || end-h[1]-1 != len(r.Team.Devices) {
								t.Errorf("%s %+v: head %v, end %d, for a section of %d lines:\n%s", name, o, h, end, len(sec), strings.Join(sec, "\n"))
								continue
							}
							for i, l := range sec {
								if got := sgr.ReplaceAllString(p.Body[h[0]+i], ""); got != l {
									t.Errorf("%s %+v: line %d of the section is %q, not %q", name, o, h[0]+i, got, l)
								}
							}
							if !strings.HasPrefix(sec[len(sec)-1], "  TOTAL ") || end < len(p.Body) && p.Body[end] != "" {
								t.Errorf("%s %+v: the section does not end after TOTAL at %d", name, o, end)
							}
						}
					}
				}
			}
		}
	}
	for _, status := range []bool{false, true} {
		if p := Render(loadReport(t, "single"), Options{Width: 120, DeviceStatus: status}); p.DevicesHead != [2]int{} || p.DevicesEnd != 0 {
			t.Errorf("a single device, status %v: head %v, end %d", status, p.DevicesHead, p.DevicesEnd)
		}
	}
}

func TestPageHeat(t *testing.T) {
	r := loadReport(t, "team")
	row := func(p Page, device string) string {
		return bodyLine(t, p, "DEVICES", func(text string) bool {
			return strings.Contains(text[:min(len(text), 24)], " "+device+" ")
		})
	}
	// In color the top step is bold: the largest cell on the page.
	color := Render(r, Options{Width: 120, Loc: sampleZone, Color: true})
	if l := row(color, "srv1"); !strings.Contains(l, "\x1b[1;38;2;") || !strings.Contains(l, "m1080\x1b[m") {
		t.Errorf("the largest cell is not bold in color: %q", l)
	}
	// Without color the largest of each column is bold, and nothing else.
	mono := Render(r, Options{Width: 120, Loc: sampleZone})
	if l := row(mono, "annbook"); !strings.Contains(l, "\x1b[1m810\x1b[m") || !strings.Contains(l, "\x1b[1m230\x1b[m") {
		t.Errorf("the largest of a column is not bold: %q", l)
	}
	if l := row(mono, "bot-a"); strings.Contains(l, "\x1b[1m") {
		t.Errorf("a cell that is no column's largest is bold: %q", l)
	}
	for _, v := range []struct {
		v, top float64
		step   int
	}{{240, 240, 5}, {76, 240, 5}, {75, 240, 4}, {24, 240, 3}, {2, 240, 1}, {0.4, 240, 1}} {
		if got := heatStep(v.v, v.top); got != v.step {
			t.Errorf("heatStep(%v, %v) = %d, want %d", v.v, v.top, got, v.step)
		}
	}
}

// TestPageHeadersDim: every column header is dim, the chosen period's too,
// in PROJECTS, the status view, and USAGE, in every period. The title of
// the first two names the period their rows are in the order of; USAGE has
// no order by period.
func TestPageHeadersDim(t *testing.T) {
	team, single := loadReport(t, "team"), loadReport(t, "single")
	dim := sgr.FindString(lipglossFg(NewTheme(true).Muted))
	dimRuns := regexp.MustCompile(regexp.QuoteMeta(dim) + `[^\x1b]*\x1b\[m`)
	for _, per := range Periods {
		o := Options{Width: 120, Loc: sampleZone, Color: true, Dark: true, Period: per}
		bold := (&page{o: o}).bold("CLAUDE").st.Render("CLAUDE")
		head := strings.ToUpper(per.String())
		for _, c := range []struct {
			table, prefix, title string
			p                    Page
		}{
			{"PROJECTS", "  PROJECT ", "PROJECTS  annbook · by " + per.String() + " · ", Render(team, o)},
			{"status", "  DEVICE ", "DEVICES  15 · 1 error · 1 silent · 4 old · by " + per.String() + " UTC · ", Render(team, devices(o))},
			{"USAGE", "  CLAUDE ", "USAGE  sam-air · M weighted tokens", Render(single, o)},
		} {
			l := bodyLine(t, c.p, c.title, func(text string) bool { return strings.HasPrefix(text, c.prefix) })
			if !strings.Contains(l, dim+head+"\x1b[m") {
				t.Errorf("%s by %s: %s is not dim: %q", c.table, per, head, l)
			}
			// Nothing else on the line but spaces, and USAGE's provider in
			// bold, is outside a dim run.
			if rest := strings.Replace(dimRuns.ReplaceAllString(l, ""), bold, "", 1); strings.TrimSpace(rest) != "" {
				t.Errorf("%s by %s: a header that is not dim, %q, in %q", c.table, per, rest, l)
			}
		}
	}
}

func TestPageHeaderBusy(t *testing.T) {
	r := loadReport(t, "team")
	h := sgr.ReplaceAllString(Render(r, Options{Width: 120, Loc: sampleZone, Busy: "⠋"}).Header, "")
	if !strings.Contains(h, "⠋ collecting") || strings.Contains(h, "collected 7m") {
		t.Errorf("busy header %q", h)
	}
}

// TestPageWideCharacters draws a page whose device, label, and path carry
// emoji, and measures each line as a terminal does: none is too wide, and
// the matrix keeps its columns.
func TestPageWideCharacters(t *testing.T) {
	r := loadReport(t, "team")
	r.Collector.DeviceLabel = "⚡✅✨❌⭐box"
	r.Projects[0].Path = "/Users/ann/src/⚡✅✨❌⭐-app"
	for i := range r.Team.Matrix.Rows {
		if r.Team.Matrix.Rows[i].Device == "srv1" {
			r.Team.Matrix.Rows[i].Device = "⚡️ci-runner✅"
		}
	}
	p := Render(r, Options{Width: 80, Loc: sampleZone})
	for i, l := range append([]string{p.Header}, p.Body...) {
		if w := ansi.StringWidth(l); w > 79 {
			t.Errorf("line %d is %d wide: %q", i, w, sgr.ReplaceAllString(l, ""))
		}
	}
	m := pageSection(p, "DEVICES")
	for _, l := range m[2:] {
		if w, want := ansi.StringWidth(l), ansi.StringWidth(m[2]); w != want {
			t.Errorf("a matrix row is %d wide, its header %d:\n%s", w, want, strings.Join(m, "\n"))
			break
		}
	}
}

// guideReport is a device's report the guide reads from: claude covered
// for 89 days, and a grok account known by its id.
func guideReport() Report {
	r := Report{GeneratedAt: now}
	r.Collector.DeviceLabel = "annbook"
	r.Collector.Schedule.Registered = true
	r.Providers = []Provider{
		{Provider: "claude", CoveredSince: tp(now.Add(-89 * 24 * time.Hour)), Accounts: []Account{{Label: "ann@acme.dev"}}},
		{Provider: "grok", Accounts: []Account{{Label: "a4c2e917", Name: "a4c2e917"}, {Label: "sam@mail.test", Name: "sam"}}},
	}
	return r
}

// TestGuideNotes: the guide says what a weighted token is, how to name a
// Grok account known by its id, that the team counts UTC days where there
// is a team, and how to keep 90 days of Claude where the ledger has less.
func TestGuideNotes(t *testing.T) {
	has := func(g, s string) bool { return strings.Contains(strings.Join(strings.Fields(g), " "), s) }
	const (
		weights = "weighted tokens: input 1, output 5 (codex 8), cache write 1.25–2, cache read 0.1–0.25, from API prices; a proxy for quota, not its formula"
		grok    = "name grok a4c2e917 for the team: ai-usage alias grok:a4c2e917 NAME"
		utc     = "USAGE and PROJECTS count your local days and rolling 7/30/90 days; team tables count UTC days, as devices send them"
		cleanup = `Claude Code deletes transcripts after 30 days: set "cleanupPeriodDays": 90 in ~/.claude/settings.json to keep 90D whole`
	)
	r := guideReport()
	g := Guide(r, "cron", Options{Width: 80, Loc: time.UTC})
	for _, l := range strings.Split(g, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("a line of %d cells: %q", w, l)
		}
	}
	for s, want := range map[string]bool{weights: true, grok: true, "alias grok:sam": false, utc: false, cleanup: false} {
		if has(g, s) != want {
			t.Errorf("the guide says %q: %v, want %v\n%s", s, !want, want, g)
		}
	}
	if !has(Guide(r, "cron", Options{Width: 80, Loc: time.UTC, ASCII: true}), strings.ReplaceAll(weights, "–", "-")) {
		t.Error("the ASCII guide's weights are not in ASCII")
	}

	// A team of one device is this device alone.
	r.Team.Devices = []TeamDevice{{Device: "a1"}}
	if has(Guide(r, "cron", Options{Width: 80, Loc: time.UTC}), utc) {
		t.Error("the guide notes UTC days without a team")
	}
	r.Team.Devices = append(r.Team.Devices, TeamDevice{Device: "b2"})
	r.Providers[0].CoveredSince = tp(now.Add(-20 * 24 * time.Hour))
	g = Guide(r, "cron", Options{Width: 80, Loc: time.UTC})
	if !has(g, utc) || !has(g, cleanup) {
		t.Errorf("the guide of a team, with 20 days of claude, lacks a note:\n%s", g)
	}
}

// TestGuideTranscripts: where Claude's transcripts miss tokens, the guide
// says so in one dim line, cut to the width.
func TestGuideTranscripts(t *testing.T) {
	const tail = "; ai-usage uses Claude Code's own count where it wrote one"
	for _, c := range []struct {
		gaps [2]*TranscriptGaps
		want string
	}{
		{[2]*TranscriptGaps{}, ""},
		{[2]*TranscriptGaps{{NullSnapshots: 30, OverTracked: 2}, {NullSnapshots: 7, OverTracked: 1}},
			"claude: 37 replies end without a final token count and 3 sessions report over twice their transcript's output" + tail},
		{[2]*TranscriptGaps{{NullSnapshots: 1}}, "claude: 1 reply ends without a final token count" + tail},
		{[2]*TranscriptGaps{nil, {OverTracked: 1}}, "claude: 1 session reports over twice its transcript's output" + tail},
		{[2]*TranscriptGaps{nil, {OverTracked: 2}}, "claude: 2 sessions report over twice their transcript's output" + tail},
	} {
		r := guideReport()
		r.Providers[0].Accounts = []Account{{Label: "ann@acme.dev", TranscriptGaps: c.gaps[0]}, {Label: "sam@mail.test", TranscriptGaps: c.gaps[1]}}
		// Grok's own gaps, were there any, are not Claude's.
		r.Providers[1].Accounts[0].TranscriptGaps = &TranscriptGaps{NullSnapshots: 5}
		if got := transcriptNote(r); got != c.want {
			t.Errorf("%v: %q, want %q", c.gaps, got, c.want)
		}
		g := Guide(r, "cron", Options{Width: 80, Loc: time.UTC})
		i := strings.Index(g, "  claude: ")
		if (i >= 0) != (c.want != "") {
			t.Errorf("%v: the guide's line at %d:\n%s", c.gaps, i, g)
			continue
		}
		if i >= 0 {
			l, _, _ := strings.Cut(g[i:], "\n")
			if ansi.StringWidth(l) > 80 || !strings.HasPrefix(c.want, strings.TrimSuffix(strings.TrimSpace(l), "…")) {
				t.Errorf("%v: the guide's line %q", c.gaps, l)
			}
		}
	}
}
