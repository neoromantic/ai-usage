package view

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
		// The interactive view's details: an account, and a device.
		{"detail-account-120", func() string { return detailText(t, team, page(120), annClaude) }},
		{"detail-account-80-ascii", func() string {
			o := page(80)
			o.ASCII = true
			return detailText(t, team, o, annClaude)
		}},
		{"detail-device-80", func() string { return detailText(t, team, page(80), kimMac) }},
		{"detail-device-120-color", func() string {
			o := page(120)
			o.Color, o.Dark = true, true
			return detailText(t, team, o, kimMac)
		}},
		{"detail-single-120", func() string {
			return detailText(t, single, page(120), Pick{Provider: "claude", Label: single.Team.Providers[0].Accounts[0].Label})
		}},
		// The matrix in dollars, and its REST at 80 columns.
		{"team-cost-80-ascii", func() string {
			o := page(80)
			o.Cost, o.ASCII = true, true
			return plainText(team, o)
		}},
		{"team-cost-120-color", func() string {
			o := page(120)
			o.Color, o.Dark, o.Cost = true, true, true
			return Text(team, o)
		}},
		// What ai-usage cost prints.
		{"cost-120", func() string { return sgr.ReplaceAllString(CostText(team, page(120)), "") }},
		{"cost-80-ascii", func() string {
			o := page(80)
			o.ASCII = true
			return sgr.ReplaceAllString(CostText(team, o), "")
		}},
		{"cost-single-100", func() string { return sgr.ReplaceAllString(CostText(single, page(100)), "") }},
		// A subscription no device has any more, nobody can say is still
		// paid; one idle with no price; and a device's dollars.
		{"detail-cost-gone-120", func() string { return detailText(t, team, page(120), Pick{Provider: "claude", Label: "old@acme.dev"}) }},
		{"detail-cost-idle-100", func() string {
			return detailText(t, single, page(100), Pick{Provider: "codex", Label: "work@example.org"})
		}},
		{"detail-cost-device-120", func() string {
			return detailText(t, team, page(120), Pick{Device: "d-2f8a6c4e0b9d7f1a3c5e7b9d"})
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

// TestWidths draws each fixture's page at every width from 80 to 160, each
// a width where some view's layout changes or the one before it: a column
// drops, a form of a line or the header changes, the legend wraps. It draws
// each view that draws apart: both views of DEVICES, the matrix in tokens,
// shares, and dollars, each period, the static page and the interactive
// view, scrolled sideways, collecting, with every project. ASCII and color
// change no layout but their own, so the periods of each view take the four
// ways between them. On each page it checks the line contract of
// checkLines; that Page.Width is the widest line as it is drawn, the body's
// or the header's own, and that the header ends there; that the body
// starts with one blank line and ends with none; and that DevicesHead and
// DevicesEnd hold DEVICES' head and rows to TOTAL. Of the status view, on
// troubled devices and long names too, it checks that the columns keep
// their order, that the chosen period and 90d always show, that a wider
// page never shows less, and that no line is wider than the page before it
// is cut. Of the matrix, it checks that the providers' heading ends with
// the count of the columns folded into REST, scrolled off the left and off
// the right edge, and that there is no REST when every column shows. The
// status card keeps the line contract at the tiers.
func TestWidths(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"team", "single"} {
		r := loadReport(t, name)
		for _, w := range []int{80, 81, 99, 100, 119, 120, 159, 160} {
			for _, ascii := range []bool{false, true} {
				o := Options{Width: w, ASCII: ascii, Loc: time.UTC}
				checkLines(t, name, o, StatusText(r, teamDir, o))
			}
		}
	}
	// Share and the status view draw only the team's DEVICES, so a single
	// device takes the periods alone; and the status view drops its columns
	// at the same widths in every period but today, so the troubled and long
	// fixtures take today and one other.
	var teamViews, singleViews []Options
	for i, per := range Periods {
		ascii, color := i%2 == 1, i >= 2
		live := Options{Period: per, Interactive: true, Busy: "|", AllProjects: true}
		single := Options{Period: per}
		if i%2 == 1 {
			single = live
		}
		single.ASCII, single.Color = color, color
		singleViews = append(singleViews, single)
		live.DeviceStatus, live.Share, live.Cost, live.ASCII, live.Color = i == 2, i == 1, i == 3, !ascii, color
		teamViews = append(teamViews,
			Options{Period: per, ASCII: ascii, Color: color},
			Options{Period: per, Share: true, Cost: per == Week, ASCII: !ascii, Color: !color},
			Options{Period: per, DeviceStatus: true, ASCII: ascii, Color: !color},
			live)
	}
	statusViews := []Options{{Period: Today, DeviceStatus: true}, {Period: Month, DeviceStatus: true, ASCII: true, Color: true}}
	order := []string{"DEVICE", "VERSION", "SEEN", "VIA", "TODAY", "7D", "30D", "90D", "NOTE"}
	for _, c := range []struct {
		name  string
		load  func(t *testing.T) Report
		views []Options
	}{
		{"team", func(t *testing.T) Report { return loadReport(t, "team") }, teamViews},
		{"single", func(t *testing.T) Report { return loadReport(t, "single") }, singleViews},
		{"troubled", troubled, statusViews},
		{"long", longNames, statusViews},
	} {
		for i, o := range c.views {
			t.Run(fmt.Sprintf("%s/%d", c.name, i), func(t *testing.T) {
				t.Parallel()
				r := c.load(t)
				var last []string
				for w := minWidth; w <= maxWidth; w++ {
					o.Width, o.Loc = w, sampleZone
					if o.Interactive {
						o.MatrixScroll = (w + i) % 8
					}
					name := fmt.Sprintf("%s %+v", c.name, o)
					p := Render(r, o)
					checkLines(t, c.name, o, strings.Join(append(append([]string{p.Header}, p.Body...), p.Legend), "\n"))

					body := 0
					for _, l := range p.Body {
						body = max(body, width(sgr.ReplaceAllString(l, "")))
					}
					// The header on its own is its sides two columns apart.
					h, health := sgr.ReplaceAllString(p.Header, ""), "● collected"
					switch {
					case o.Busy != "":
						health = o.Busy + " collecting"
					case o.ASCII:
						health = "* collected"
					}
					if at := strings.Index(h, "  "+health); at < 0 {
						t.Errorf("%s: the header lost its health: %q", name, h)
					} else if own := width(h) - (at + 2 - len(strings.TrimRight(h[:at], " "))) + 2; p.Width != max(body, own) {
						t.Errorf("%s: Width %d, the widest body line %d, the header's own width %d", name, p.Width, body, own)
					}
					if width(h) != p.Width {
						t.Errorf("%s: the header ends at %d, not %d", name, width(h), p.Width)
					}
					if len(p.Body) < 2 || p.Body[0] != "" || p.Body[1] == "" || p.Body[len(p.Body)-1] == "" {
						t.Errorf("%s: the body does not start with one blank line, or ends with one", name)
					}
					if !p.DeviceViews {
						if p.DevicesHead != [2]int{} || p.DevicesEnd != 0 || p.MatrixColumns != 0 {
							t.Errorf("%s: one device has a head %v, an end %d, and %d matrix columns", name, p.DevicesHead, p.DevicesEnd, p.MatrixColumns)
						}
						continue
					}

					sec := pageSection(p, "DEVICES")
					switch hd, end := p.DevicesHead, p.DevicesEnd; {
					case o.Cost:
						// The $ mode has a row of what nobody used under the devices.
					case hd[1]-hd[0] != devicesHead || end-hd[0] != len(sec) || end-hd[1]-1 != len(r.Team.Devices) || sgr.ReplaceAllString(p.Body[hd[0]], "") != sec[0]:
						t.Errorf("%s: head %v, end %d, for a section of %d lines:\n%s", name, hd, end, len(sec), strings.Join(sec, "\n"))
					case !strings.HasPrefix(sec[len(sec)-1], "  TOTAL "):
						t.Errorf("%s: the section does not end with TOTAL: %q", name, sec[len(sec)-1])
					}

					if !o.DeviceStatus {
						folded := p.MatrixColumns - p.MatrixShown
						switch heads, names := sec[1], sec[2]; {
						case folded > 0 && (!strings.HasSuffix(heads, "  +"+strconv.Itoa(folded)) || !strings.Contains(names, " rest ")):
							t.Errorf("%s: %d folded, heading %q, names %q", name, folded, heads, names)
						case folded == 0 && strings.Contains(names, " rest "):
							t.Errorf("%s: none folded, names %q", name, names)
						}
						continue
					}
					pg := newPage(&r, o)
					for _, l := range pg.deviceStatus() {
						if l.width() > pg.w {
							t.Errorf("%s: a line %d wide before it is cut: %q", name, l.width(), l.String())
						}
					}
					heads := strings.Fields(sec[2])
					if !slices.IsSortedFunc(heads, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) }) {
						t.Errorf("%s: columns out of order: %v", name, heads)
					}
					for _, must := range []string{"DEVICE", "VERSION", "SEEN", strings.ToUpper(o.Period.String()), "90D"} {
						if !slices.Contains(heads, must) {
							t.Errorf("%s: no %s in %v", name, must, heads)
						}
					}
					for _, h := range last {
						if !slices.Contains(heads, h) {
							t.Errorf("%s: %s shows on a narrower page but not here: %v", name, h, heads)
						}
					}
					last = heads
				}
			})
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

func ExampleText() {
	r := Report{GeneratedAt: time.Date(2026, 9, 23, 14, 38, 0, 0, time.UTC)}
	r.Collector.DeviceLabel = "mbp-anna"
	r.Collector.Version = "dev"
	r.Collector.Health = health(r.Collector, r.GeneratedAt)
	fmt.Print(plainText(r, Options{Loc: time.UTC}))
	// Output:
	// ai-usage · mbp-anna  ● never collected  ● no relay  ● dev build
	//
	// SUBSCRIPTIONS  0
	//   no subscription has been used on this team's devices yet
}
