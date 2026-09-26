package view

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// loadReport loads a report from testdata.
func loadReport(t *testing.T, name string) Report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// teamDir is the data directory the team fixture's status shows.
const teamDir = "/Users/ann/Library/Application Support/ai-usage"

// sampleZone is the zone of README.md's sample page: the fixtures' 14:38
// UTC is its 17:38.
var sampleZone = time.FixedZone("UTC+3", 3*60*60)

// plainText is the static page as a pipe gets it: every escape stripped.
func plainText(r Report, o Options) string { return sgr.ReplaceAllString(Text(r, o), "") }

// devices is o with DEVICES in its status view.
func devices(o Options) Options {
	o.DeviceStatus = true
	return o
}

func TestGolden(t *testing.T) {
	team, single := loadReport(t, "team"), loadReport(t, "single")
	page := func(w int) Options { return Options{Width: w, Loc: sampleZone} }
	for _, c := range []struct {
		name string
		out  func() string
	}{
		{"team-status-80", func() string { return StatusText(team, teamDir, Options{Width: 80, Loc: time.UTC}) }},
		{"single-status-80", func() string {
			return StatusText(single, "/Users/sam/Library/Application Support/ai-usage", Options{Width: 80, Loc: time.UTC})
		}},
		{"team-80", func() string { return plainText(team, page(80)) }},
		{"team-120", func() string { return plainText(team, page(120)) }},
		{"team-160", func() string { return plainText(team, page(160)) }},
		{"team-120-color", func() string {
			o := page(120)
			o.Color, o.Dark = true, true
			return Text(team, o)
		}},
		// Without color only bold and reverse video are left.
		{"team-120-mono", func() string { return Text(team, page(120)) }},
		{"team-80-ascii", func() string {
			o := page(80)
			o.ASCII = true
			return plainText(team, o)
		}},
		{"team-share", func() string {
			o := page(120)
			o.Share = true
			return plainText(team, o)
		}},
		{"team-share-color", func() string {
			o := page(120)
			o.Color, o.Dark, o.Share = true, true, true
			return Text(team, o)
		}},
		{"team-30d", func() string {
			o := page(120)
			o.Period = Month
			return plainText(team, o)
		}},
		{"single-80", func() string { return plainText(single, page(80)) }},
		{"single-120", func() string { return plainText(single, page(120)) }},
		{"projects-all", func() string {
			o := page(100)
			o.AllProjects = true
			return plainText(team, o)
		}},
		// The status view of DEVICES, as --devices prints it.
		{"team-devices-80", func() string { return plainText(team, devices(page(80))) }},
		{"team-devices-120", func() string { return plainText(team, devices(page(120))) }},
		{"team-devices-160", func() string { return plainText(team, devices(page(160))) }},
		{"team-devices-80-ascii", func() string {
			o := devices(page(80))
			o.ASCII = true
			return plainText(team, o)
		}},
		{"team-devices-120-color", func() string {
			o := devices(page(120))
			o.Color, o.Dark = true, true
			return Text(team, o)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := c.out()
			path := filepath.Join("testdata", c.name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v; run go test ./internal/view -update", err)
			}
			if got != string(want) {
				t.Errorf("%s differs; run go test ./internal/view -update and review the diff\ngot:\n%s", c.name, got)
			}
		})
	}
}

var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// TestWidths draws every fixture's status and page at the widths around
// each tier and checks the line contract: nothing wider than T-1, no
// trailing spaces, only ASCII in ASCII mode, and no color without color.
func TestWidths(t *testing.T) {
	reports := map[string]Report{"team": loadReport(t, "team"), "single": loadReport(t, "single")}
	for name, r := range reports {
		for _, w := range []int{80, 81, 99, 100, 119, 120, 159, 160} {
			for _, ascii := range []bool{false, true} {
				o := Options{Width: w, ASCII: ascii, Loc: time.UTC}
				checkLines(t, name, o, StatusText(r, teamDir, o))
			}
		}
		for w := 80; w <= 160; w++ {
			for _, ascii := range []bool{false, true} {
				for _, color := range []bool{false, true} {
					o := Options{Width: w, ASCII: ascii, Color: color, Loc: sampleZone}
					checkLines(t, name, o, Text(r, o))
					checkLines(t, name, devices(o), Text(r, devices(o)))
				}
			}
		}
		for _, w := range []int{80, 100, 120, 160} {
			for _, per := range Periods {
				for _, share := range []bool{false, true} {
					for _, ascii := range []bool{false, true} {
						o := Options{Width: w, Period: per, Share: share, ASCII: ascii, AllProjects: share, Loc: sampleZone}
						checkLines(t, name, o, Text(r, o))
						checkLines(t, name, devices(o), Text(r, devices(o)))
						o.Interactive, o.MatrixScroll, o.Busy = true, 3, "|"
						checkLines(t, name, o, Text(r, o))
						checkLines(t, name, devices(o), Text(r, devices(o)))
					}
				}
			}
		}
	}
}

