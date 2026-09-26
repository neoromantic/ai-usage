package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/view"
)

// fakeRender draws a page of n body lines and a matrix of cols columns, each
// 10 cells wide after a 20-cell device column, so the width decides how many
// fit. The header shows what the options asked for.
func fakeRender(n, cols int) func(view.Report, view.Options) view.Page {
	return func(r view.Report, o view.Options) view.Page {
		body := make([]string, n)
		for i := range body {
			body[i] = fmt.Sprintf("line %d", i)
		}
		shown := min(cols-o.MatrixScroll, max(1, (o.Width-20)/10))
		if cols == 0 {
			shown = 0
		}
		return view.Page{
			Header:        fmt.Sprintf("%s %s share=%v busy=%q interactive=%v width=%d", r.Collector.DeviceLabel, o.Period, o.Share, o.Busy, o.Interactive, o.Width),
			Body:          body,
			MatrixColumns: cols,
			MatrixShown:   shown,
		}
	}
}

func report(label string) view.Report {
	return view.Report{GeneratedAt: time.Date(2026, 9, 23, 17, 38, 0, 0, time.UTC), Collector: view.Collector{DeviceLabel: label}}
}

// model is a view of a 100-line page with a 12-column matrix, sized w×h,
// whose timers never fire in a test.
func model(t *testing.T, w, h int, c Config) Model {
	t.Helper()
	if c.Render == nil {
		c.Render = fakeRender(100, 12)
	}
	if c.Report.GeneratedAt.IsZero() {
		c.Report = report("leebook")
	}
	m := New(c)
	m.pollEvery, m.spinEvery = time.Hour, time.Hour
	return update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
}

func update(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

// press is the key a terminal sends for s, as Bubble Tea names it.
func press(t *testing.T, s string) tea.KeyPressMsg {
	t.Helper()
	var k tea.KeyPressMsg
	switch s {
	case "up":
		k = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		k = tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		k = tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		k = tea.KeyPressMsg{Code: tea.KeyRight}
	case "pgup":
		k = tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		k = tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "esc":
		k = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		k = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+c":
		k = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		k = tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}
	if k.String() != s {
		t.Fatalf("press(%q) is %q", s, k.String())
	}
	return k
}

func keys(t *testing.T, m Model, ks ...string) Model {
	t.Helper()
	for _, k := range ks {
		m = update(t, m, press(t, k))
	}
	return m
}

// screen is the view's rows without styles.
func screen(m Model) []string {
	return strings.Split(ansi.Strip(m.View().Content), "\n")
}

func bar(m Model) string {
	rows := screen(m)
	return rows[len(rows)-1]
}

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func refresher() func(context.Context) error { return func(context.Context) error { return nil } }

func TestFrame(t *testing.T) {
	m := model(t, 100, 20, Config{Refresh: refresher()})
	v := m.View()
	if !v.AltScreen || v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("alt screen %v, mouse %v", v.AltScreen, v.MouseMode)
	}
	rows := screen(m)
	if len(rows) != 20 {
		t.Fatalf("%d rows, want 20:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(rows[0], "leebook 7d share=false") || !strings.Contains(rows[0], "interactive=true width=100") {
		t.Fatalf("header %q", rows[0])
	}
	if rows[1] != "line 0" || rows[17] != "line 16" {
		t.Fatalf("body starts %q, ends %q", rows[1], rows[17])
	}
	if rows[18] != strings.Repeat("─", 100) {
		t.Fatalf("rule %q", rows[18])
	}
	if !strings.HasSuffix(rows[19], "? help · q quit") {
		t.Fatalf("key bar %q", rows[19])
	}

	// Before the terminal says its size there is nothing to draw.
	if c := New(Config{Render: fakeRender(3, 0)}).View(); c.Content != "" || !c.AltScreen {
		t.Fatalf("unsized view %+v", c)
	}
	// A fixed layout width is kept on any terminal.
	m = model(t, 100, 20, Config{Options: view.Options{Width: 120}})
	if !strings.Contains(screen(m)[0], "width=120") {
		t.Fatalf("fixed width header %q", screen(m)[0])
	}
}

// teamReport is the view's team fixture: 13 devices on 8 subscriptions.
func teamReport(t *testing.T) view.Report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "view", "testdata", "team.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r view.Report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNarrowAndShortTerminals(t *testing.T) {
	wide := func(view.Report, view.Options) view.Page {
		return view.Page{Header: strings.Repeat("h", 90), Body: []string{"\x1b[1m" + strings.Repeat("x", 90) + "\x1b[0m"}}
	}
	m := model(t, 40, 10, Config{Render: wide})
	for i, row := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(row); w > 40 {
			t.Fatalf("row %d is %d wide", i, w)
		}
	}
	for h := 1; h <= 3; h++ {
		rows := screen(model(t, 40, h, Config{Render: wide}))
		if len(rows) != h || !strings.HasSuffix(rows[h-1], "q quit") {
			t.Fatalf("height %d: %q", h, rows)
		}
	}
}

func ansiStrip(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = ansi.Strip(l)
	}
	return out
}

func TestQuit(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c", "esc"} {
		if _, cmd := model(t, 100, 20, Config{}).Update(press(t, k)); !quits(cmd) {
			t.Fatalf("%s did not quit", k)
		}
	}
	// In the help, q still quits.
	if _, cmd := keys(t, model(t, 100, 20, Config{}), "?").Update(press(t, "q")); !quits(cmd) {
		t.Fatal("q in the help did not quit")
	}
	for _, k := range []string{"x", "j", "p"} {
		if _, cmd := model(t, 100, 20, Config{}).Update(press(t, k)); quits(cmd) {
			t.Fatalf("%s quit", k)
		}
	}
}

// TestBackground: the terminal's answer decides the background, whatever
// the view started on.
func TestBackground(t *testing.T) {
	m := model(t, 120, 30, Config{Options: view.Options{Dark: true}})
	if m = update(t, m, tea.BackgroundColorMsg{Color: color.White}); m.opts.Dark {
		t.Fatal("a white background is dark")
	}
	if m = update(t, m, tea.BackgroundColorMsg{Color: color.Black}); !m.opts.Dark {
		t.Fatal("a black background is light")
	}
}
