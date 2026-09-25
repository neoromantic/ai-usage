package view

import (
	"strconv"
	"strings"
	"testing"
)

// TestMatrixMore ends the providers' heading with the count of the columns
// off the right edge, whole, at every width and scroll: not those scrolled
// off the left, and none when the last column shows.
func TestMatrixMore(t *testing.T) {
	r := loadReport(t, "team")
	for w := 80; w <= 160; w += 4 {
		for _, share := range []bool{false, true} {
			for scroll := 0; scroll < 8; scroll++ {
				o := Options{Width: w, Loc: sampleZone, Share: share, Interactive: true, MatrixScroll: scroll}
				p := Render(r, o)
				heads := pageSection(p, "DEVICES")[1]
				right := p.MatrixColumns - min(scroll, p.MatrixColumns-1) - p.MatrixShown
				switch {
				case right > 0 && !strings.HasSuffix(heads, "  +"+strconv.Itoa(right)+" more"):
					t.Errorf("width %d, share %v, scroll %d: %d off the right, heading %q", w, share, scroll, right, heads)
				case right == 0 && strings.Contains(heads, "more"):
					t.Errorf("width %d, share %v, scroll %d: none off the right, heading %q", w, share, scroll, heads)
				}
			}
		}
	}
	// Scrolled to the last column, as far as the interactive view goes.
	p := Render(r, Options{Width: 80, Loc: sampleZone, Interactive: true, MatrixScroll: 3})
	if heads := pageSection(p, "DEVICES")[1]; p.MatrixShown != 5 || strings.Contains(heads, "more") {
		t.Errorf("scrolled to the end, shown %d, heading %q", p.MatrixShown, heads)
	}
	// Share has the same columns, and room for the count.
	p = Render(r, Options{Width: 80, Loc: sampleZone, Share: true})
	if heads := pageSection(p, "DEVICES")[1]; !strings.HasSuffix(heads, "  +"+strconv.Itoa(p.MatrixColumns-p.MatrixShown)+" more") {
		t.Errorf("share at 80, shown %d of %d, heading %q", p.MatrixShown, p.MatrixColumns, heads)
	}
}