// monoSGR are the escapes a page without color may carry: bold and
// reverse video, and their resets.
var monoSGR = regexp.MustCompile(`^\x1b\[(?:0|1|7|22|27)?(?:;(?:0|1|7|22|27))*m$`)

func checkLines(t *testing.T, name string, o Options, out string) {
	t.Helper()
	if !o.Color {
		for _, e := range sgr.FindAllString(out, -1) {
			if !monoSGR.MatchString(e) {
				t.Errorf("%s %+v: a color without color: %q", name, o, e)
				break
			}
		}
	}
	if strings.Contains(sgr.ReplaceAllString(out, ""), "\x1b") {
		t.Errorf("%s %+v: an escape that is not SGR", name, o)
	}
	for i, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		l = sgr.ReplaceAllString(l, "")
		if width(l) > o.Width-1 {
			t.Errorf("%s %+v line %d is %d wide:\n%s", name, o, i+1, width(l), l)
		}
		if strings.HasSuffix(l, " ") {
			t.Errorf("%s %+v line %d ends in a space: %q", name, o, i+1, l)
		}
		if o.ASCII {
			for _, b := range []byte(l) {
				if b >= 0x80 {
					t.Errorf("%s %+v line %d is not ASCII: %q", name, o, i+1, l)
					break
				}
			}
		}
	}
}

// TestPageFits checks that Page.Width is the widest line as it is drawn,
// the body's or the header's own, and that the header ends there, at every
// width, in both views of DEVICES, both modes of the matrix, and every
// period.
func TestPageFits(t *testing.T) {
	reports := map[string]Report{"team": loadReport(t, "team"), "single": loadReport(t, "single")}
	for name, r := range reports {
		for w := 80; w <= 160; w++ {
			for _, status := range []bool{false, true} {
				for _, share := range []bool{false, true} {
					for _, per := range Periods {
						o := Options{Width: w, Loc: sampleZone, DeviceStatus: status, Share: share, Period: per}
						p := Render(r, o)
						body := 0
						for _, l := range p.Body {
							body = max(body, width(sgr.ReplaceAllString(l, "")))
						}
						// The header on its own is its sides two columns apart.
						h := sgr.ReplaceAllString(p.Header, "")
						i := strings.Index(h, "  ● collected")
						if i < 0 {
							t.Errorf("%s %+v: the header lost its health: %q", name, o, h)
							continue
						}
						gap := i + 2 - len(strings.TrimRight(h[:i], " "))
						own := width(h) - gap + 2
						if p.Width != max(body, own) {
							t.Errorf("%s %+v: Width %d, the widest body line %d, the header's own width %d", name, o, p.Width, body, own)
						}
						if width(h) != p.Width {
							t.Errorf("%s %+v: the header ends at %d, not %d", name, o, width(h), p.Width)
						}
						if len(p.Body) < 2 || p.Body[0] != "" || p.Body[1] == "" || p.Body[len(p.Body)-1] == "" {
							t.Errorf("%s %+v: the body does not start with one blank line, or ends with one", name, o)
						}
					}
				}
			}
		}
	}
}

func ExampleText() {
	r := Report{GeneratedAt: time.Date(2026, 9, 23, 14, 38, 0, 0, time.UTC)}
	r.Collector.DeviceLabel = "mbp-anna"
	r.Collector.Version = "dev"
	fmt.Print(plainText(r, Options{Loc: time.UTC}))
	// Output:
	// ai-usage · mbp-anna  ● never collected  ● no relay  ● dev build
	//
	// SUBSCRIPTIONS  0
	//   no subscription has been used on this team's devices yet
}
