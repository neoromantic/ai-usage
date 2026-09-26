package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/view"
)

// bigTeam is the team fixture with 12 more Hermes bots, bot-j to bot-u, on
// lee's subscription as bot-i is, each a little less busy than the one
// before: 25 devices, more rows than a screen holds.
func bigTeam(t *testing.T) view.Report {
	t.Helper()
	r := teamReport(t)
	m := &r.Team.Matrix
	var row view.Row
	for _, x := range m.Rows {
		if x.Device == "bot-i" {
			row = x
		}
	}
	var dev view.TeamDevice
	for _, d := range r.Team.Devices {
		if d.Label == "bot-i" {
			dev = d
		}
	}
	lee := -1
	for k, c := range m.Columns {
		if c.Label == "lee@corp.test" {
			lee = k
		}
	}
	if row.Device == "" || dev.Label == "" || lee < 0 {
		t.Fatal("the team fixture has no bot-i on lee's subscription")
	}
	for i := range 12 {
		week := int64(1_900_000 - i*150_000)
		u := view.Usage{Today: week / 7, Week: week, Month: 4 * week, Quarter: 11 * week}
		name, id := fmt.Sprintf("bot-%c", 'j'+i), fmt.Sprintf("d-b%02xc1d2e3f4a5b6c7d8e9f%x0", 9+i, (9+i)%16)
		cells := append([]view.Cell(nil), row.Cells...)
		cells[lee] = view.Cell{Usage: u, WindowTokens: week / 7}
		m.Rows = append(m.Rows, view.Row{Device: name, DeviceID: id, Cells: cells, Usage: u})
		c := &m.Columns[lee].Usage
		c.Today, c.Week, c.Month, c.Quarter = c.Today+u.Today, c.Week+u.Week, c.Month+u.Month, c.Quarter+u.Quarter
		d := dev
		d.Device, d.Label, d.Usage = id, name, u
		d.Sources = append([]view.Source(nil), dev.Sources...)
		r.Team.Devices = append(r.Team.Devices, d)
	}
	return r
}

// sight checks the page's rows on the screen: the page from its top, with
// the head of DEVICES in the first three instead once its title has
// scrolled off, while TOTAL is still under them. It returns the lines of
// the page in sight, in order, and whether the head is pinned.
func sight(t *testing.T, m Model) (in []int, pinned bool) {
	t.Helper()
	rows := screen(m)[1 : 1+m.bodyHeight()]
	body := ansiStrip(m.page.Body)
	h, end := m.page.DevicesHead, m.page.DevicesEnd
	pinned = m.top > h[0] && m.top+3 < end
	for i, row := range rows {
		line, want := m.top+i, ""
		switch {
		case pinned && i < 3:
			line, want = -1, body[h[0]+i]
		case line < len(body):
			want = body[line]
		default:
			line = -1
		}
		if row != want {
			t.Fatalf("top %d, pinned %v: row %d is %q, want %q\n%s", m.top, pinned, i, row, want, strings.Join(rows, "\n"))
		}
		if line >= 0 {
			in = append(in, line)
		}
	}
	return in, pinned
}

