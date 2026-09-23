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
		{"team-80", func() string { return Text(team, Options{Width: 80, Loc: time.UTC}) }},
		{"team-120", func() string { return Text(team, Options{Width: 120, Loc: time.UTC}) }},
		{"team-80-ascii", func() string { return Text(team, Options{Width: 80, ASCII: true, Loc: time.UTC}) }},
		{"team-80-color", func() string { return Text(team, Options{Width: 80, Color: true, Loc: time.UTC}) }},
		{"team-devices-80", func() string { return Text(team, Options{Width: 80, Mode: Devices, Loc: time.UTC}) }},
		{"team-tokens-80", func() string { return Text(team, Options{Width: 80, Mode: Tokens, Loc: time.UTC}) }},
		{"team-projects-80", func() string { return Text(team, Options{Width: 80, Mode: Projects, Loc: time.UTC}) }},
		{"team-status-80", func() string { return StatusText(team, teamDir, Options{Width: 80, Loc: time.UTC}) }},
		{"single-80", func() string { return Text(single, Options{Width: 80, Loc: time.UTC}) }},
		{"single-120", func() string { return Text(single, Options{Width: 120, Loc: time.UTC}) }},
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

// TestWidths draws every fixture in every view at the widths around each
// tier and checks the line contract: nothing wider than T-1, no trailing
// spaces, only ASCII in ASCII mode, no escapes without color.
func TestWidths(t *testing.T) {
	reports := map[string]Report{"team": loadReport(t, "team"), "single": loadReport(t, "single")}
	for name, r := range reports {
		for _, mode := range []Mode{Default, Projects, Tokens, Devices, -1} {
			for _, w := range []int{80, 81, 99, 100, 119, 120, 159, 160} {
				for _, ascii := range []bool{false, true} {
					o := Options{Width: w, ASCII: ascii, Mode: mode, Loc: time.UTC}
					var out string
					if mode == -1 {
						out = StatusText(r, teamDir, o)
					} else {
						out = Text(r, o)
					}
					checkLines(t, name, o, out)
				}
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

// TestColorIsClosedOnEachLine checks every escape is reset on its line, and
// that the text under the color is the no-color text.
func TestColorIsClosedOnEachLine(t *testing.T) {
	for _, name := range []string{"team", "single"} {
		r := loadReport(t, name)
		for _, w := range []int{80, 120} {
			plainOut := Text(r, Options{Width: w, Loc: time.UTC})
			colored := Text(r, Options{Width: w, Color: true, Loc: time.UTC})
			if sgr.ReplaceAllString(colored, "") != plainOut {
				t.Errorf("%s at %d: color changes the text", name, w)
			}
			for i, l := range strings.Split(colored, "\n") {
				opens := strings.Count(l, "\x1b[") - strings.Count(l, "\x1b[0m")
				if opens != strings.Count(l, "\x1b[0m") {
					t.Errorf("%s at %d, line %d: %d escapes, %d resets: %q", name, w, i+1, opens, strings.Count(l, "\x1b[0m"), l)
				}
			}
		}
	}
}

// TestBorrowedIsGrayAndOldKeepsItsColor: a stale reading is still a floor
// until its window resets, so it keeps its level colors; a Hermes row that
// borrows Codex's reading is gray and leaves the pace note to Codex.
func TestBorrowedIsGrayAndOldKeepsItsColor(t *testing.T) {
	out := Text(loadReport(t, "single"), Options{Width: 80, Color: true, Loc: time.UTC})
	if strings.Contains(out, "\x1b[2;") {
		t.Errorf("faint escape in the output:\n%s", out)
	}
	var claude, hermes string
	for _, l := range strings.Split(out, "\n") {
		plainLine := sgr.ReplaceAllString(l, "")
		switch {
		case strings.HasPrefix(plainLine, "● sam@example.com") && claude == "":
			claude = l
		case strings.HasPrefix(plainLine, "● openai-codex"):
			hermes = l
		}
	}
	if !strings.Contains(claude, "\x1b[31m██████\x1b[0m") || !strings.Contains(claude, "\x1b[1;31m 100%") {
		t.Errorf("old critical reading lost its color: %q", claude)
	}
	if !strings.Contains(hermes, "\x1b[90m████▋\x1b[0m") || strings.Contains(hermes, "\x1b[35m") || strings.Contains(hermes, "\x1b[33m") {
		t.Errorf("borrowed reading is not gray: %q", hermes)
	}
	if n := strings.Count(out, "full in"); n != 1 {
		t.Errorf("%d pace notes, want only the Codex one", n)
	}
}

// TestPageEndsAtTheTablesOnOneDevice: with no USED BY or NOTE column to fill
// a wide terminal, the header and the rules end where THIS DEVICE does.
func TestPageEndsAtTheTablesOnOneDevice(t *testing.T) {
	for _, c := range []struct {
		name  string
		width int
		want  int
	}{{"single", 80, 79}, {"single", 100, 99}, {"single", 140, 107}, {"team", 140, 139}} {
		out := Text(loadReport(t, c.name), Options{Width: c.width, Loc: time.UTC})
		lines := strings.Split(out, "\n")
		if w := width(lines[0]); w != c.want {
			t.Errorf("%s at %d: header is %d wide, want %d", c.name, c.width, w, c.want)
		}
		for _, l := range lines {
			if strings.HasPrefix(l, "  claude ─") && width(l) != c.want {
				t.Errorf("%s at %d: rule is %d wide, want %d: %q", c.name, c.width, width(l), c.want, l)
			}
		}
	}
}
