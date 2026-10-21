package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/view"
)

// picked is the screen row of the picked line, and the line's text, which
// the row must show.
func picked(t *testing.T, m Model) (row int, text string) {
	t.Helper()
	if !m.picking {
		t.Fatal("nothing is picked")
	}
	from, to := m.inSight()
	if m.picked.Line < from || m.picked.Line >= to {
		t.Fatalf("pick at line %d out of sight %d to %d", m.picked.Line, from, to)
	}
	row = m.picked.Line - m.top + 1
	text = ansi.Strip(m.page.Body[m.picked.Line])
	if got := screen(m)[row]; !strings.HasPrefix(got, text) {
		t.Fatalf("row %d is %q, not the picked line %q", row, got, text)
	}
	return row, text
}

// TestPick: Enter picks the first row in sight, ↑ ↓ g G move the pick over
// every row the page offers, the page follows it, the pinned head of
// DEVICES included, and the row is drawn in reverse video.
func TestPick(t *testing.T) {
	r := bigTeam(t)
	for _, size := range []struct{ w, h int }{{80, 12}, {120, 24}} {
		m := model(t, size.w, size.h, Config{Report: r, Render: view.Render, Options: view.Options{Color: true, Dark: true}})
		if m.picking || !strings.Contains(bar(m), "↵ pick") && size.w > 80 {
			t.Fatalf("%v: picking %v before enter, bar %q", size, m.picking, bar(m))
		}
		m = keys(t, m, "enter")
		if m.picked != m.page.Picks[0] {
			t.Fatalf("%v: enter picked %+v, want the first %+v", size, m.picked, m.page.Picks[0])
		}
		if got := bar(m); !strings.HasPrefix(got, " ↑↓ pick · ↵ open · esc back") {
			t.Fatalf("%v: picking bar %q", size, got)
		}
		row, _ := picked(t, m)
		if raw := strings.Split(m.View().Content, "\n")[row]; !strings.Contains(raw, "\x1b[7") && !strings.Contains(raw, ";7m") {
			t.Fatalf("%v: the picked row is not in reverse video: %q", size, raw)
		}
		for i := 1; i < len(m.page.Picks); i++ {
			m = keys(t, m, "j")
			if m.picked != m.page.Picks[i] {
				t.Fatalf("%v: j %d picked %+v, want %+v", size, i, m.picked, m.page.Picks[i])
			}
			picked(t, m)
		}
		// Past the last, j stays; G and g go to the last and the first.
		last := m.picked
		if m = keys(t, m, "j"); m.picked != last {
			t.Fatalf("%v: j past the last picked %+v", size, m.picked)
		}
		for i := len(m.page.Picks) - 2; i >= 0; i-- {
			m = keys(t, m, "k")
			if m.picked != m.page.Picks[i] {
				t.Fatalf("%v: k picked %+v, want %+v", size, m.picked, m.page.Picks[i])
			}
			picked(t, m)
		}
		if m = keys(t, m, "G"); m.picked != last || m.top != m.maxTop() && m.picked.Line >= m.top+m.bodyHeight() {
			t.Fatalf("%v: G picked %+v at top %d", size, m.picked, m.top)
		}
		if m = keys(t, m, "g"); m.picked != m.page.Picks[0] {
			t.Fatalf("%v: g picked %+v", size, m.picked)
		}
		// A screen down keeps a row in sight picked.
		m = keys(t, m, "pgdown", "pgdown")
		picked(t, m)
		// Esc leaves picking; Esc again quits.
		m = keys(t, m, "esc")
		if m.picking || strings.Contains(bar(m), "esc back") {
			t.Fatalf("%v: esc left the pick %v, bar %q", size, m.picking, bar(m))
		}
		if _, cmd := m.Update(press(t, "esc")); !quits(cmd) {
			t.Fatalf("%v: esc on the page did not quit", size)
		}
	}
}