// TestPinnedHead: scrolled down through a team of more devices than fit,
// the page keeps the head of DEVICES at its top, in both views, while the
// rows scroll under it: from the step its title scrolls off, with the
// first device row right under it the step before, to the step TOTAL
// comes up under it, or to the end of a page too short for that. Every
// line of the page comes into sight, row by row, and a screen at a time,
// which skips none under the head.
func TestPinnedHead(t *testing.T) {
	r := bigTeam(t)
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 24}, {80, 12}, {120, 12}} {
		for _, status := range []bool{false, true} {
			at := fmt.Sprintf("%dx%d, status %v", size.w, size.h, status)
			m := model(t, size.w, size.h, Config{Report: r, Render: view.Render, Options: view.Options{DeviceStatus: status}})
			h, end := m.page.DevicesHead, m.page.DevicesEnd
			// At 24 rows the page is too short under DEVICES to scroll TOTAL
			// up under the head; at 12 it is not.
			if short := m.maxTop() < end-3; short != (size.h == 24) {
				t.Fatalf("%s: the page's last top is %d, DEVICES ends at %d", at, m.maxTop(), end)
			}
			title := "DEVICES × SUBSCRIPTIONS  25 · "
			if status {
				title = "DEVICES  25 · "
			}
			if h[1]-h[0] != 3 || end-h[1]-1 != 25 || !strings.HasPrefix(ansi.Strip(m.page.Body[h[0]]), title) {
				t.Fatalf("%s: head %v, end %d:\n%s", at, h, end, strings.Join(ansiStrip(m.page.Body), "\n"))
			}
			first := ansi.Strip(m.page.Body[h[1]])
			seen := map[int]bool{}
			pins := 0
			for {
				in, pinned := sight(t, m)
				for _, l := range in {
					seen[l] = true
				}
				if pinned {
					pins++
				}
				rows := screen(m)
				switch m.top {
				case h[0]:
					// The head at the top on its own, the first device row
					// right under it.
					if rows[4] != first {
						t.Errorf("%s, top %d: %q under the head, not the first device row %q", at, m.top, rows[4], first)
					}
				case end - 4:
					if !pinned || !strings.HasPrefix(rows[4], "  TOTAL ") {
						t.Errorf("%s, top %d: TOTAL is not under the pinned head:\n%s", at, m.top, strings.Join(rows, "\n"))
					}
				case end - 3:
					// TOTAL came up under the head: the head is let go, and
					// TOTAL goes on up with the rows over it.
					if pinned || !strings.HasPrefix(rows[3], "  TOTAL ") {
						t.Errorf("%s, top %d: the head is still pinned:\n%s", at, m.top, strings.Join(rows, "\n"))
					}
				}
				if m.top == m.maxTop() {
					break
				}
				m = keys(t, m, "j")
			}
			if want := min(end-4, m.maxTop()) - h[0]; pins != want {
				t.Errorf("%s: pinned at %d tops, want %d", at, pins, want)
			}
			for l := range m.page.Body {
				if !seen[l] {
					t.Errorf("%s: line %d never came into sight: %q", at, l, ansi.Strip(m.page.Body[l]))
				}
			}
			// Back up row by row, then a screen at a time down and up: a
			// new screen starts no further down than the line under the
			// last one in sight, and ends no further up than the line over
			// the first.
			for m.top > 0 {
				m = keys(t, m, "k")
				sight(t, m)
			}
			for _, k := range []string{"pgdown", "pgup"} {
				for {
					before, _ := sight(t, m)
					top := m.top
					m = keys(t, m, k)
					if m.top == top {
						break
					}
					after, _ := sight(t, m)
					if k == "pgdown" && (m.top < top || after[0] > before[len(before)-1]+1) ||
						k == "pgup" && (m.top > top || after[len(after)-1] < before[0]-1) {
						t.Errorf("%s, %s from %d to %d: %v in sight, then %v", at, k, top, m.top, before, after)
					}
				}
				if k == "pgdown" && m.top != m.maxTop() || k == "pgup" && m.top != 0 {
					t.Errorf("%s: %s stopped at %d", at, k, m.top)
				}
			}
		}
	}
}

