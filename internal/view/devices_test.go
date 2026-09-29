package view

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMatrixRest ends the providers' heading with the count of the columns
// folded into REST, those scrolled off the left and those off the right
// edge, at every width and scroll, and has no REST when every column shows.
func TestMatrixRest(t *testing.T) {
	r := loadReport(t, "team")
	for w := 80; w <= 160; w += 4 {
		for _, share := range []bool{false, true} {
			for scroll := range 8 {
				o := Options{Width: w, Loc: sampleZone, Share: share, Interactive: true, MatrixScroll: scroll}
				p := Render(r, o)
				sec := pageSection(p, "DEVICES")
				heads, names := sec[1], sec[2]
				folded := p.MatrixColumns - p.MatrixShown
				switch {
				case folded > 0 && (!strings.HasSuffix(heads, "  +"+strconv.Itoa(folded)) || !strings.Contains(names, " rest ")):
					t.Errorf("width %d, share %v, scroll %d: %d folded, heading %q, names %q", w, share, scroll, folded, heads, names)
				case folded == 0 && strings.Contains(names, " rest "):
					t.Errorf("width %d, share %v, scroll %d: none folded, names %q", w, share, scroll, names)
				}
			}
		}
	}
}

// gridTeam is a team of devices whose burn on every subscription is whole
// millions, so the page's numbers add up as the data does: bot-0 to bot-4
// on claude, codex and grok, where grok burns the most.
func gridTeam() Report {
	r := Report{GeneratedAt: now, Collector: Collector{Version: "v1.2.3", DeviceLabel: "bot-0"}}
	r.Collector.Health = health(r.Collector)
	cols := []Column{
		{Provider: "claude", Label: "ann@acme.dev", Name: "ann", State: StateOK},
		{Provider: "claude", Label: "kim@corp.test", Name: "kim", State: StateOK},
		{Provider: "codex", Label: "lee@corp.test", Name: "lee", State: StateOK},
		{Provider: "codex", Label: "sam@mail.test", Name: "sam", State: StateOK},
		{Provider: "codex", Label: "bob@mail.test", Name: "bob", State: StateOK},
		{Provider: "grok", Label: "grok-one", Name: "gone", State: StateOK},
		{Provider: "grok", Label: "grok-two", Name: "gtwo", State: StateOK},
		{Provider: "hermes", Name: "hermes", NoQuota: true, State: StateUnknown},
	}
	m := func(n int64) Usage {
		return Usage{Today: n * 1e6, Week: 7 * n * 1e6, Month: 30 * n * 1e6, Quarter: 90 * n * 1e6}
	}
	for d := range 5 {
		id := "d-bot-" + strconv.Itoa(d)
		r.Team.Devices = append(r.Team.Devices, TeamDevice{Device: id, Label: "bot-" + strconv.Itoa(d), CollectorVersion: "v1.2.3", CollectedAt: now, This: d == 0})
		row := Row{Device: "bot-" + strconv.Itoa(d), DeviceID: id, BurnEstimated: d == 3}
		for c := range cols {
			n := int64((d*7+c*3)%5) + 1
			if cols[c].Provider == "grok" {
				n *= 20
			}
			row.Cells = append(row.Cells, Cell{Burn: m(n)})
			row.Burn = row.Burn.add(m(n))
			cols[c].Burn = cols[c].Burn.add(m(n))
		}
		r.Team.Matrix.Rows = append(r.Team.Matrix.Rows, row)
	}
	r.Team.Matrix.Columns = cols
	return r
}

