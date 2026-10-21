package view

import (
	"strings"
	"testing"
)

// detailText is a detail page as the interactive view shows it, its header
// and its body, one line each.
func detailText(t *testing.T, r Report, o Options, k Pick) string {
	t.Helper()
	o.Interactive = true
	p, ok := Detail(r, o, k)
	if !ok {
		t.Fatalf("no detail of %+v", k)
	}
	return p.Header + "\n" + strings.Join(p.Body, "\n") + "\n"
}

// The fixture's rows the detail tests open.
var (
	annClaude = Pick{Provider: "claude", Label: "ann@acme.dev"}
	kimMac    = Pick{Device: "d-9b1d3f5a7c9e1b3d5f7a9c1e"}
)

// TestPicks: every row the page offers to open is on its line, in either
// view of DEVICES and on a single device, and the static page is the same
// with them.
func TestPicks(t *testing.T) {
	team, single := loadReport(t, "team"), loadReport(t, "single")
	for _, c := range []struct {
		name string
		r    Report
		o    Options
	}{
		{"team", team, Options{Width: 120, Loc: sampleZone, Interactive: true}},
		{"team status", team, devices(Options{Width: 80, Loc: sampleZone, Interactive: true})},
		{"team ascii", team, Options{Width: 80, Loc: sampleZone, Interactive: true, ASCII: true}},
		{"single", single, Options{Width: 120, Loc: sampleZone, Interactive: true}},
	} {
		p := Render(c.r, c.o)
		if len(p.Picks) == 0 {
			t.Fatalf("%s: no picks", c.name)
		}
		last := -1
		devs, accts := 0, 0
		for _, k := range p.Picks {
			if k.Line <= last {
				t.Fatalf("%s: picks out of order at line %d", c.name, k.Line)
			}
			last = k.Line
			line := sgr.ReplaceAllString(p.Body[k.Line], "")
			var name string
			switch {
			case k.Device != "":
				devs++
				d := (&page{r: &c.r}).device(k.Device, "")
				if d == nil {
					t.Fatalf("%s: pick of no device %q", c.name, k.Device)
				}
				name = d.Label
			default:
				accts++
				a := accountOf(&c.r.Team, k.Provider, k.Label)
				if a == nil {
					t.Fatalf("%s: pick of no account %+v", c.name, k)
				}
				name = shortID(a.Label)
				if !strings.Contains(line, name) {
					name = a.Name
				}
			}
			if c.o.ASCII {
				name = (&page{o: c.o}).txt(name)
			}
			if !strings.Contains(line, prefix(name, 12)) {
				t.Errorf("%s: line %d does not name %q: %q", c.name, k.Line, name, line)
			}
			if _, ok := Detail(c.r, c.o, k); !ok {
				t.Errorf("%s: %+v opens nothing", c.name, k)
			}
		}
		if accts == 0 || len(c.r.Team.Devices) > 1 && devs < len(c.r.Team.Matrix.Rows) {
			t.Errorf("%s: %d accounts and %d devices to pick", c.name, accts, devs)
		}
	}
	if _, ok := Detail(team, Options{}, Pick{Device: "d-gone"}); ok {
		t.Fatal("a device not in the report has a detail")
	}
	if _, ok := Detail(team, Options{}, Pick{Provider: "claude", Label: "gone@acme.dev"}); ok {
		t.Fatal("an account not in the report has a detail")
	}
}

// TestDetailLines: a detail keeps the page's line contract at every width,
// in every period, and in ASCII, for every row it opens.
func TestDetailLines(t *testing.T) {
	for _, name := range []string{"team", "single"} {
		r := loadReport(t, name)
		for _, k := range Render(r, Options{Width: 120, Interactive: true}).Picks {
			for _, w := range []int{80, 99, 120, 160} {
				for _, ascii := range []bool{false, true} {
					for _, color := range []bool{false, true} {
						o := Options{Width: w, ASCII: ascii, Color: color, Loc: sampleZone, Period: Periods[w%4]}
						checkLines(t, name, o, detailText(t, r, o, k))
					}
				}
			}
		}
	}
}

// TestDetailContent: an account's detail has its windows, its history, the
// devices that spent it, and this device's projects on it; a device's has
// its collector and the accounts it spent.
func TestDetailContent(t *testing.T) {
	team := loadReport(t, "team")
	o := Options{Width: 120, Loc: sampleZone}
	// This device's projects on the account, which the fixture leaves out.
	team.Providers[0].Accounts[0].Projects = team.Projects[:2]
	acct := detailText(t, team, o, annClaude)
	for _, want := range []string{"CLAUDE  ann@acme.dev  ● logged in here", "WINDOWS", "UTILIZATION", "DEVICES", "PROJECTS", "TOTAL"} {
		if !strings.Contains(sgr.ReplaceAllString(acct, ""), want) {
			t.Errorf("account detail lacks %q:\n%s", want, acct)
		}
	}
	dev := sgr.ReplaceAllString(detailText(t, team, o, kimMac), "")
	for _, want := range []string{"DEVICE  Mac.localdomain", "USER", "kim", "VERSION", "ERROR", "ACCOUNTS", "TOTAL"} {
		if !strings.Contains(dev, want) {
			t.Errorf("device detail lacks %q:\n%s", want, dev)
		}
	}
	// Another device's detail has no PROJECTS: the report holds only this
	// device's.
	if strings.Contains(dev, "PROJECTS") {
		t.Errorf("another device's detail has projects:\n%s", dev)
	}
	// The chosen period orders the tables and titles them.
	month := o
	month.Period = Month
	if got := sgr.ReplaceAllString(detailText(t, team, month, kimMac), ""); !strings.Contains(got, "by 30d UTC") {
		t.Errorf("30d detail:\n%s", got)
	}
}
