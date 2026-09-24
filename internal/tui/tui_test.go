package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// immediate are the messages cmd and the commands it batches give at once; a
// timer, which waits an hour in these tests, gives none.
func immediate(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if b, ok := msg.(tea.BatchMsg); ok {
			var msgs []tea.Msg
			for _, c := range b {
				msgs = append(msgs, immediate(c)...)
			}
			return msgs
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(100 * time.Millisecond):
		return nil
	}
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

func TestKeyBar(t *testing.T) {
	const full = " ↑↓ scroll · ←→ matrix · p period ‹7d› · % share · r refresh · ? help · q quit"
	m := model(t, 120, 30, Config{Refresh: refresher()})
	if got := bar(m); got != full {
		t.Fatalf("wide key bar\n got %q\nwant %q", got, full)
	}
	// Only keys that do something now: a matrix that fits, a page that
	// fits, a view without collection.
	if got := bar(model(t, 160, 30, Config{Refresh: refresher()})); strings.Contains(got, "matrix") {
		t.Fatalf("matrix that fits: %q", got)
	}
	if got := bar(model(t, 120, 30, Config{Render: fakeRender(5, 12)})); got != " ←→ matrix · p period ‹7d› · % share · ? help · q quit" {
		t.Fatalf("short page, no refresh: %q", got)
	}
	if got := bar(model(t, 120, 30, Config{Render: fakeRender(100, 0)})); got != " ↑↓ scroll · p period ‹7d› · ? help · q quit" {
		t.Fatalf("no matrix: %q", got)
	}

	// A narrow terminal drops the least used keys first and keeps help and
	// quit last.
	for _, c := range []struct {
		width int
		want  string
	}{
		{78, full},
		{77, " ↑↓ scroll · ←→ matrix · p period ‹7d› · r refresh · ? help · q quit"},
		{67, " ↑↓ scroll · ←→ matrix · p period ‹7d› · ? help · q quit"},
		{55, " ↑↓ scroll · p period ‹7d› · ? help · q quit"},
		{43, " ↑↓ scroll · ? help · q quit"},
		{27, " ? help · q quit"},
		{10, " ? help · "},
	} {
		if got := bar(model(t, c.width, 30, Config{Refresh: refresher()})); got != c.want {
			t.Errorf("width %d\n got %q\nwant %q", c.width, got, c.want)
		}
	}
}

func TestPills(t *testing.T) {
	m := model(t, 120, 30, Config{})
	m = keys(t, m, "%")
	if got := bar(m); !strings.Contains(got, "p period ‹7d› · % ‹share› · ? help") {
		t.Fatalf("share on: %q", got)
	}

	m = model(t, 120, 30, Config{Options: view.Options{ASCII: true}})
	if got := bar(m); got != " j k scroll . h l matrix . p period [7d] . % share . ? help . q quit" {
		t.Fatalf("ascii: %q", got)
	}
	for _, r := range ansi.Strip(m.View().Content) {
		if r >= 0x80 {
			t.Fatalf("ascii view has %q", r)
		}
	}

	// In color, the chosen option is a pill in reverse accent, as wide as
	// its marks, and keys are bold.
	m = model(t, 120, 30, Config{Options: view.Options{Color: true, Dark: true}})
	raw := m.keyBar()
	if got := ansi.Strip(raw); !strings.Contains(got, "p period  7d  · % share") || ansi.StringWidth(got) != ansi.StringWidth(bar(model(t, 120, 30, Config{}))) {
		t.Fatalf("color key bar %q", got)
	}
	if pill := m.pill("7d"); !strings.Contains(pill, "7") || !strings.HasPrefix(pill, "\x1b[") || ansi.Strip(pill) != " 7d " {
		t.Fatalf("pill %q", pill)
	}
	if !strings.Contains(raw, "\x1b[1;") && !strings.Contains(raw, "\x1b[1m") {
		t.Fatalf("keys are not bold: %q", raw)
	}
}

func TestPeriods(t *testing.T) {
	m := model(t, 120, 30, Config{})
	for _, want := range []string{"30d", "90d", "today", "7d", "30d"} {
		m = keys(t, m, "p")
		if got := m.opts.Period.String(); got != want {
			t.Fatalf("p: %s, want %s", got, want)
		}
		if !strings.Contains(screen(m)[0], " "+want+" ") || !strings.Contains(bar(m), "‹"+want+"›") {
			t.Fatalf("period %s not drawn: %q / %q", want, screen(m)[0], bar(m))
		}
	}
	for k, want := range map[string]view.Period{"1": view.Today, "7": view.Week, "3": view.Month, "9": view.Quarter} {
		if got := keys(t, m, k).opts.Period; got != want {
			t.Fatalf("%s picks %s, want %s", k, got, want)
		}
	}
}

func TestShare(t *testing.T) {
	m := model(t, 120, 30, Config{})
	m = keys(t, m, "%")
	if !m.opts.Share || !strings.Contains(screen(m)[0], "share=true") {
		t.Fatalf("%% did not turn share on: %q", screen(m)[0])
	}
	if m = keys(t, m, "%"); m.opts.Share {
		t.Fatal("%% did not turn share off")
	}
	// Without a matrix, % does nothing and is not in the bar.
	m = model(t, 120, 30, Config{Render: fakeRender(100, 0)})
	if m = keys(t, m, "%"); m.opts.Share || strings.Contains(bar(m), "%") {
		t.Fatalf("share without a matrix: %v %q", m.opts.Share, bar(m))
	}
	// A matrix of NO QUOTA tokens alone has no column to share; % still
	// leaves share mode.
	noQuota := func(r view.Report, o view.Options) view.Page {
		if o.Share {
			return fakeRender(100, 0)(r, o)
		}
		return fakeRender(100, 1)(r, o)
	}
	m = keys(t, model(t, 120, 30, Config{Render: noQuota}), "%")
	if !m.opts.Share || !strings.Contains(bar(m), "% ‹share›") {
		t.Fatalf("share of NO QUOTA only: %v %q", m.opts.Share, bar(m))
	}
	if m = keys(t, m, "%"); m.opts.Share || !strings.Contains(bar(m), "% share") {
		t.Fatalf("%% did not leave share mode: %q", bar(m))
	}
}

// olderReport is a team with a device on a collector older than v0.2.0,
// whose tokens over 90 days alone are known, as the report's JSON has it.
const olderReport = `{"generated_at": "2026-09-23T17:38:00Z", "collector": {"device_label": "annbook"}, "team": {
  "devices": [
    {"device": "d-annbook", "label": "annbook", "this_device": true, "collector_version": "v0.2.0", "collected_at": "2026-09-23T17:30:00Z",
      "usage": {"today": 4000000, "7d": 8000000, "30d": 12000000, "90d": 12000000}},
    {"device": "d-macbook-old", "label": "MacBook-Old", "collector_version": "v0.1.4", "collected_at": "2026-09-23T17:20:00Z", "old": true,
      "usage": {"today": 0, "7d": 0, "30d": 0, "90d": 400000000, "unknown": ["today", "7d", "30d"]}}],
  "matrix": {
    "columns": [{"provider": "claude", "label": "ann@acme.dev", "name": "ann", "state": "over", "percent": 60,
      "usage": {"today": 4000000, "7d": 8000000, "30d": 12000000, "90d": 412000000, "unknown": ["today", "7d", "30d"]},
      "window_tokens": 8000000, "window_unknown": true}],
    "rows": [
      {"device": "annbook", "device_id": "d-annbook", "usage": {"today": 4000000, "7d": 8000000, "30d": 12000000, "90d": 12000000},
        "cells": [{"usage": {"today": 4000000, "7d": 8000000, "30d": 12000000, "90d": 12000000}, "window_tokens": 8000000, "share": null}]},
      {"device": "MacBook-Old", "device_id": "d-macbook-old", "usage": {"today": 0, "7d": 0, "30d": 0, "90d": 400000000, "unknown": ["today", "7d", "30d"]},
        "cells": [{"usage": {"today": 0, "7d": 0, "30d": 0, "90d": 400000000, "unknown": ["today", "7d", "30d"]}, "window_tokens": 0,
          "window_unknown": true, "share": null}]}]}}}`

// The period and share keys redraw a device on an older collector: its
// tokens over 90 days, and ? for every shorter period and for its share,
// with the totals that miss it at least what they show.
func TestOlderDevice(t *testing.T) {
	var r view.Report
	if err := json.Unmarshal([]byte(olderReport), &r); err != nil {
		t.Fatal(err)
	}
	// The tail of the first line that starts with the device's name: its
	// cell and its total.
	cells := func(m Model, device string) string {
		for _, l := range screen(m) {
			if f := strings.Fields(strings.TrimLeft(l, "●↓ ")); len(f) > 0 && f[0] == device {
				return strings.Join(f[1:], " ")
			}
		}
		t.Fatalf("no %s row:\n%s", device, strings.Join(screen(m), "\n"))
		return ""
	}
	m := model(t, 120, 40, Config{Report: r, Render: view.Render})
	check := func(what, old, total string) {
		t.Helper()
		if got := cells(m, "MacBook-Old"); got != old {
			t.Errorf("%s: MacBook-Old %q, want %q", what, got, old)
		}
		if got := cells(m, "TOTAL"); got != total {
			t.Errorf("%s: TOTAL %q, want %q", what, got, total)
		}
	}
	check("7d", "? ?", "≥8 ≥8")
	for _, c := range []struct{ key, old, total string }{
		{"p", "? ?", "≥12 ≥12"}, {"p", "400 400", "412 412"}, {"p", "? ?", "≥4 ≥4"}, {"p", "? ?", "≥8 ≥8"},
		{"9", "400 400", "412 412"}, {"1", "? ?", "≥4 ≥4"}, {"3", "? ?", "≥12 ≥12"}, {"7", "? ?", "≥8 ≥8"},
	} {
		m = keys(t, m, c.key)
		check(c.key+" "+m.opts.Period.String(), c.old, c.total)
	}
	// Its share is not known, nor is annbook's, but the window's is.
	m = keys(t, m, "%")
	check("share", "?", "60")
	if got := cells(m, "annbook"); got != "?" {
		t.Errorf("share: annbook %q", got)
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
	for _, want := range []string{"MARKS", "━ ─", "┈", "not known", "≥", "—", "no forecast", "●", "×", "↓", "‹ ›", "STATES", "over", "under", "collect now"} {
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

func TestRefresh(t *testing.T) {
	var loads atomic.Int32
	release := make(chan error, 1)
	m := model(t, 120, 30, Config{
		Refresh: func(ctx context.Context) error { return <-release },
		Load: func(now time.Time) (view.Report, error) {
			loads.Add(1)
			return report("collected"), nil
		},
	})
	next, cmd := m.Update(press(t, "r"))
	m = next.(Model)
	if !m.busy || !strings.Contains(screen(m)[0], `busy="⠋"`) || strings.Contains(bar(m), "refresh") {
		t.Fatalf("while collecting: header %q, bar %q", screen(m)[0], bar(m))
	}
	// A second r while one runs starts nothing.
	if _, again := m.Update(press(t, "r")); again != nil {
		t.Fatal("r started a second collection")
	}
	// The spinner turns with its own ticks only.
	m = update(t, m, spinMsg{m.gen})
	if !strings.Contains(screen(m)[0], `busy="⠙"`) {
		t.Fatalf("spinner did not turn: %q", screen(m)[0])
	}
	m = update(t, m, spinMsg{m.gen - 1})
	if m.frame != 1 {
		t.Fatalf("an old spinner turned this one: frame %d", m.frame)
	}

	release <- errors.New("claude: no answer in time\nand more")
	msgs := immediate(cmd)
	if len(msgs) != 1 {
		t.Fatalf("r gave %v", msgs)
	}
	if _, ok := msgs[0].(refreshedMsg); !ok {
		t.Fatalf("collection ended with %T", msgs[0])
	}
	next, cmd = m.Update(msgs[0])
	m = next.(Model)
	rows := screen(m)
	if m.busy || !strings.Contains(rows[0], `busy=""`) || rows[len(rows)-3] != " refresh failed: claude: no answer in time" {
		t.Fatalf("after a failed collection:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(bar(m), "r refresh") {
		t.Fatalf("r is back: %q", bar(m))
	}
	// It reloads what the collection saved.
	m = update(t, m, cmd())
	if loads.Load() != 1 || !strings.HasPrefix(screen(m)[0], "collected") {
		t.Fatalf("after the reload: %d loads, header %q", loads.Load(), screen(m)[0])
	}
	// The next collection clears the error.
	m = keys(t, m, "r")
	if m.statusText() != "" {
		t.Fatalf("status %q", m.statusText())
	}
}

func TestReload(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 17, 38, 0, 0, time.UTC)
	var asOf []time.Time
	fail := false
	m := model(t, 120, 30, Config{
		Watch: []string{state, filepath.Join(dir, "team-cache.json")},
		Now:   func() time.Time { return now },
		Load: func(at time.Time) (view.Report, error) {
			asOf = append(asOf, at)
			if fail {
				return view.Report{}, errors.New("state.json: permission denied")
			}
			return report(fmt.Sprintf("load%d", len(asOf))), nil
		},
	})
	poll := func() Model {
		t.Helper()
		next, cmd := m.Update(pollMsg(now))
		// The next poll waits; a load answers at once.
		return update(t, next.(Model), immediate(cmd)...)
	}

	now = now.Add(10 * time.Second)
	if m = poll(); len(asOf) != 0 {
		t.Fatal("reloaded with nothing new")
	}
	// A run saved new state.
	if err := os.WriteFile(state, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if m = poll(); len(asOf) != 1 || !strings.HasPrefix(screen(m)[0], "load1") {
		t.Fatalf("after a save: %d loads, header %q", len(asOf), screen(m)[0])
	}
	if m = poll(); len(asOf) != 1 {
		t.Fatal("reloaded the same save twice")
	}
	// Relative times tick every 30 seconds.
	now = now.Add(31 * time.Second)
	if m = poll(); len(asOf) != 2 || !asOf[1].Equal(now) || !strings.HasPrefix(screen(m)[0], "load2") {
		t.Fatalf("after 31s: loads as of %v", asOf)
	}
	// A reload that fails keeps the page and says why.
	fail = true
	now = now.Add(31 * time.Second)
	m = poll()
	rows := screen(m)
	if !strings.HasPrefix(rows[0], "load2") || rows[len(rows)-3] != " reload failed: state.json: permission denied" {
		t.Fatalf("failed reload:\n%s", strings.Join(rows, "\n"))
	}
	fail = false
	now = now.Add(31 * time.Second)
	if m = poll(); m.statusText() != "" {
		t.Fatalf("status after a good reload: %q", m.statusText())
	}

	// A zero report loads at the start.
	c := New(Config{Load: func(time.Time) (view.Report, error) { return report("first"), nil }, Render: fakeRender(3, 0)})
	c.pollEvery = time.Hour
	var loaded bool
	// It asks the terminal for its background, and loads; the poll waits.
	for _, msg := range immediate(c.Init()) {
		if l, ok := msg.(loadedMsg); ok && l.report.Collector.DeviceLabel == "first" {
			loaded = true
		}
	}
	if !loaded {
		t.Fatal("Init did not load a zero report")
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

func TestJobs(t *testing.T) {
	if (&jobs{ctx: context.Background()}).stop() {
		t.Fatal("stop found a job where none started")
	}
	j := &jobs{ctx: context.Background()}
	if !j.start() {
		t.Fatal("start refused")
	}
	if !j.stop() {
		t.Fatal("stop missed the running job")
	}
	if j.start() {
		t.Fatal("a job started after stop")
	}
	j.done()
	j.wg.Wait()
}

// TestRun runs the program on a pipe: it draws on the alternate screen,
// leaves it on q, and stops a collection that still runs before returning.
func TestRun(t *testing.T) {
	stopped := make(chan struct{})
	var told atomic.Bool
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Config{
			Report:   report("leebook"),
			Render:   fakeRender(5, 0),
			Stopping: func() { told.Store(true) },
			Refresh: func(ctx context.Context) error {
				<-ctx.Done()
				close(stopped)
				return ctx.Err()
			},
		}, in, &out)
	}()
	if _, err := w.WriteString("r"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := w.WriteString("q"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after q")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Run returned before the collection stopped")
	}
	if !told.Load() {
		t.Fatal("Run did not say it waits for the collection")
	}
	s := out.String()
	if !strings.Contains(s, "\x1b[?1049h") || !strings.Contains(s, "\x1b[?1049l") {
		t.Fatalf("no alternate screen in and out: %q", s)
	}
}
