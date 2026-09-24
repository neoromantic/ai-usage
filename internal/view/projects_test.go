package view

import (
	"strings"
	"testing"
)

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
