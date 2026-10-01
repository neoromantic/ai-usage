package view

import (
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// TestSubscriptionsCountNoReadingByTheMainWindow counts as no reading only
// an account whose main window has none: not one read in the first tenth
// of its week, which has a bar and what is left but no forecast yet.
func TestSubscriptionsCountNoReadingByTheMainWindow(t *testing.T) {
	st := emptyState()
	// The week began 6 hours ago, and the 5h window has reset since.
	addAccount(st, "codex", "ann@acme.dev", true, &state.Quota{At: now, Source: "harness", Windows: []snapshot.Window{
		win("7d", 3, now.Add(6*24*time.Hour+18*time.Hour)),
		win("5h", 10, now.Add(-time.Hour)),
	}}, 1_000_000)
	// Every window has reset.
	addAccount(st, "codex", "bob@mail.test", false, &state.Quota{At: now.Add(-3 * time.Hour), Source: "harness", Windows: []snapshot.Window{
		win("7d", 40, now.Add(-time.Hour)),
	}}, 0)
	addAccount(st, "claude", "sam@mail.test", false, nil, 1_000_000)
	r := Build(newFixture(t, st).in)
	if a := findTeamAccount(t, r, "codex", "ann@acme.dev"); a.State != StateUnknown {
		t.Fatalf("ann's state is %q, want no forecast yet", a.State)
	}
	title := pageSection(Render(r, Options{Width: 100, Loc: time.UTC}), "SUBSCRIPTIONS")[0]
	if title != "SUBSCRIPTIONS  3 · 2 no reading" {
		t.Errorf("title %q, want 2 no reading", title)
	}
}

// TestBarTickFollowsTheForecast draws the tick inside the used part just
// when AT RESET says out or over, also where the used part and the tick
// fall in one cell.
func TestBarTickFollowsTheForecast(t *testing.T) {
	p := newPage(&Report{GeneratedAt: now}, Options{Width: 120})
	for _, c := range []struct {
		used, elapsed float64
		state, bar    string
	}{
		{14.6, 0.1625, StateTight, "━━━━┃───────────────────"},
		{51.5, 0.5, StateOver, "━━━━━━━━━━━╋────────────"},
		{42, 0.25, StateOver, "━━━━━━╋━━━──────────────"},
		{12, 0.25, StateUnder, "━━━───┃─────────────────"},
		{100, 0.7, StateOut, "━━━━━━━━━━━━━━━━╋━━━━━━━"},
	} {
		// A week read now, with elapsed of it passed.
		resets := now.Add(time.Duration((1 - c.elapsed) * float64(week)))
		w := readWindow(snapshot.Window{Name: "7d", Percent: c.used, ResetsAt: &resets}, now, now, nil)
		if w.State != c.state {
			t.Errorf("%v%% used at %v: state %q, want %q", c.used, c.elapsed, w.State, c.state)
		}
		if got := p.bar(&w, barCells).String(); got != c.bar {
			t.Errorf("%v%% used at %v, %s: bar %s, want %s", c.used, c.elapsed, w.State, got, c.bar)
		}
	}
}

// TestSubscriptionsDevicesAndNoReading: the column of how many devices hold
// an account is DEVICES, and a row with no reading says why in its bar's
// place, dim and cut to the bar.
func TestSubscriptionsDevicesAndNoReading(t *testing.T) {
	r := loadReport(t, "team")
	for _, w := range []int{80, 120} {
		p := Render(r, Options{Width: w, Loc: sampleZone})
		head := bodyLine(t, p, "SUBSCRIPTIONS", func(l string) bool { return strings.HasPrefix(l, "  CLAUDE ") })
		if !strings.Contains(head, " DEVICES") || strings.Contains(head, "USERS") {
			t.Errorf("at %d the header is %q", w, head)
		}
		kim := bodyLine(t, p, "SUBSCRIPTIONS", func(l string) bool { return strings.Contains(l, "kim@corp.test") })
		if !strings.Contains(kim, "  /opt/homebrew/bin/cl") || !strings.Contains(kim, "…") {
			t.Errorf("at %d kim's row is %q", w, kim)
		}
	}
	color := Render(r, Options{Width: 120, Loc: sampleZone, Color: true, Dark: true})
	dim := sgr.FindString(lipglossFg(NewTheme(true).Muted))
	if !strings.Contains(strings.Join(color.Body, "\n"), dim+"/opt/homebrew/bin/claud…") {
		t.Error("the reason there is no reading is not dim")
	}
	// A row that has a reading has its bar whatever else it says.
	for i := range r.Team.Providers {
		for j := range r.Team.Providers[i].Accounts {
			if a := &r.Team.Providers[i].Accounts[j]; a.Label == "lee@corp.test" {
				why := "should not show"
				a.NoReading = &why
			}
		}
	}
	if s := strings.Join(Render(r, Options{Width: 120, Loc: sampleZone}).Body, "\n"); strings.Contains(s, "should not") {
		t.Errorf("a reason over a reading:\n%s", s)
	}
}
