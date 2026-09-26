package view

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

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

// moreAttention is the team fixture with a silent device and an unused
// window as well, 8 lines of ATTENTION.
func moreAttention(t *testing.T) Report {
	r := loadReport(t, "team")
	since := time.Date(2026, 9, 21, 11, 2, 0, 0, time.UTC)
	resets := time.Date(2026, 9, 29, 13, 30, 0, 0, time.UTC)
	pct := 33.0
	r.Attention = append(r.Attention,
		Attention{Kind: AttentionSilent, Devices: []string{"bot-e"}, At: &since},
		Attention{Kind: AttentionUnder, Provider: "codex", Account: "lee@corp.test", Name: "lee", ResetsAt: &resets, Percent: &pct})
	return r
}

func TestPageAttentionIsCut(t *testing.T) {
	r := moreAttention(t)
	static := pageSection(Render(r, Options{Width: 160, Loc: sampleZone}), "ATTENTION")
	if len(static) != 1+attentionLines+1 || strings.TrimSpace(static[len(static)-1]) != "+2 more" {
		t.Errorf("static ATTENTION is not cut at %d lines:\n%s", attentionLines, strings.Join(static, "\n"))
	}
	p := Render(r, Options{Width: 160, Loc: sampleZone, Interactive: true})
	all := pageSection(p, "ATTENTION")
	if len(all) != 1+8 {
		t.Fatalf("the interactive view does not show every line:\n%s", strings.Join(all, "\n"))
	}
	for _, want := range []string{
		" SILENT  bot-e                no report since Mon 14:02, 2d 3h ago",
		" UNDER   codex lee@corp.test  leaves ~67% unused at this week's pace, resets Tue 16:30, in 5d 22h",
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
			" OVER   claude ann@acme.dev  runs out ~Thu 01:17, 2d 23h before reset",
			" OVER   codex sam@mail.test  runs out ~Sat 06:54, 2d 13h before reset",
		},
		100: {
			" OVER   claude ann@acme.dev  runs out ~Thu 01:17, 2d 23h before reset · reading 1d old",
			" OVER   codex sam@mail.test  runs out ~Sat 06:54 at this week's pace, 2d 13h before reset",
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
	names := regexp.MustCompile(`^ OLD +12 devices on (v1\.4\.0|old releases)  +(.+) \+(\d+) · latest v1\.4\.2$`)
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
			lines := pageSection(Render(r, Options{Width: w, Loc: sampleZone}), "ATTENTION")
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
	if p.MatrixColumns != 8 || p.MatrixShown != 5 {
		t.Errorf("columns %d, shown %d; want 8 and 5", p.MatrixColumns, p.MatrixShown)
	}
	p = Render(r, Options{Width: 80, Loc: sampleZone, Interactive: true, MatrixScroll: 5})
	m := pageSection(p, "DEVICES")
	if p.MatrixShown != 3 || !strings.Contains(m[1], "CODEX") || !strings.Contains(m[1], "NO QUOTA") ||
		!strings.Contains(m[2], "unknown") || strings.Contains(m[2], "lee") {
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
		matrix := false
		for _, l := range p.Body {
			plain := sgr.ReplaceAllString(l, "")
			matrix = matrix || strings.HasPrefix(plain, "DEVICES")
			if matrix && strings.Contains(plain[:min(len(plain), 24)], " "+device+" ") {
				return l
			}
		}
		t.Fatalf("no row of %s", device)
		return ""
	}
	// In color the top step is bold: the largest cell on the page.
	color := Render(r, Options{Width: 120, Loc: sampleZone, Color: true})
	if l := row(color, "srv1"); !strings.Contains(l, "\x1b[1;38;2;") || !strings.Contains(l, "m240\x1b[m") {
		t.Errorf("the largest cell is not bold in color: %q", l)
	}
	// Without color the largest of each column is bold, and nothing else.
	mono := Render(r, Options{Width: 120, Loc: sampleZone})
	if l := row(mono, "annbook"); !strings.Contains(l, "\x1b[1m19\x1b[m") || !strings.Contains(l, "\x1b[1m180\x1b[m") {
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
	// line is the first body line from the one that starts with from on
	// whose text starts with prefix, as it is drawn.
	line := func(p Page, from, prefix string) string {
		t.Helper()
		in := false
		for _, l := range p.Body {
			text := sgr.ReplaceAllString(l, "")
			in = in || strings.HasPrefix(text, from)
			if in && strings.HasPrefix(text, prefix) {
				return l
			}
		}
		t.Fatalf("no line %q after %q", prefix, from)
		return ""
	}
	for _, per := range Periods {
		o := Options{Width: 120, Loc: sampleZone, Color: true, Dark: true, Period: per}
		bold := (&page{o: o}).bold("CLAUDE").st.Render("CLAUDE")
		head := strings.ToUpper(per.String())
		for _, c := range []struct {
			table, prefix, title string
			p                    Page
		}{
			{"PROJECTS", "  PROJECT ", "PROJECTS  annbook · by " + per.String() + " · ", Render(team, o)},
			{"status", "  DEVICE ", "DEVICES  13 · 1 error · 2 old · by " + per.String() + " · ", Render(team, devices(o))},
			{"USAGE", "  CLAUDE ", "USAGE  sam-air · M tokens in+out", Render(single, o)},
		} {
			l := line(c.p, c.title, c.prefix)
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
