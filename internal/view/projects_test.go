package view

import (
	"strings"
	"testing"
)

// TestLegend ends the static page with one line of legend from 120 columns
// on, with every mark on screen, and with as few lines as fit below that.
// With the at least mark too, which only a collector older than v0.2.0
// brings, it is one line from 133 columns on.
func TestLegend(t *testing.T) {
	for _, c := range []struct {
		width, lines int
		atLeast      bool
	}{{80, 2, false}, {100, 2, false}, {119, 2, false}, {120, 1, false}, {160, 1, false},
		{80, 2, true}, {131, 2, true}, {133, 1, true}} {
		for _, ascii := range []bool{false, true} {
			p := newPage(&Report{}, Options{Width: c.width, ASCII: ascii})
			for _, k := range []string{"used", "left", "tick", "tickIn", "dotted", "stale", "silent", "unknown", "unread",
				"dash", "here", "this", "fail", "old", "none", "chosen"} {
				p.mark(k)
			}
			if c.atLeast {
				p.mark("atLeast")
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
	// line of them.
	if l := Render(loadReport(t, "team"), Options{Width: 120, Loc: sampleZone}).Legend; strings.Contains(l, "\n") {
		t.Errorf("the team's legend at 120 is more than one line:\n%s", l)
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