// TestOpen: Enter opens the picked row's detail in the page's place, from
// its top, with its own key bar; the period redraws it; Esc and Backspace
// go back to the page where it was, the row still picked.
func TestOpen(t *testing.T) {
	m := model(t, 120, 30, Config{Report: teamReport(t), Render: view.Render, Refresh: refresher()})
	m = keys(t, m, "enter")
	// The first device row of DEVICES.
	for m.picked.Device == "" || m.picked.Line < m.page.DevicesHead[1] {
		m = keys(t, m, "j")
	}
	top, at := m.top, m.picked
	_, text := picked(t, m)
	m = keys(t, m, "enter")
	if m.open == nil || *m.open != at || m.top != 0 {
		t.Fatalf("enter opened %+v at top %d", m.open, m.top)
	}
	name := strings.Fields(strings.TrimLeft(text, "●×~↓ "))[0]
	rows := screen(m)
	if !strings.HasPrefix(rows[2], "DEVICE  "+name) || !strings.Contains(strings.Join(rows, "\n"), "ACCOUNTS") {
		t.Fatalf("detail of %s:\n%s", name, strings.Join(rows, "\n"))
	}
	if got := bar(m); got != " esc back · p period ‹7d› · r refresh · ? help · q quit" {
		t.Fatalf("detail bar %q", got)
	}
	// The page's keys for the matrix do nothing here; the period does.
	m = keys(t, m, "%", "right", "s", "j", "3")
	if m.opts.Share || m.opts.MatrixScroll != 0 || m.opts.DeviceStatus || m.open == nil {
		t.Fatalf("matrix keys in a detail: %+v", m.opts)
	}
	if !strings.Contains(strings.Join(screen(m), "\n"), "by 30d UTC") {
		t.Fatalf("30d detail:\n%s", strings.Join(screen(m), "\n"))
	}
	m = keys(t, m, "7")
	for _, back := range []string{"esc", "backspace"} {
		next, cmd := m.Update(press(t, back))
		b := next.(Model)
		if quits(cmd) || b.open != nil || !b.picking || !b.picked.Same(at) || b.top != top {
			t.Fatalf("%s from a detail: quits %v, open %v, picked %+v at top %d, was %+v at %d", back, quits(cmd), b.open, b.picked, b.top, at, top)
		}
		picked(t, b)
	}
	// Backspace on the page goes back from the pick and does not quit.
	m = keys(t, m, "esc")
	next, cmd := m.Update(press(t, "backspace"))
	if quits(cmd) || next.(Model).picking {
		t.Fatal("backspace on the page quit or kept the pick")
	}
}

// TestPickFollows: a pick stays on its account or device when the period
// reorders the rows or the view changes, and a detail whose row left the
// report goes back to the page.
func TestPickFollows(t *testing.T) {
	r := teamReport(t)
	load := r
	m := model(t, 120, 40, Config{Report: r, Render: view.Render, Load: func(time.Time) (view.Report, error) { return load, nil }})
	m = keys(t, m, "enter")
	for m.picked.Device == "" || m.picked.Line < m.page.DevicesHead[1] {
		m = keys(t, m, "j")
	}
	m = keys(t, m, "j", "j", "j")
	at := m.picked
	for _, k := range []string{"1", "3", "s", "9", "s", "%"} {
		m = keys(t, m, k)
		if !m.picked.Same(at) {
			t.Fatalf("after %s the pick moved from %+v to %+v", k, at, m.picked)
		}
		picked(t, m)
	}
	m = keys(t, m, "enter")
	// A reload that drops the device closes its detail.
	load.Team.Devices = slices.DeleteFunc(slices.Clone(r.Team.Devices), func(d view.TeamDevice) bool { return d.Device == at.Device })
	load.Team.Matrix.Rows = slices.DeleteFunc(slices.Clone(r.Team.Matrix.Rows), func(x view.Row) bool { return x.DeviceID == at.Device })
	m = update(t, m, loadedMsg{report: load})
	if m.open != nil || !m.picking || m.picked.Same(at) {
		t.Fatalf("detail of a device gone: open %v, picked %+v", m.open, m.picked)
	}
	picked(t, m)
}

// TestClick: a click picks a row, and a click on the picked row opens it;
// a click on a line that opens nothing does nothing.
func TestClick(t *testing.T) {
	m := model(t, 120, 40, Config{Report: teamReport(t), Render: view.Render})
	click := func(m Model, row int) Model {
		return update(t, m, tea.MouseClickMsg{X: 5, Y: row, Button: tea.MouseLeft})
	}
	m = click(m, 1)
	if m.picking {
		t.Fatal("a click on the blank line under the header picked it")
	}
	k := m.page.Picks[2]
	row := k.Line - m.top + 1
	m = click(m, row)
	if !m.picking || m.picked != k || m.open != nil {
		t.Fatalf("click picked %+v, open %v", m.picked, m.open)
	}
	m = click(m, row)
	if m.open == nil || *m.open != k {
		t.Fatalf("second click opened %v", m.open)
	}
	// Clicks in a detail open nothing more.
	if m = click(m, row); m.open == nil || *m.open != k {
		t.Fatalf("click in a detail: %v", m.open)
	}
}

// TestPickSolo: on a single device, the rows to pick are its accounts, and
// a page with nothing to pick has no pick.
func TestPickSolo(t *testing.T) {
	m := model(t, 120, 40, Config{Report: soloReport(t), Render: view.Render})
	m = keys(t, m, "enter", "G", "enter")
	if m.open == nil || m.open.Provider == "" {
		t.Fatalf("solo: opened %+v", m.open)
	}
	if !strings.Contains(strings.Join(screen(m), "\n"), "WINDOWS") {
		t.Fatalf("solo account detail:\n%s", strings.Join(screen(m), "\n"))
	}
	m = model(t, 120, 40, Config{})
	if m = keys(t, m, "enter"); m.picking || strings.Contains(bar(m), "pick") {
		t.Fatalf("fake page with no rows: picking %v, bar %q", m.picking, bar(m))
	}
}

// soloReport is the view's fixture of a single device.
func soloReport(t *testing.T) view.Report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "view", "testdata", "single.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r view.Report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
