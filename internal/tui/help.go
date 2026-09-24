package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/view"
)

// entry is a line of the help: what to press or look for, and what it means.
type entry struct{ what, means string }

// helpLines are the help panel: every key, and the legend of the marks and
// the forecast states the static report ends with.
func (m Model) helpLines() []string {
	g := func(utf8, ascii string) string {
		if m.opts.ASCII {
			return ascii
		}
		return utf8
	}
	keys := []entry{
		{g("↑ ↓  j k", "up down  j k"), "scroll the page, as the mouse wheel does"},
		{"PgUp PgDn Space", "a screen at a time"},
		{"g G", "the top or the bottom"},
		{g("← →  h l", "left right  h l"), "scroll the matrix, as Shift and the wheel do"},
		{"p", "the next period: today, 7d, 30d, 90d"},
		{"1 7 3 9", "today, 7d, 30d, or 90d"},
		{"%", "tokens or share in the matrix"},
	}
	if m.cfg.Refresh != nil {
		keys = append(keys, entry{"r", "collect now; the header shows a spinner until it is done"})
	}
	keys = append(keys, entry{"?", "this help; Esc closes it"}, entry{"q  Esc  Ctrl-C", "quit"})
	marks := []entry{
		{g("━ ─", "= -"), "what a window has used, and what is left of it"},
		{g("┃", "|"), "even use by now; past it, faster than the window allows"},
		{g("╋", "+"), "the same tick inside the used part"},
		{g("┈", "."), "a window with no reading"},
		{"?", "not known: how full a window is, or a device's tokens"},
		{g("≥", ">="), "at least: a total with a part that is not known"},
		{g("—", "-"), "no forecast: no reading, or too early in the window"},
		{g("●", "*"), "this device, or an account logged in on it"},
		{g("×", "x"), "a device that fails"},
		{"~", "a silent device, or a reading older than 6 hours"},
		{g("↓", "v"), "a device on an older release"},
		{g("·", "."), "nothing"},
		{g("‹ ›", "[ ]"), "the chosen option"},
	}
	states := []entry{
		{view.StateOut, "the window is at 100%"},
		{view.StateOver, "100% or more: it runs out before it resets"},
		{view.StateTight, "85% to 99%"},
		{view.StateOK, "50% to 84%"},
		{view.StateUnder, "below 50%: room for more work"},
	}

	col := 0
	for _, es := range [][]entry{keys, marks, states} {
		for _, e := range es {
			col = max(col, ansi.StringWidth(e.what))
		}
	}
	col += 3
	pad := func(s string) string { return s + strings.Repeat(" ", max(1, col-ansi.StringWidth(s))) }

	lines := []string{"", m.style("KEYS", styleTitle)}
	for _, e := range keys {
		lines = append(lines, "  "+m.style(pad(e.what), styleKey)+e.means)
	}
	lines = append(lines, "", m.style("MARKS", styleTitle))
	for _, e := range marks {
		lines = append(lines, "  "+pad(e.what)+e.means)
	}
	lines = append(lines, "", m.style("STATES", styleTitle)+"  "+m.style("how full a window will be at its reset, at its pace so far", styleAction))
	for _, e := range states {
		what := pad(e.what)
		if m.opts.Color {
			what = lipgloss.NewStyle().Bold(true).Foreground(m.theme.State(e.what)).Render(e.what) + strings.Repeat(" ", col-len(e.what))
		}
		lines = append(lines, "  "+what+e.means)
	}
	return lines
}
