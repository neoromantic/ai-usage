package view

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// fixture loads a report from testdata.
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

func TestGolden(t *testing.T) {
	team, single := loadReport(t, "team"), loadReport(t, "single")
	for _, c := range []struct {
		name string
		out  func() string
	}{
		{"team-status-80", func() string { return StatusText(team, teamDir, Options{Width: 80, Loc: time.UTC}) }},
		{"single-status-80", func() string {
			return StatusText(single, "/Users/sam/Library/Application Support/ai-usage", Options{Width: 80, Loc: time.UTC})
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

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestWidths draws every fixture's status at the widths around each tier
// and checks the line contract: nothing wider than T-1, no trailing
// spaces, only ASCII in ASCII mode, no escapes without color.
func TestWidths(t *testing.T) {
	reports := map[string]Report{"team": loadReport(t, "team"), "single": loadReport(t, "single")}
	for name, r := range reports {
		for _, w := range []int{80, 81, 99, 100, 119, 120, 159, 160} {
			for _, ascii := range []bool{false, true} {
				o := Options{Width: w, ASCII: ascii, Loc: time.UTC}
				checkLines(t, name, o, StatusText(r, teamDir, o))
			}
		}
	}
}

func checkLines(t *testing.T, name string, o Options, out string) {
	t.Helper()
	if strings.Contains(out, "\x1b") {
		t.Errorf("%s %+v: an escape without color", name, o)
	}
	for i, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
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
