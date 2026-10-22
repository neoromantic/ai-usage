package view

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/cost"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

func costSub(t *testing.T, c *Cost, provider, label string) CostSubscription {
	t.Helper()
	if s := c.Subscription(provider, label); s != nil {
		return *s
	}
	t.Fatalf("no cost of %s %s in %+v", provider, label, c.Subscriptions)
	return CostSubscription{}
}

// TestCostStates builds a team's cost from this device's state, another
// device's snapshot, and the cost store, with a subscription in each state
// and each source of a price: one active at a typed price, with a plan
// changed this week; one whose typed price is for another plan; one idle
// at its list price; one with no list price; one no device has any more
// that nobody can say is still paid; and one cancelled.
func TestCostStates(t *testing.T) {
	day := 24 * time.Hour
	st := emptyState()
	// claude ann: logged in here, read now, used here and on the other
	// device; three weeks of its cycles.
	addAccount(st, "claude", "ann@acme.dev", true, weekQuota(now, 40), 0)
	st.Accounts[state.Key("claude", "ann@acme.dev")].Plan = "max 20x"
	spend(st, "claude", "ann@acme.dev", "/work/app", 3_000_000, now.Add(-time.Hour), now.Add(-9*day), now.Add(-16*day))
	// codex bob: here, with Hermes spending through it.
	addAccount(st, "codex", "bob@acme.dev", true, codexQuota(now, 30), 0)
	st.Accounts[state.Key("codex", "bob@acme.dev")].Plan = "pro"
	spend(st, "codex", "bob@acme.dev", "/work/app", 1_000_000, now.Add(-2*time.Hour))
	spendVia(st, "openai-codex", "/work/bot", "codex", "bob@acme.dev", 1_000_000, now.Add(-3*time.Hour))
	// grok: here, last used 10 days ago: idle.
	addAccount(st, "grok", "g-1", true, &state.Quota{At: now, Source: "log", Windows: []snapshot.Window{win("7d", 0, now.Add(3*day))}}, 0)
	st.Accounts[state.Key("grok", "g-1")].Plan = "SuperGrok"
	spend(st, "grok", "g-1", "/work/app", 1_000_000, now.Add(-10*day))
	f := newFixture(t, st)
	f.cycles = collect.Cycles{collect.HistoryKey("claude", "ann@acme.dev"): {
		{Window: "7d", ResetsAt: now.Add(-3 * day), Percent: 80, LastReadAt: now.Add(-3*day - time.Hour)},
		{Window: "7d", ResetsAt: now.Add(-10 * day), Percent: 60, LastReadAt: now.Add(-10*day - time.Hour)},
		{Window: "7d", ResetsAt: now.Add(-17 * day), Percent: 100, LastReadAt: now.Add(-17*day - time.Hour)},
	}}
	f.seal(now)
	// The other device: claude ann, and codex team on a plan the list has
	// no price for.
	other := emptyState()
	addAccount(other, "claude", "ann@acme.dev", true, nil, 0)
	spend(other, "claude", "ann@acme.dev", "/work/x", 1_000_000, now.Add(-2*time.Hour))
	addAccount(other, "codex", "kim@corp.test", true, codexQuota(now, 10), 0)
	other.Accounts[state.Key("codex", "kim@corp.test")].Plan = "team"
	spend(other, "codex", "kim@corp.test", "/work/x", 1_000_000, now.Add(-2*time.Hour))
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: []snapshot.Doc{
		otherDoc(t, f.key, "d-other-device", "kimbox", now, other),
	}}

	s := &cost.Store{V: cost.StoreVersion, Since: now.Add(-200 * day), Subs: map[string]*cost.Sub{}}
	ann := s.Sub("claude", "ann@acme.dev", now.Add(-100*day))
	ann.Plans = []cost.PlanAt{{At: now.Add(-100 * day), Plan: "max 5x"}, {At: now.Add(-2 * day), Plan: "max 20x"}}
	ann.PlanConfirmedAt, ann.LastSeen = now, now
	if err := ann.Answer.SetPrice(new(180.0), "max 20x", now.Add(-day)); err != nil {
		t.Fatal(err)
	}
	if err := ann.Answer.SetRenews(now.Add(2*day).Format(time.DateOnly), now.Add(-day)); err != nil {
		t.Fatal(err)
	}
	bob := s.Sub("codex", "bob@acme.dev", now.Add(-100*day))
	bob.Plans = []cost.PlanAt{{At: now.Add(-100 * day), Plan: "pro"}}
	bob.PlanConfirmedAt, bob.LastSeen = now, now
	if err := bob.Answer.SetPrice(new(15.0), "plus", now.Add(-50*day)); err != nil {
		t.Fatal(err)
	}
	old := s.Sub("claude", "old@acme.dev", now.Add(-150*day))
	old.Plans = []cost.PlanAt{{At: now.Add(-150 * day), Plan: "pro"}}
	old.PlanConfirmedAt, old.LastSeen = now.Add(-60*day), now.Add(-30*day)
	gone := s.Sub("codex", "gone@acme.dev", now.Add(-150*day))
	gone.Plans = []cost.PlanAt{{At: now.Add(-150 * day), Plan: "plus"}}
	gone.PlanConfirmedAt, gone.LastSeen = now.Add(-5*day), now.Add(-5*day)
	gone.Answer.Cancel(now.Add(-4 * day))
	f.in.Cost = s

	r := Build(f.in)
	c := r.Cost
	if c == nil {
		t.Fatal("no cost")
	}

	a := costSub(t, c, "claude", "ann@acme.dev")
	if a.PriceSource != PriceTyped || *a.PriceUSD != 180 || a.State != CostActive || !a.Current || a.RenewsAt == nil || !a.RenewsConfirmed {
		t.Fatalf("ann = %+v", a)
	}
	// Three weeks of cycles, 80, 60, and 100, and the oldest week, with
	// no cycle and no tokens, 0.
	if a.UsedPct == nil || *a.UsedPct != 60 || a.UsedEstimated {
		t.Fatalf("ann's Used = %v, estimated %v", a.UsedPct, a.UsedEstimated)
	}
	if len(a.Consumers) != 2 || a.Consumers[0].DeviceID != "d-this-device" || a.Consumers[0].Device != "sam@thisbox" || a.Consumers[1].Device != "kim@kimbox" {
		t.Fatalf("ann's consumers = %+v", a.Consumers)
	}
	if len(a.Plans) != 2 || a.Plans[1].Plan != "max 20x" {
		t.Fatalf("ann's plans = %+v", a.Plans)
	}

	b := costSub(t, c, "codex", "bob@acme.dev")
	if b.PriceSource != PriceStale || b.TypedFor == nil || *b.TypedFor != "plus" || *b.PriceUSD != 200 {
		t.Fatalf("bob = %+v", b)
	}
	// Hermes spent through bob's login on this device: one consumer.
	if len(b.Consumers) != 1 || b.Consumers[0].Share != 100 {
		t.Fatalf("bob's consumers = %+v", b.Consumers)
	}

	if g := costSub(t, c, "grok", "g-1"); g.State != CostIdle || g.IdleSince == nil || g.PriceUSD == nil || *g.PriceUSD != 30 {
		t.Fatalf("grok = %+v", g)
	}
	k := costSub(t, c, "codex", "kim@corp.test")
	if k.PriceSource != PriceUnknown || k.PriceUSD != nil || k.Current {
		t.Fatalf("kim = %+v", k)
	}
	if o := costSub(t, c, "claude", "old@acme.dev"); o.State != CostUnconfirmed || o.UnconfirmedSince == nil || o.Current {
		t.Fatalf("old = %+v", o)
	}
	if e := costSub(t, c, "codex", "gone@acme.dev"); e.State != CostEnded || e.EndedAt == nil {
		t.Fatalf("gone = %+v", e)
	}
	// Ended last within its provider.
	var order []string
	for _, s := range c.Subscriptions {
		order = append(order, s.Provider+" "+s.Account)
	}
	if i, j := slices.Index(order, "codex gone@acme.dev"), slices.Index(order, "codex kim@corp.test"); i < j {
		t.Fatalf("order = %v", order)
	}

	// The total: 180 + 200 + 30 + old's 20, kim's without a price, gone's
	// in none.
	if c.TotalUSD != 430 || c.WithoutPrice != 1 || c.UsedUSD == nil || c.Prices.Source != cost.SourceBuiltin {
		t.Fatalf("totals = %v, %d without, used %v, prices %+v", c.TotalUSD, c.WithoutPrice, c.UsedUSD, c.Prices)
	}
	if c.NextCharge == nil || c.NextCharge.Account != "ann@acme.dev" || *c.NextCharge.USD != 180 {
		t.Fatalf("next charge = %+v", c.NextCharge)
	}

	// Questions: of this device's subscriptions, a charge day; of the one
	// nobody can say is paid, whether it is. Not kim's price: kim is not
	// logged in here.
	var asked []string
	for _, q := range c.Questions {
		asked = append(asked, q.Kind+" "+q.Provider+" "+q.Account)
		if q.Text == "" || q.ID == "" {
			t.Errorf("question without words or id: %+v", q)
		}
	}
	for _, want := range []string{"renewal codex bob@acme.dev", "renewal grok g-1", "still_paid claude old@acme.dev"} {
		if !slices.Contains(asked, want) {
			t.Errorf("questions %v lack %q", asked, want)
		}
	}
	if slices.ContainsFunc(asked, func(s string) bool {
		return strings.Contains(s, "kim@corp.test") || strings.HasPrefix(s, "renewal claude ann")
	}) {
		t.Errorf("questions %v", asked)
	}

	// Events: the plan change, the charge in 2 days, and the end, each in
	// words.
	kinds := map[string]CostEvent{}
	for _, e := range c.Events {
		kinds[e.Kind] = e
	}
	if e := kinds[EventPlanChanged]; e.DeltaUSD == nil || !strings.Contains(e.Text, "Max 5x to Max 20x") {
		t.Fatalf("plan change = %+v", e)
	}
	if e := kinds[EventChargesSoon]; !strings.Contains(e.Text, "claude ann@acme.dev charges $180 on ") {
		t.Fatalf("charges soon = %+v", e)
	}
	if _, ok := kinds[EventEnded]; !ok {
		t.Fatalf("events = %+v", c.Events)
	}

	// ATTENTION: CHECK for old, COST for the change, IDLE with its price.
	var att []string
	for _, x := range r.Attention {
		att = append(att, x.Kind+" "+x.Account+": "+x.Text)
	}
	for _, want := range []string{
		"check old@acme.dev: still paid? last confirmed 3 Jul · on no device since 2 Aug: ai-usage cost confirm or cancel",
		"cost ann@acme.dev: plan Max 5x → Max 20x, +$80/mo",
		"idle g-1: idle 10d, $30/mo paid and unused",
	} {
		if !slices.Contains(att, want) {
			t.Errorf("ATTENTION lacks %q:\n%s", want, strings.Join(att, "\n"))
		}
	}
	// The matrix in dollars: each device's part, and the column's price.
	text := plainText(r, Options{Width: 160, Cost: true, Loc: time.UTC})
	for _, want := range []string{"$ used, unused, and price a month", "unused", "$180", "COST  $430/mo +1 without a price"} {
		if !strings.Contains(text, want) {
			t.Errorf("page lacks %q:\n%s", want, text)
		}
	}
}

// TestCostWithoutStore: a report with no cost store yet prices what the team
// reads at the list's prices, and a report saved before cost has none and
// draws no COST.
func TestCostWithoutStore(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann@acme.dev", true, weekQuota(now, 40), 10)
	st.Accounts[state.Key("claude", "ann@acme.dev")].Plan = "max 5x"
	r := Build(newFixture(t, st).in)
	if s := costSub(t, r.Cost, "claude", "ann@acme.dev"); *s.PriceUSD != 100 || s.PriceSource != PriceList || len(r.Cost.Events) != 0 {
		t.Fatalf("no store: %+v, events %+v", s, r.Cost.Events)
	}
	r.Cost = nil
	Fill(&r)
	if text := plainText(r, Options{Width: 120, Loc: time.UTC}); strings.Contains(text, "COST") || strings.Contains(text, "/mo") {
		t.Fatalf("a report without cost:\n%s", text)
	}
}
