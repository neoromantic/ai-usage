package view

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLegend ends the static page with one line of legend from 120 columns
// on, with every mark on screen, and with as few lines as fit below that.
func TestLegend(t *testing.T) {
	for _, c := range []struct {
		width, lines int
	}{{80, 2}, {100, 2}, {119, 2}, {120, 1}, {160, 1}} {
		for _, ascii := range []bool{false, true} {
			p := newPage(&Report{}, Options{Width: c.width, ASCII: ascii})
			for _, k := range []string{"used", "left", "tick", "tickIn", "dotted", "stale", "silent", "unknown", "unread",
				"dash", "here", "this", "fail", "old", "none", "chosen"} {
				p.mark(k)
			}
			lines := strings.Split(p.legend(), "\n")
			if len(lines) != c.lines {
				t.Errorf("at %d, ASCII %v, the legend is %d lines, want %d:\n%s", c.width, ascii, len(lines), c.lines, strings.Join(lines, "\n"))
			}
			for _, l := range lines {
				if width(l) > c.width-1 {
					t.Errorf("at %d, ASCII %v, a line of the legend is %d wide: %q", c.width, ascii, width(l), l)
				}
			}
		}
	}
	// The team's page at 120 shows every kind of mark, and ends with one
	// line of them, then why its totals are estimated.
	if l := strings.Split(Render(loadReport(t, "team"), Options{Width: 120, Loc: sampleZone}).Legend, "\n"); len(l) != 2 || !strings.HasPrefix(l[1], "~ stale/estimated: ") {
		t.Errorf("the team's legend at 120 is not one line of marks and one of why:\n%s", strings.Join(l, "\n"))
	}
}

// TestProjectPathsArePrintable draws a project whose folder's name carries
// escapes and a carriage return: none of them reaches the terminal.
func TestProjectPathsArePrintable(t *testing.T) {
	r := loadReport(t, "team")
	r.Projects[0].Path = "/Users/ann/src/x\x1b]52;c;ZWNobyBoaQ==\x07y"
	r.Projects[1].Path = "/Users/ann/src/\x1b[2Jwipe\rover"
	for _, o := range []Options{
		{Width: 120, Loc: sampleZone, Color: true},
		{Width: 120, Loc: sampleZone, ASCII: true},
	} {
		out := sgr.ReplaceAllString(Text(r, o), "")
		for _, c := range []string{"\x1b", "\x07", "\r"} {
			if strings.Contains(out, c) {
				t.Errorf("%+v: the page carries %q", o, c)
			}
		}
		if !strings.Contains(out, "~/src/x ]52;c;ZWNobyBoaQ== y") {
			t.Errorf("%+v: the path is not shown with spaces for its control characters:\n%s", o, out)
		}
	}
}

// projectsReport is a report of this device's projects alone, as of now,
// the ledger holding claude since 20 days and codex since 89.
func projectsReport(ps ...Project) Report {
	r := Report{GeneratedAt: now, Projects: ps}
	r.Collector.DeviceLabel = "annbook"
	r.Providers = []Provider{
		{Provider: "claude", CoveredSince: tp(now.Add(-20 * 24 * time.Hour))},
		{Provider: "codex", CoveredSince: tp(now.Add(-89 * 24 * time.Hour))},
	}
	return r
}

func burned(week, quarter int64) Usage {
	return Usage{Today: week / 7, Week: week, Month: quarter, Quarter: quarter}
}

// TestProjectsTopTen: the top 10 are by the burn shown for the period, and
// projects shown alike, as <1, go by 90 days.
func TestProjectsTopTen(t *testing.T) {
	var ps []Project
	for i := range 9 {
		ps = append(ps, Project{Path: "/p/big" + strconv.Itoa(i), Burn: burned(int64(i+2)*1_000_000, 90_000_000), Providers: []string{"codex"}})
	}
	ps = append(ps,
		Project{Path: "/p/busy-now", Burn: burned(900_000, 1_000_000), Providers: []string{"codex"}},
		Project{Path: "/p/busy-quarter", Burn: burned(100_000, 50_000_000), Providers: []string{"codex"}})
	sec := strings.Join(pageSection(Render(projectsReport(ps...), Options{Width: 100, Loc: time.UTC}), "PROJECTS"), "\n")
	if !strings.Contains(sec, "/p/busy-quarter") || strings.Contains(sec, "/p/busy-now") || !strings.Contains(sec, "+ 1 more") {
		t.Fatalf("top ten:\n%s", sec)
	}
}

