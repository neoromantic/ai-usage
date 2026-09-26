package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/view"
)

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
	// A matrix gone in share mode, as after a refresh that left one
	// device: % still leaves share mode.
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

// viewsRender is fakeRender for a team whose DEVICES has both views: the
// status view has no matrix, and the header says which view shows.
func viewsRender(n, cols int) func(view.Report, view.Options) view.Page {
	return func(r view.Report, o view.Options) view.Page {
		c := cols
		if o.DeviceStatus {
			c = 0
		}
		p := fakeRender(n, c)(r, o)
		p.DeviceViews = true
		p.Header += fmt.Sprintf(" status=%v", o.DeviceStatus)
		return p
	}
}

// usageBar and statusBar are the key bars of a team's two views when every
// key fits.
const (
	usageBar  = " ↑↓ scroll · ←→ matrix · s ‹usage› status · p period ‹7d› · % share · r refresh · ? help · q quit"
	statusBar = " ↑↓ scroll · s usage ‹status› · p period ‹7d› · r refresh · ? help · q quit"
)

// TestDeviceViews: s steps between the matrix and the status view, which
// has no share and no sideways scroll, keeps the page's top and the
// matrix's scroll, and takes the period keys.
func TestDeviceViews(t *testing.T) {
	m := model(t, 120, 30, Config{Render: viewsRender(100, 12), Refresh: refresher()})
	if got := bar(m); got != usageBar {
		t.Fatalf("usage bar\n got %q\nwant %q", got, usageBar)
	}
	m = keys(t, m, "j", "j", "j", "right", "right")
	m = keys(t, m, "s")
	if got := bar(m); !m.opts.DeviceStatus || got != statusBar || !strings.Contains(screen(m)[0], "status=true") {
		t.Fatalf("status bar\n got %q\nwant %q\nheader %q", got, statusBar, screen(m)[0])
	}
	if m.top != 3 || m.opts.MatrixScroll != 2 {
		t.Fatalf("s moved the page: top %d, matrix scroll %d", m.top, m.opts.MatrixScroll)
	}
	// % and sideways do nothing here; the period keys work.
	m = keys(t, m, "%", "left", "h", "p", "9")
	m = update(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp, Mod: tea.ModShift})
	if m.opts.Share || m.opts.MatrixScroll != 2 || m.opts.Period != view.Quarter || !strings.Contains(bar(m), "‹90d›") {
		t.Fatalf("keys in status: share %v, scroll %d, period %s, bar %q", m.opts.Share, m.opts.MatrixScroll, m.opts.Period, bar(m))
	}
	// Back to the matrix, as it was scrolled.
	m = keys(t, m, "s")
	if got := bar(m); m.opts.DeviceStatus || m.opts.MatrixScroll != 2 || m.top != 3 || !strings.Contains(got, "s ‹usage› status · p period ‹90d› · % share") {
		t.Fatalf("back: status %v, scroll %d, top %d, bar %q", m.opts.DeviceStatus, m.opts.MatrixScroll, m.top, got)
	}
	// In share mode, the status view hides % and keeps the mode.
	m = keys(t, m, "%", "s")
	if !m.opts.Share || strings.Contains(bar(m), "%") {
		t.Fatalf("share in status: %v %q", m.opts.Share, bar(m))
	}
	if m = keys(t, m, "s"); !strings.Contains(bar(m), "% ‹share›") {
		t.Fatalf("share after status: %q", bar(m))
	}
}

func TestDeviceViewsStart(t *testing.T) {
	// A view opened in status starts there; in the help, s waits.
	m := model(t, 120, 30, Config{Render: viewsRender(100, 12), Options: view.Options{DeviceStatus: true}})
	if got := bar(m); !strings.Contains(got, "s usage ‹status›") {
		t.Fatalf("opened in status: %q", got)
	}
	if m = keys(t, m, "?", "s"); !m.opts.DeviceStatus {
		t.Fatal("s in the help changed the view")
	}
	// The help says what s does, and what its marks mean there.
	help := strings.Join(ansiStrip(m.helpLines()), "\n")
	for _, want := range []string{"  s ", "each device's status", "fails in VIA", "release in VERSION"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q:\n%s", want, help)
		}
	}
	// One device has one view: s does nothing and is not in the bar.
	m = model(t, 120, 30, Config{})
	if m = keys(t, m, "s"); m.opts.DeviceStatus || strings.Contains(bar(m), " s ") {
		t.Fatalf("one view: status %v, bar %q", m.opts.DeviceStatus, bar(m))
	}
}

