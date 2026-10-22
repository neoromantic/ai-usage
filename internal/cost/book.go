package cost

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"time"
)

// Seen is what a report found of one of the team's subscriptions: what its
// numbers are worked out from beside the store.
type Seen struct {
	Provider string
	Label    string
	// Plan is the plan the team read last, "" when none.
	Plan string
	// Here says it is logged in on this device: only then are its charge
	// day and price asked here.
	Here bool
	// Cycles are the ended periods of its main window, the weekly one,
	// else the longest it has, across the team.
	Cycles []Cycle
	// Consumers are its consumers' weighted tokens by week of SlotOf.
	Consumers []Consumer
	// IdleSince is when it was last used, when the report calls it idle.
	IdleSince *time.Time
	// Billing and SubscribedAt are what its tool on this device says of
	// who bills it and when it began.
	Billing      string
	SubscribedAt time.Time
}

// Question is one thing to ask a person of a subscription. Its ID is the
// same for as long as the question stands, and new after a plan change.
type Question struct {
	ID       string
	Kind     string
	Provider string
	Label    string
	Plan     string
	// Suggestion is the answer offered, when there is one: for a renewal,
	// the next date on the day the tool says the subscription began.
	Suggestion string
	// Choices are the cadences to choose from, for a renewal of a plan the
	// list prices yearly too.
	Choices []string
}

// Book is the cost of the team's subscriptions as of a report.
type Book struct {
	Prices Prices
	// Source is where Prices came from: SourceBuiltin or SourceRelay.
	Source string
	// Subs are the subscriptions, by provider and label.
	Subs      []Result
	Totals    Totals
	Consumers []Spend
	// Events are those of the last EventsKept, oldest first, and Questions
	// those to ask on this device now.
	Events    []Event
	Questions []Question
}

// Build works out the cost of the team's subscriptions as of now in loc:
// those the store keeps, with what the team said of them as device self
// merged, and those the report saw on a paid plan that the store has not
// recorded yet. list is the price list in use, and source where it came
// from. A nil store is an empty one.
func Build(s *Store, list Prices, source, self string, seen []Seen, now time.Time, loc *time.Location) Book {
	if s == nil {
		s = (&Store{V: StoreVersion}).fill()
	}
	b := Book{Prices: list, Source: source}
	by := map[string]Seen{}
	for _, g := range seen {
		if costProvider(g.Provider) && g.Label != "" {
			by[Key(g.Provider, g.Label)] = g
		}
	}
	keys := map[string]bool{}
	for k := range s.Subs {
		keys[k] = true
	}
	for k, g := range by {
		if s.Subs[k] == nil && g.Plan != "" && !Free(g.Plan) && Norm(g.Plan) != "unknown" {
			keys[k] = true
		}
	}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		sub, g := s.Subs[k], by[k]
		in := Input{
			Now: now, Loc: loc, List: list, Sub: sub, Provider: g.Provider, Label: g.Label, Plan: g.Plan,
			Cycles: g.Cycles, Consumers: g.Consumers, IdleSince: g.IdleSince, Billing: g.Billing, SubscribedAt: g.SubscribedAt,
		}
		if sub != nil {
			in.Provider, in.Label, in.Answer = sub.Provider, cmp.Or(g.Label, sub.Label), sub.Merged(self)
		}
		r := Compute(in)
		b.Subs = append(b.Subs, r)
		b.Events = append(b.Events, Events(in, r, s.Since)...)
		for _, kind := range Pending(r, sub, g.Here, s.NeverAsk) {
			q := Question{Kind: kind, Provider: r.Provider, Label: r.Label, Plan: r.Plan}
			q.ID = questionID(q)
			if kind == AskRenewal {
				if r.SuggestedDay > 0 {
					q.Suggestion = RenewsOn(r.SuggestedDay, now, cmp.Or(loc, time.Local))
				}
				q.Choices = Cadences(list, r.Provider, r.Plan)
			}
			b.Questions = append(b.Questions, q)
		}
	}
	slices.SortStableFunc(b.Subs, func(a, c Result) int {
		return cmp.Or(cmp.Compare(a.Provider, c.Provider), cmp.Compare(Key(a.Provider, a.Label), Key(c.Provider, c.Label)))
	})
	slices.SortStableFunc(b.Events, func(a, c Event) int { return a.At.Compare(c.At) })
	b.Totals = Sum(b.Subs)
	b.Consumers = ByConsumer(b.Subs)
	return b
}

// questionID keys a question by its subscription, kind, and plan.
func questionID(q Question) string {
	h := sha256.Sum256([]byte(Key(q.Provider, q.Label) + "\x00" + q.Kind + "\x00" + Norm(q.Plan)))
	return q.Kind + "-" + hex.EncodeToString(h[:6])
}