// TestProjectsSessionsInPeriod: SESS counts the sessions active in the
// period, and none as none.
func TestProjectsSessionsInPeriod(t *testing.T) {
	r := projectsReport(Project{Path: "/p/app", Sessions: 4, Burn: burned(7_000_000, 9_000_000), Providers: []string{"codex"},
		Active: Counts{Today: 0, Week: 2, Month: 3, Quarter: 4}})
	for per, want := range map[Period]string{Today: "·", Week: "2", Month: "3", Quarter: "4"} {
		sec := pageSection(Render(r, Options{Width: 100, Loc: time.UTC, Period: per}), "PROJECTS")
		head, row := strings.Fields(sec[1]), strings.Fields(sec[2])
		if i := slices.Index(head, "SESS"); i < 0 || row[i] != want {
			t.Errorf("by %s: %v under %v, want SESS %s", per, row, head, want)
		}
	}
}

// TestProjectsCoverage: where the ledger holds less than 85 days of a
// provider, the title says since when, and the 90D of the projects it
// worked on is dim; the note goes first where the title is too wide, then
// the device's name.
func TestProjectsCoverage(t *testing.T) {
	r := projectsReport(
		Project{Path: "/p/claude", Burn: burned(7_000_000, 30_000_000), Providers: []string{"codex", "claude"}},
		Project{Path: "/p/codex", Burn: burned(5_000_000, 40_000_000), Providers: []string{"codex"}})
	o := Options{Width: 100, Loc: time.UTC, Color: true, Dark: true}
	p := Render(r, o)
	if title := pageSection(p, "PROJECTS")[0]; title != "PROJECTS  annbook · by 7d · M weighted tokens · claude since Aug 12" {
		t.Errorf("title %q", title)
	}
	dim := sgr.FindString(lipglossFg(NewTheme(true).Muted))
	for path, muted := range map[string]bool{"/p/claude": true, "/p/codex": false} {
		l := bodyLine(t, p, "PROJECTS", func(s string) bool { return strings.HasPrefix(s, "  "+path) })
		q := map[bool]string{true: "30", false: "40"}[muted]
		if got := strings.Contains(l, dim+q+"\x1b[m"); got != muted {
			t.Errorf("%s: 90D dim %v, want %v: %q", path, got, muted, l)
		}
	}
	// Every provider covers 85 days: no note, nothing dim.
	full := r
	full.Providers = []Provider{{Provider: "claude", CoveredSince: tp(now.Add(-85 * 24 * time.Hour))}}
	if title := pageSection(Render(full, o), "PROJECTS")[0]; title != "PROJECTS  annbook · by 7d · M weighted tokens" {
		t.Errorf("title %q", title)
	}
	for _, c := range []struct {
		label, want string
	}{
		{strings.Repeat("d", 20), "PROJECTS  dddddddddddddddddddd · by 7d · M weighted tokens"},
		{strings.Repeat("d", 60), "PROJECTS  by 7d · M weighted tokens"},
	} {
		r.Collector.DeviceLabel = c.label
		if title := pageSection(Render(r, Options{Width: 80, Loc: time.UTC}), "PROJECTS")[0]; title != c.want {
			t.Errorf("label of %d: title %q, want %q", len(c.label), title, c.want)
		}
	}
	// Of two providers short, the second's note goes first.
	r.Providers[1].CoveredSince = tp(now.Add(-10 * 24 * time.Hour))
	r.Collector.DeviceLabel = "annbook"
	for w, want := range map[int]string{
		100: "PROJECTS  annbook · by 7d · M weighted tokens · claude since Aug 12 · codex since Aug 22",
		80:  "PROJECTS  annbook · by 7d · M weighted tokens · claude since Aug 12",
	} {
		if title := pageSection(Render(r, Options{Width: w, Loc: time.UTC}), "PROJECTS")[0]; title != want {
			t.Errorf("two short at %d: title %q, want %q", w, title, want)
		}
	}
}