func TestDeviceViewsBar(t *testing.T) {
	// In ASCII the chosen view is in brackets; in color, a pill as wide.
	m := model(t, 120, 30, Config{Render: viewsRender(100, 12), Options: view.Options{ASCII: true}})
	if got := bar(m); !strings.Contains(got, " . s [usage] status . ") {
		t.Fatalf("ascii: %q", got)
	}
	m = model(t, 120, 30, Config{Render: viewsRender(100, 12), Options: view.Options{Color: true, Dark: true}})
	if got := ansi.Strip(m.keyBar()); !strings.Contains(got, "s  usage  status · p") || !strings.Contains(m.keyBar(), m.pill("usage")) {
		t.Fatalf("color: %q", got)
	}

	// A narrow terminal first names the status view alone, as % names
	// share, then drops share and refresh, then the views, before the
	// matrix.
	for _, c := range []struct {
		width  int
		status bool
		want   string
	}{
		{97, false, usageBar},
		{96, false, " ↑↓ scroll · ←→ matrix · s status · p period ‹7d› · % share · r refresh · ? help · q quit"},
		{89, false, " ↑↓ scroll · ←→ matrix · s status · p period ‹7d› · % share · r refresh · ? help · q quit"},
		{88, false, " ↑↓ scroll · ←→ matrix · s status · p period ‹7d› · r refresh · ? help · q quit"},
		{67, false, " ↑↓ scroll · ←→ matrix · s status · p period ‹7d› · ? help · q quit"},
		{66, false, " ↑↓ scroll · ←→ matrix · p period ‹7d› · ? help · q quit"},
		{55, false, " ↑↓ scroll · p period ‹7d› · ? help · q quit"},
		{75, true, statusBar},
		{74, true, " ↑↓ scroll · s ‹status› · p period ‹7d› · r refresh · ? help · q quit"},
		{68, true, " ↑↓ scroll · s ‹status› · p period ‹7d› · ? help · q quit"},
		{56, true, " ↑↓ scroll · p period ‹7d› · ? help · q quit"},
	} {
		o := view.Options{DeviceStatus: c.status}
		if got := bar(model(t, c.width, 30, Config{Render: viewsRender(100, 12), Refresh: refresher(), Options: o})); got != c.want {
			t.Errorf("width %d, status %v\n got %q\nwant %q", c.width, c.status, got, c.want)
		}
	}
	// In ASCII the short form is as wide, in brackets.
	m = model(t, 74, 30, Config{Render: viewsRender(100, 12), Refresh: refresher(), Options: view.Options{ASCII: true, DeviceStatus: true}})
	if got := bar(m); got != " j k scroll . s [status] . p period [7d] . r refresh . ? help . q quit" {
		t.Errorf("ascii, short: %q", got)
	}
}

// TestDeviceViewsAt80: on the team fixture at 80 columns, the key bar names
// the status view alone and keeps refresh, dropping only share, and the
// status view has room for every key it has.
func TestDeviceViewsAt80(t *testing.T) {
	m := model(t, 80, 30, Config{Report: teamReport(t), Render: view.Render, Refresh: refresher()})
	if got, want := bar(m), " ↑↓ scroll · ←→ matrix · s status · p period ‹7d› · r refresh · ? help · q quit"; got != want {
		t.Errorf("usage at 80\n got %q\nwant %q", got, want)
	}
	m = keys(t, m, "s")
	if got, want := bar(m), " ↑↓ scroll · s usage ‹status› · p period ‹7d› · r refresh · ? help · q quit"; got != want {
		t.Errorf("status at 80\n got %q\nwant %q", got, want)
	}
}

// TestDeviceViewsDrawn: s draws the status view of a real report, with the
// device's release, and the page keeps its lines.
func TestDeviceViewsDrawn(t *testing.T) {
	m := model(t, 120, 40, Config{Report: teamReport(t), Render: view.Render})
	lines := len(m.page.Body)
	m = keys(t, m, "s")
	text := strings.Join(screen(m), "\n")
	if !strings.Contains(text, "DEVICES  13 · 1 error · 2 old · by 7d") || !strings.Contains(text, "v1.4.0 ↓") || len(m.page.Body) != lines {
		t.Fatalf("status view, %d lines, was %d:\n%s", len(m.page.Body), lines, text)
	}
}
