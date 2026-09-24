package view

import (
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