// TestMatrixRowsAddUp: at 80 and 120 columns, in every period, a row's
// shown cells and REST add up to its TOTAL, and so do the columns' totals,
// and the grok group, which burns the most, shows its first column first.
func TestMatrixRowsAddUp(t *testing.T) {
	r := gridTeam()
	for _, w := range []int{80, 120} {
		for _, per := range Periods {
			p := Render(r, Options{Width: w, Loc: time.UTC, Period: per})
			sec := pageSection(p, "DEVICES")
			if !strings.HasPrefix(strings.TrimSpace(sec[1]), "GROK") || !strings.HasPrefix(strings.TrimSpace(sec[2]), "g") {
				t.Errorf("width %d %s: the biggest group is not first:\n%s", w, per, strings.Join(sec, "\n"))
			}
			if w == 80 && !strings.Contains(sec[2], " rest ") {
				t.Errorf("width 80 %s: nothing folds:\n%s", per, strings.Join(sec, "\n"))
			}
			for _, l := range sec[3:] {
				f := strings.Fields(strings.ReplaceAll(l, "~", ""))
				var nums []int64
				for _, s := range f {
					if s == "·" {
						nums = append(nums, 0)
					} else if n, err := strconv.ParseInt(s, 10, 64); err == nil {
						nums = append(nums, n)
					}
				}
				var sum int64
				for _, n := range nums[:len(nums)-1] {
					sum += n
				}
				if sum != nums[len(nums)-1] {
					t.Errorf("width %d %s: %q adds up to %d", w, per, l, sum)
				}
			}
		}
	}
}

// TestMatrixEstimated marks an estimated row's TOTAL, and the team's, with
// ~, and TOTAL is wide enough for it; the legend says why where it fits.
func TestMatrixEstimated(t *testing.T) {
	r := gridTeam()
	reason := "bot-3 runs v0.3.4: it updates itself within 15 minutes"
	r.Team.Estimated, r.Team.EstimatedReason = true, &reason
	for _, c := range []struct {
		w      int
		legend string
	}{
		{80, "~ estimated: " + reason},
		{120, "~ estimated: " + reason},
	} {
		p := Render(r, Options{Width: c.w, Loc: time.UTC})
		sec := pageSection(p, "DEVICES")
		for _, l := range sec[3:] {
			est := strings.HasPrefix(strings.TrimSpace(l), "bot-3") || strings.HasPrefix(strings.TrimSpace(l), "TOTAL")
			f := strings.Fields(l)
			if got := strings.HasPrefix(f[len(f)-1], "~"); got != est {
				t.Errorf("width %d: %q marked %v", c.w, l, got)
			}
		}
		if !strings.Contains(p.Legend, c.legend) {
			t.Errorf("width %d: legend %q", c.w, p.Legend)
		}
	}
	long := strings.Repeat("x", 90)
	r.Team.EstimatedReason = &long
	if p := Render(r, Options{Width: 80, Loc: time.UTC}); !strings.Contains(p.Legend, "~ estimated  ") && !strings.HasSuffix(p.Legend, "~ estimated") {
		t.Errorf("a reason too long for the legend: %q", p.Legend)
	}
}

// TestStatusOrderIsTheMatrix: the status view lists the devices in the
// matrix's order, in every period.
func TestStatusOrderIsTheMatrix(t *testing.T) {
	r := loadReport(t, "team")
	for _, per := range []Period{Week, Month, Today, Quarter} {
		names := func(status bool) []string {
			sec := pageSection(Render(r, Options{Width: 120, Loc: sampleZone, Period: per, DeviceStatus: status}), "DEVICES")
			var out []string
			for _, l := range sec[3 : len(sec)-1] {
				out = append(out, strings.Fields(l[2:])[0])
			}
			return out
		}
		if m, s := names(false), names(true); strings.Join(m, " ") != strings.Join(s, " ") {
			t.Errorf("%s: matrix %v, status %v", per, m, s)
		}
	}
}

// TestGoldensFit80: no line of the pages drawn at 80 columns is wider.
func TestGoldensFit80(t *testing.T) {
	for _, name := range []string{"team-80", "team-devices-80", "team-80-ascii", "team-devices-80-ascii"} {
		b, err := os.ReadFile(filepath.Join("testdata", name+".golden"))
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(string(b), "\n") {
			if width(l) > 80 {
				t.Errorf("%s line %d is %d wide: %q", name, i+1, width(l), l)
			}
		}
	}
}
