package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/view"
)

func TestHelp(t *testing.T) {
	m := model(t, 100, 20, Config{Refresh: refresher()})
	m = keys(t, m, "?")
	rows := screen(m)
	text := strings.Join(rows, "\n")
	if !m.help || !strings.Contains(text, "KEYS") || !strings.Contains(text, "PgUp PgDn Space") {
		t.Fatalf("help:\n%s", text)
	}
	if got := bar(m); got != " ↑↓ scroll · esc close · q quit" {
		t.Fatalf("help key bar %q", got)
	}
	all := strings.Join(ansiStrip(m.helpLines()), "\n")
	for _, want := range []string{"MARKS", "━ ─", "┈", "not known", "—", "no forecast", "●", "×", "↓", "‹ ›", "STATES", "over", "under", "collect now"} {
		if !strings.Contains(all, want) {
			t.Fatalf("help lacks %q:\n%s", want, all)
		}
	}
	for _, l := range m.helpLines() {
		if ansi.StringWidth(l) > 80 {
			t.Fatalf("help line wider than 80: %q", ansi.Strip(l))
		}
	}
	// The help scrolls; the page's keys wait until it closes.
	m = keys(t, m, "j", "p", "right", "%")
	if m.helpTop != 1 || m.top != 0 || m.opts.Period != view.Week || m.opts.MatrixScroll != 0 || m.opts.Share {
		t.Fatalf("keys in help: helpTop %d top %d period %s scroll %d share %v", m.helpTop, m.top, m.opts.Period, m.opts.MatrixScroll, m.opts.Share)
	}
	next, cmd := m.Update(press(t, "esc"))
	m = next.(Model)
	if m.help || quits(cmd) || screen(m)[1] != "line 0" {
		t.Fatalf("esc did not just close the help: help %v", m.help)
	}
	// Opening it again starts at its top; ? closes it too.
	if m = keys(t, m, "?"); m.helpTop != 0 {
		t.Fatalf("help reopened at %d", m.helpTop)
	}
	if m = keys(t, m, "?"); m.help {
		t.Fatal("? did not close the help")
	}

	// Without collection, `r` is not in the help; in ASCII, no glyph is.
	m = keys(t, model(t, 100, 60, Config{Options: view.Options{ASCII: true}}), "?")
	all = strings.Join(ansiStrip(m.helpLines()), "\n")
	if strings.Contains(all, "collect now") {
		t.Fatal("help lists r without a refresh")
	}
	for _, r := range all {
		if r >= 0x80 {
			t.Fatalf("ascii help has %q:\n%s", r, all)
		}
	}
	if got := bar(m); got != " esc close . q quit" {
		t.Fatalf("help that fits: %q", got)
	}
}
