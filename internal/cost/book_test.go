package cost

import (
	"slices"
	"testing"
	"time"
)

// Build works out every subscription the store keeps, even one no device
// has any more, and one the report saw before the store did; it leaves out
// what is free, of Hermes, or has no plan, and asks on this device only
// what is for it to answer.
func TestBuild(t *testing.T) {
	list := Builtin()
	s := (&Store{V: StoreVersion, Since: t0.Add(-90 * day)}).fill()
	mira := s.Sub("claude", "mira@studio.dev", t0.Add(-60*day))
	mira.Plans = []PlanAt{{t0.Add(-60 * day), "max 20x"}}
	mira.LastSeen, mira.PlanConfirmedAt = t0, t0.Add(-time.Hour)
	mira.Heard = map[string]Answer{"d-mbp": {Renews: "2026-09-23", RenewsAt: t0.Add(-day)}}
	gone := s.Sub("codex", "leo@studio.dev", t0.Add(-60*day))
	gone.Plans = []PlanAt{{t0.Add(-60 * day), "plus"}}
	gone.LastSeen, gone.PlanConfirmedAt = t0.Add(-40*day), t0.Add(-40*day)
	began := time.Date(2025, 3, 9, 10, 0, 0, 0, time.UTC)
	seen := []Seen{
		{Provider: "claude", Label: "Mira@studio.dev", Plan: "max 20x", Here: true, Cycles: weekly(80, 80, 80, 80),
			Consumers: []Consumer{{Name: "mira@mira-mbp", Slots: [Slots]int64{1, 1, 1, 1}}}},
		{Provider: "codex", Label: "ann@acme.dev", Plan: "team", Here: true, SubscribedAt: began},
		{Provider: "grok", Label: "kim@acme.dev", Plan: "SuperGrok"},
		{Provider: "codex", Label: "free@acme.dev", Plan: "free"},
		{Provider: "codex", Label: "api key"},
		{Provider: "hermes", Label: "bot", Plan: "pro"},
	}
	b := Build(s, list, SourceBuiltin, "d-self", seen, t0, time.UTC)
	var names []string
	for _, r := range b.Subs {
		names = append(names, r.Provider+" "+r.Label+" "+r.State)
	}
	want := []string{"claude Mira@studio.dev active", "codex ann@acme.dev active", "codex leo@studio.dev unconfirmed", "grok kim@acme.dev active"}
	if !slices.Equal(names, want) {
		t.Fatalf("subscriptions %v", names)
	}
	if b.Source != SourceBuiltin || b.Totals.USD != 250 || b.Totals.WithoutPrice != 1 || !near(b.Subs[0].UsedPct, 80) {
		t.Fatalf("totals %+v", b.Totals)
	}
	if b.Subs[0].NextCharge == nil || !b.Subs[0].NextCharge.Equal(time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("the teammate's charge day: %v", b.Subs[0].NextCharge)
	}
	if len(b.Consumers) != 1 || !near(b.Consumers[0].USD, 160) {
		t.Fatalf("consumers %+v", b.Consumers)
	}
	var asked []string
	for _, q := range b.Questions {
		asked = append(asked, q.Kind+" "+q.Label)
	}
	if !slices.Equal(asked, []string{"renewal ann@acme.dev", "price ann@acme.dev", "still_paid leo@studio.dev"}) {
		t.Fatalf("questions %v", asked)
	}
	if q := b.Questions[0]; q.Suggestion != "2026-11-09" || q.Choices != nil || q.ID == b.Questions[1].ID {
		t.Fatalf("renewal %+v", q)
	}
	if len(b.Events) != 2 || b.Events[0].Kind != EventUnconfirmed || b.Events[1].Kind != EventChargesSoon {
		t.Fatalf("events %+v", b.Events)
	}
	// The same report has the same ids; a skip is not asked again.
	again := Build(s, list, SourceBuiltin, "d-self", seen, t0, time.UTC)
	if again.Questions[0].ID != b.Questions[0].ID || again.Events[0].ID != b.Events[0].ID {
		t.Fatal("ids change between reports")
	}
	gone.Skip(AskStillPaid)
	if q := Build(s, list, SourceBuiltin, "d-self", nil, t0, time.UTC).Questions; len(q) != 0 {
		t.Fatalf("asked after a skip: %+v", q)
	}
	if b := Build(nil, list, SourceBuiltin, "d-self", nil, t0, nil); len(b.Subs) != 0 || b.Totals.USD != 0 {
		t.Fatal("Build of nothing")
	}
}