// TestPinnedHeadSideways: the pinned head is the page's own, so the names
// of the subscriptions scroll sideways with the matrix under them, and s
// pins the status view's head in its place. The wheel keeps it pinned, and
// the help covers it.
func TestPinnedHeadSideways(t *testing.T) {
	m := model(t, 80, 24, Config{Report: bigTeam(t), Render: view.Render})
	h := m.page.DevicesHead
	for m.top < h[0]+8 {
		m = keys(t, m, "j")
	}
	names := screen(m)[3]
	if _, pinned := sight(t, m); !pinned || names != ansi.Strip(m.page.Body[h[0]+2]) {
		t.Fatalf("not pinned at %d: %q", m.top, names)
	}
	m = keys(t, m, "right", "right")
	if _, pinned := sight(t, m); !pinned || m.opts.MatrixScroll != 2 || screen(m)[3] == names {
		t.Fatalf("scrolled sideways by %d, pinned %v, names %q, were %q", m.opts.MatrixScroll, pinned, screen(m)[3], names)
	}
	m = update(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if _, pinned := sight(t, m); !pinned || m.top != h[0]+11 {
		t.Fatalf("wheel down: top %d, pinned %v", m.top, pinned)
	}
	m = keys(t, m, "s")
	if _, pinned := sight(t, m); !pinned || !strings.HasPrefix(screen(m)[1], "DEVICES  25 · ") || !strings.HasPrefix(screen(m)[3], "  DEVICE ") {
		t.Fatalf("status view, pinned %v:\n%s", pinned, strings.Join(screen(m), "\n"))
	}
	m = keys(t, m, "?")
	if rows := screen(m); rows[1] != "" || rows[2] != "KEYS" {
		t.Fatalf("the help under a pinned head:\n%s", strings.Join(rows, "\n"))
	}
	if m = keys(t, m, "esc"); !strings.HasPrefix(screen(m)[1], "DEVICES  25 · ") {
		t.Fatalf("the head is not back after the help: %q", screen(m)[1])
	}
}

func TestMatrixScroll(t *testing.T) {
	// 10 of 12 columns fit at 120.
	m := model(t, 120, 30, Config{})
	for _, c := range []struct {
		key  string
		want int
	}{{"left", 0}, {"right", 1}, {"l", 2}, {"right", 2}, {"h", 1}, {"left", 0}, {"left", 0}} {
		if m = keys(t, m, c.key); m.opts.MatrixScroll != c.want {
			t.Fatalf("%s: scroll %d, want %d", c.key, m.opts.MatrixScroll, c.want)
		}
	}
	wheel := func(b tea.MouseButton, mod tea.KeyMod) tea.MouseWheelMsg {
		return tea.MouseWheelMsg{Button: b, Mod: mod}
	}
	m = update(t, m, wheel(tea.MouseWheelDown, tea.ModShift), wheel(tea.MouseWheelRight, 0))
	if m.opts.MatrixScroll != 2 || m.top != 0 {
		t.Fatalf("shift+wheel and wheel right: scroll %d, top %d", m.opts.MatrixScroll, m.top)
	}
	m = update(t, m, wheel(tea.MouseWheelUp, tea.ModShift))
	if m.opts.MatrixScroll != 1 {
		t.Fatalf("shift+wheel up: scroll %d", m.opts.MatrixScroll)
	}

	// A wider terminal scrolls back as far as the columns on the right
	// still all show.
	m = keys(t, m, "right")
	if m = update(t, m, tea.WindowSizeMsg{Width: 130, Height: 30}); m.opts.MatrixScroll != 1 {
		t.Fatalf("130 wide: scroll %d, want 1", m.opts.MatrixScroll)
	}
	if m = update(t, m, tea.WindowSizeMsg{Width: 160, Height: 30}); m.opts.MatrixScroll != 0 || strings.Contains(bar(m), "matrix") {
		t.Fatalf("160 wide: scroll %d, bar %q", m.opts.MatrixScroll, bar(m))
	}
	if m = keys(t, m, "right"); m.opts.MatrixScroll != 0 {
		t.Fatalf("a matrix that fits scrolled to %d", m.opts.MatrixScroll)
	}
}

func TestScroll(t *testing.T) {
	// 17 body rows of 100 lines: the last top is 83.
	m := model(t, 100, 20, Config{})
	for _, c := range []struct {
		key  string
		want int
	}{
		{"k", 0}, {"up", 0}, {"j", 1}, {"down", 2}, {"k", 1},
		{"G", 83}, {"j", 83}, {"space", 83}, {"g", 0},
		{"space", 17}, {"pgdown", 34}, {"pgup", 17}, {"pgup", 0}, {"pgup", 0},
	} {
		if m = keys(t, m, c.key); m.top != c.want {
			t.Fatalf("%s: top %d, want %d", c.key, m.top, c.want)
		}
	}
	m = update(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.top != 6 || screen(m)[1] != "line 6" {
		t.Fatalf("wheel down twice: top %d, first row %q", m.top, screen(m)[1])
	}
	if m = update(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp}); m.top != 3 {
		t.Fatalf("wheel up: top %d", m.top)
	}

	// On a resize the page keeps its place, and the bottom stays the bottom.
	m = update(t, keys(t, m, "G"), tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.top != 73 || screen(m)[27] != "line 99" {
		t.Fatalf("taller at the bottom: top %d, last row %q", m.top, screen(m)[27])
	}
	m = update(t, keys(t, m, "g", "j", "j"), tea.WindowSizeMsg{Width: 120, Height: 25})
	if m.top != 2 {
		t.Fatalf("resized in the middle: top %d", m.top)
	}
	// A page that shrinks on reload keeps the top in bounds.
	m.cfg.Render = fakeRender(10, 12)
	if m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 25}); m.top != 0 {
		t.Fatalf("short page: top %d", m.top)
	}
}
