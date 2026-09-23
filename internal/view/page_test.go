package view

import (
	"strings"
	"testing"
	"time"
)

// pageSection is the plain lines of the body section whose title starts
// with title.
func pageSection(p Page, title string) []string {
	var out []string
	for _, l := range p.Body {
		l = sgr.ReplaceAllString(l, "")
		switch {
		case strings.HasPrefix(l, title):
			out = append(out, l)
		case len(out) > 0 && l == "":
			return out
		case len(out) > 0:
			out = append(out, l)
		}
	}
	return out
}

// moreAttention is the team fixture with a silent device and an unused
// window as well, 8 lines of ATTENTION.
func moreAttention(t *testing.T) Report {
	r := loadReport(t, "team")
	since := time.Date(2026, 9, 21, 11, 2, 0, 0, time.UTC)
	resets := time.Date(2026, 9, 29, 13, 30, 0, 0, time.UTC)
	pct := 33.0
	r.Attention = append(r.Attention,
		Attention{Kind: AttentionSilent, Devices: []string{"bot-e"}, At: &since},
		Attention{Kind: AttentionUnder, Provider: "codex", Account: "lee@corp.test", Name: "lee", ResetsAt: &resets, Percent: &pct})
	return r
}

func TestPageAttentionIsCut(t *testing.T) {
	r := moreAttention(t)
	static := pageSection(Render(r, Options{Width: 160, Loc: sampleZone}), "ATTENTION")
	if len(static) != 1+attentionLines+1 || strings.TrimSpace(static[len(static)-1]) != "+2 more" {
		t.Errorf("static ATTENTION is not cut at %d lines:\n%s", attentionLines, strings.Join(static, "\n"))
	}
	p := Render(r, Options{Width: 160, Loc: sampleZone, Interactive: true})
	all := pageSection(p, "ATTENTION")
	if len(all) != 1+8 {
		t.Fatalf("the interactive view does not show every line:\n%s", strings.Join(all, "\n"))
	}
	for _, want := range []string{
		" SILENT  bot-e                no report since Mon 14:02, 2d 3h ago",
		" UNDER   codex lee@corp.test  leaves ~67% unused at this week's pace, resets Tue 16:30, in 5d 22h",
	} {
		found := false
		for _, l := range all {
			found = found || l == want
		}
		if !found {
			t.Errorf("no line %q in\n%s", want, strings.Join(all, "\n"))
		}
	}
	if p.Legend != "" {
		t.Errorf("the interactive view has a legend: %q", p.Legend)
	}
}

func TestPageMatrixScroll(t *testing.T) {
	r := loadReport(t, "team")
	p := Render(r, Options{Width: 80, Loc: sampleZone})
	if p.MatrixColumns != 8 || p.MatrixShown != 5 {
		t.Errorf("columns %d, shown %d; want 8 and 5", p.MatrixColumns, p.MatrixShown)
	}
	p = Render(r, Options{Width: 80, Loc: sampleZone, Interactive: true, MatrixScroll: 5})
	m := pageSection(p, "DEVICES")
	if p.MatrixShown != 3 || !strings.Contains(m[1], "CODEX") || !strings.Contains(m[1], "NO QUOTA") ||
		!strings.Contains(m[2], "unknown") || strings.Contains(m[2], "lee") {
		t.Errorf("scrolled by 5, shown %d:\n%s", p.MatrixShown, strings.Join(m, "\n"))
	}
	if !strings.HasPrefix(m[3], "  srv1 ") {
		t.Errorf("the device column is not kept: %q", m[3])
	}
	// Past the end, the last column still shows.
	p = Render(r, Options{Width: 80, Loc: sampleZone, MatrixScroll: 99})
	if p.MatrixShown != 1 {
		t.Errorf("scrolled past the end, shown %d", p.MatrixShown)
	}
	if single := Render(loadReport(t, "single"), Options{Width: 80}); single.MatrixColumns != 0 {
		t.Errorf("one device has a matrix of %d columns", single.MatrixColumns)
	}
}

func TestPageHeat(t *testing.T) {
	r := loadReport(t, "team")
	row := func(p Page, device string) string {
		matrix := false
		for _, l := range p.Body {
			plain := sgr.ReplaceAllString(l, "")
			matrix = matrix || strings.HasPrefix(plain, "DEVICES")
			if matrix && strings.Contains(plain[:min(len(plain), 24)], " "+device+" ") {
				return l
			}
		}
		t.Fatalf("no row of %s", device)
		return ""
	}
	// In color the top step is bold: the largest cell on the page.
	color := Render(r, Options{Width: 120, Loc: sampleZone, Color: true})
	if l := row(color, "srv1"); !strings.Contains(l, "\x1b[1;38;2;") || !strings.Contains(l, "m240\x1b[m") {
		t.Errorf("the largest cell is not bold in color: %q", l)
	}
	// Without color the largest of each column is bold, and nothing else.
	mono := Render(r, Options{Width: 120, Loc: sampleZone})
	if l := row(mono, "annbook"); !strings.Contains(l, "\x1b[1m19\x1b[m") || !strings.Contains(l, "\x1b[1m180\x1b[m") {
		t.Errorf("the largest of a column is not bold: %q", l)
	}
	if l := row(mono, "bot-a"); strings.Contains(l, "\x1b[1m") {
		t.Errorf("a cell that is no column's largest is bold: %q", l)
	}
	for _, v := range []struct {
		v, top float64
		step   int
	}{{240, 240, 5}, {76, 240, 5}, {75, 240, 4}, {24, 240, 3}, {2, 240, 1}, {0.4, 240, 1}} {
		if got := heatStep(v.v, v.top); got != v.step {
			t.Errorf("heatStep(%v, %v) = %d, want %d", v.v, v.top, got, v.step)
		}
	}
}

func TestPageHeaderBusy(t *testing.T) {
	r := loadReport(t, "team")
	h := sgr.ReplaceAllString(Render(r, Options{Width: 120, Loc: sampleZone, Busy: "⠋"}).Header, "")
	if !strings.Contains(h, "⠋ collecting") || strings.Contains(h, "collected 7m") {
		t.Errorf("busy header %q", h)
	}
}
