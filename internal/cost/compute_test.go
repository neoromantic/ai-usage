package cost

import (
	"math"
	"reflect"
	"slices"
	"testing"
	"time"
)

const day = 24 * time.Hour

func TestSlots(t *testing.T) {
	// t0 is a Thursday, 12:00 UTC.
	for _, c := range []struct {
		at   time.Time
		want int
	}{
		{t0, 3}, {t0.Add(-12 * time.Hour), 3}, {t0.Add(-12*time.Hour - time.Second), 3}, {t0.Add(-6 * day), 3},
		{t0.Add(-7 * day), 2}, {t0.Add(-13 * day), 2}, {t0.Add(-14 * day), 1}, {t0.Add(-27 * day), 0},
		{t0.Add(-28 * day), -1}, {t0.Add(12 * time.Hour), -1}, {t0.Add(11 * time.Hour), 3},
	} {
		if got := SlotOf(c.at, t0); got != c.want {
			t.Errorf("SlotOf(%v) = %d, want %d", c.at, got, c.want)
		}
	}
	for i, want := range []time.Time{
		time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), t0,
	} {
		if got := SlotEnd(i, t0); !got.Equal(want) {
			t.Errorf("SlotEnd(%d) = %v, want %v", i, got, want)
		}
	}
	days := []int64{5, 0, 0, 0, 0, 0, 0, 7, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 3, 99}
	if got := SlotTokens(days, t0, t0); got != [Slots]int64{5, 1, 7, 5} {
		t.Errorf("SlotTokens = %v", got)
	}
	// A device last collected yesterday: its index 0 is yesterday.
	if got := SlotTokens([]int64{4, 0, 0, 0, 0, 0, 6}, t0.Add(-day), t0); got != [Slots]int64{0, 0, 6, 4} {
		t.Errorf("SlotTokens of yesterday = %v", got)
	}
	if got := SlotTokens(nil, t0, t0); got != [Slots]int64{} {
		t.Errorf("SlotTokens of nothing = %v", got)
	}
}

func TestUsedSlots(t *testing.T) {
	cycles := []Cycle{
		{t0.Add(-1 * day), 91}, {t0.Add(time.Hour), 50}, // the second has not reset
		{t0.Add(-8 * day), 120},
		{t0.Add(-20 * day), 40}, {t0.Add(-19 * day), 76},
		{t0.Add(-40 * day), 100},
	}
	got := UsedSlots(cycles, [Slots]int64{0, 900, 900, 0}, t0)
	want := [Slots]Slot{
		{Known: true, Percent: 0},
		{Known: true, Percent: 76, Tokens: 900},
		{Known: true, Percent: 100, Tokens: 900},
		{Known: true, Percent: 91},
	}
	if got != want {
		t.Fatalf("UsedSlots = %+v", got)
	}
	got = UsedSlots(nil, [Slots]int64{0, 5, 0, 1}, t0)
	if got[0].Known != true || got[1].Known || got[2].Known != true || got[3].Known {
		t.Fatalf("a week with tokens and no period is unknown: %+v", got)
	}
}

func TestPrice(t *testing.T) {
	list := Builtin()
	typed, yearly := 150.0, 1200.0
	for name, c := range map[string]struct {
		provider, plan string
		a              Answer
		usd            float64
		known          bool
		source, stale  string
	}{
		"list":             {"claude", "max 20x", Answer{}, 200, true, PriceList, ""},
		"list yearly":      {"claude", "pro", Answer{Cadence: Yearly}, 200.0 / 12, true, PriceList, ""},
		"free":             {"codex", "free", Answer{}, 0, true, PriceList, ""},
		"not in the list":  {"codex", "team", Answer{}, 0, false, PriceUnknown, ""},
		"coarse max":       {"claude", "max", Answer{}, 0, false, PriceUnknown, ""},
		"typed":            {"claude", "max 20x", Answer{Price: &typed, PriceFor: "max 20x"}, 150, true, PriceTyped, ""},
		"typed for coarse": {"claude", "max 20x", Answer{Price: &typed, PriceFor: "max"}, 150, true, PriceTyped, ""},
		"typed any plan":   {"codex", "team", Answer{Price: &typed}, 150, true, PriceTyped, ""},
		"typed yearly":     {"codex", "team", Answer{Price: &yearly, PriceFor: "team", Cadence: Yearly}, 100, true, PriceTyped, ""},
		"stale, listed":    {"claude", "max 20x", Answer{Price: &typed, PriceFor: "max 5x"}, 200, true, PriceStale, "max 5x"},
		"stale, unlisted":  {"codex", "team", Answer{Price: &typed, PriceFor: "plus"}, 0, false, PriceStale, "plus"},
	} {
		p := Price(list, c.provider, c.plan, c.a)
		if (p.USD != nil) != c.known || c.known && math.Abs(*p.USD-c.usd) > 1e-9 || p.Source != c.source || p.StaleFor != c.stale {
			t.Errorf("%s: Price = %+v (%v)", name, p, p.USD)
		}
	}
	if !reflect.DeepEqual(Cadences(list, "claude", "pro"), []string{Monthly, Yearly}) || Cadences(list, "claude", "max 20x") != nil || Cadences(list, "codex", "x") != nil {
		t.Error("Cadences")
	}
}

func TestNextCharge(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	d := func(y int, m time.Month, dd int) time.Time { return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		anchor, cadence string
		now             time.Time
		want            time.Time
	}{
		{"2026-10-31", Monthly, t0, d(2026, 10, 31)},
		{"2026-10-22", Monthly, t0, d(2026, 10, 22)},
		{"2026-10-21", Monthly, t0, d(2026, 11, 21)},
		{"2026-01-31", Monthly, t0, d(2026, 10, 31)},
		{"2026-01-31", Monthly, d(2026, 11, 5), d(2026, 11, 30)},
		{"2026-01-31", Monthly, d(2027, 2, 1), d(2027, 2, 28)},
		{"2026-01-31", Monthly, d(2027, 3, 1), d(2027, 3, 31)},
		{"2025-12-14", Monthly, d(2026, 1, 20), d(2026, 2, 14)},
		{"2024-02-29", Yearly, t0, d(2027, 2, 28)},
		{"2024-02-29", Yearly, d(2028, 1, 1), d(2028, 2, 29)},
		{"2025-11-01", Yearly, t0, d(2026, 11, 1)},
		{"2025-10-01", Yearly, t0, d(2027, 10, 1)},
		{"2027-01-05", Monthly, t0, d(2027, 1, 5)},
		{"", Monthly, t0, time.Time{}},
		{"14", Monthly, t0, time.Time{}},
	} {
		got, ok := NextCharge(c.anchor, c.cadence, c.now, time.UTC)
		if ok != !c.want.IsZero() || !got.Equal(c.want) {
			t.Errorf("NextCharge(%s, %s, %v) = %v, %v", c.anchor, c.cadence, c.now, got, ok)
		}
	}
	// Late in the evening in Berlin it is already the next day.
	late := time.Date(2026, 10, 21, 23, 0, 0, 0, time.UTC)
	if got, _ := NextCharge("2026-09-21", Monthly, late, berlin); !got.Equal(time.Date(2026, 11, 21, 0, 0, 0, 0, berlin)) {
		t.Errorf("in Berlin = %v", got)
	}
	for _, c := range []struct {
		day  int
		now  time.Time
		want string
	}{
		{14, t0, "2026-11-14"}, {22, t0, "2026-10-22"}, {31, t0, "2026-10-31"},
		{31, d(2026, 9, 15), "2026-09-30"}, {30, d(2027, 2, 1), "2027-02-28"}, {1, d(2026, 12, 2), "2027-01-01"},
	} {
		if got := RenewsOn(c.day, c.now, time.UTC); got != c.want {
			t.Errorf("RenewsOn(%d, %v) = %s, want %s", c.day, c.now, got, c.want)
		}
	}
}

// sub60 is a subscription found 60 days ago on plan, whose quota was read
// an hour ago, logged in now.
func sub60(provider, label, plan string) *Sub {
	return &Sub{Provider: provider, Label: label, Plans: []PlanAt{{t0.Add(-60 * day), plan}},
		FirstSeen: t0.Add(-60 * day), LastSeen: t0, PlanConfirmedAt: t0.Add(-time.Hour)}
}

func weekly(pcts ...float64) []Cycle {
	var out []Cycle
	for i, p := range pcts {
		// The oldest first, one reset in each week.
		out = append(out, Cycle{ResetsAt: t0.Add(-time.Duration(len(pcts)-1-i)*7*day - day), Percent: p})
	}
	return out
}

func consumers(spec map[string]int64) []Consumer {
	var out []Consumer
	for name, n := range spec {
		out = append(out, Consumer{Name: name, Slots: [Slots]int64{n, n, n, n}})
	}
	slices.SortFunc(out, func(a, b Consumer) int { return a.Tokens() - b.Tokens() })
	return out
}

func (c Consumer) Tokens() int {
	n := 0
	for _, t := range c.Slots {
		n += int(t)
	}
	return n
}

func near(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-6 }

func TestCompute(t *testing.T) {
	list := Builtin()
	in := func(sub *Sub, cycles []Cycle, cs []Consumer) Input {
		return Input{Now: t0, Loc: time.UTC, List: list, Sub: sub, Provider: sub.Provider, Label: sub.Label, Cycles: cycles, Consumers: cs}
	}
	t.Run("used over four weeks, split by tokens", func(t *testing.T) {
		r := Compute(in(sub60("claude", "mira@studio.dev", "max 20x"), weekly(91, 100, 76, 97), consumers(map[string]int64{"mira@mira-mbp": 10, "leo@studio-1": 30})))
		if r.State != StateActive || r.PlanName != "Max 20x" || !near(r.Price.USD, 200) || r.Price.Source != PriceList {
			t.Fatalf("%+v", r)
		}
		if !near(r.UsedPct, 91) || !near(r.UsedUSD, 182) || !near(r.UnusedUSD, 18) || r.UsedEstimated {
			t.Fatalf("used %v%% $%v, unused $%v", *r.UsedPct, *r.UsedUSD, *r.UnusedUSD)
		}
		if len(r.Consumers) != 2 || r.Consumers[0].Name != "leo@studio-1" || !near(r.Consumers[0].USD, 136.5) || r.Consumers[0].Share != 0.75 || !near(r.Consumers[1].USD, 45.5) {
			t.Fatalf("consumers %+v", r.Consumers)
		}
		if r.Tokens != 160 || !near(r.USDPerMWeighted, 200/(160/1e6)) {
			t.Fatalf("tokens %d, per M %v", r.Tokens, *r.USDPerMWeighted)
		}
		if r.NextCharge != nil || r.SuggestedDay != 0 {
			t.Fatal("a charge nobody confirmed")
		}
	})
	t.Run("the price in effect in each week", func(t *testing.T) {
		sub := sub60("claude", "mira@studio.dev", "max 5x")
		sub.Plans = append(sub.Plans, PlanAt{t0.Add(-10 * day), "max 20x"})
		r := Compute(in(sub, weekly(100, 100, 100, 100), consumers(map[string]int64{"a": 1})))
		if !near(r.UsedUSD, 150) || !near(r.UnusedUSD, 50) || r.Slots[1].Plan != "max 5x" || r.Slots[2].Plan != "max 20x" {
			t.Fatalf("used $%v, unused $%v, slots %+v", *r.UsedUSD, *r.UnusedUSD, r.Slots)
		}
		// Downgraded: what was used costs more than the price now.
		sub.Plans[1].Plan = "pro"
		sub.Plans[0].Plan = "max 20x"
		if r := Compute(in(sub, weekly(100, 100, 100, 100), nil)); !near(r.UnusedUSD, 0) {
			t.Fatalf("unused below zero: %v", *r.UnusedUSD)
		}
	})
	t.Run("no week known: the whole price, by tokens, estimated", func(t *testing.T) {
		r := Compute(in(sub60("codex", "bots@studio.dev", "pro"), nil, consumers(map[string]int64{"a": 1, "b": 3})))
		if r.UsedPct != nil || !near(r.UsedUSD, 200) || !r.UsedEstimated || !near(r.UnusedUSD, 0) || !near(r.Consumers[0].USD, 150) {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("nobody used it: all of it unused", func(t *testing.T) {
		idle := t0.Add(-23 * day)
		i := in(sub60("codex", "leo@studio.dev", "plus"), nil, nil)
		i.IdleSince = &idle
		r := Compute(i)
		if !near(r.UsedPct, 0) || !near(r.UsedUSD, 0) || !near(r.UnusedUSD, 20) || r.State != StateIdle || r.IdleSince != &idle || r.USDPerMWeighted != nil {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("no price", func(t *testing.T) {
		r := Compute(in(sub60("codex", "ann@acme.dev", "team"), weekly(50, 50), consumers(map[string]int64{"a": 1})))
		if r.Price.USD != nil || r.Price.Source != PriceUnknown || !near(r.UsedPct, 50) || r.UsedUSD != nil || r.UnusedUSD != nil || r.Consumers[0].USD != nil {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("typed, yearly, with a confirmed charge", func(t *testing.T) {
		p := 1200.0
		i := in(sub60("codex", "ann@acme.dev", "team"), nil, nil)
		i.Answer = Answer{Price: &p, PriceFor: "team", PriceAt: t0, Cadence: Yearly, CadenceAt: t0, Renews: "2025-11-14", RenewsAt: t0}
		i.SubscribedAt = time.Date(2025, 3, 9, 10, 0, 0, 0, time.UTC)
		r := Compute(i)
		if !near(r.Price.USD, 100) || r.Price.Source != PriceTyped || r.Cadence != Yearly || r.NextCharge == nil || !r.NextCharge.Equal(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)) || r.SuggestedDay != 0 {
			t.Fatalf("%+v", r)
		}
		i.Answer = Answer{}
		if r := Compute(i); r.SuggestedDay != 9 {
			t.Fatalf("suggested day %d", r.SuggestedDay)
		}
	})
	t.Run("billed through a store", func(t *testing.T) {
		i := in(sub60("claude", "ann@acme.dev", "max 5x"), nil, nil)
		i.Billing = "apple_subscription"
		if r := Compute(i); r.BilledThrough != "App Store" || BilledThrough("stripe_subscription") != "" || BilledThrough("google_play_subscription") != "Google Play" {
			t.Fatal(r.BilledThrough)
		}
	})
	t.Run("not in the store yet", func(t *testing.T) {
		r := Compute(Input{Now: t0, List: list, Provider: "codex", Label: "ann@acme.dev", Plan: "plus"})
		if r.Plan != "plus" || !near(r.Price.USD, 20) || r.State != StateActive || !r.FirstSeen.Equal(t0) {
			t.Fatalf("%+v", r)
		}
	})
}

func TestStates(t *testing.T) {
	list := Builtin()
	for name, c := range map[string]struct {
		edit  func(*Sub, *Answer)
		state string
		since time.Time
	}{
		"active":                      {func(*Sub, *Answer) {}, StateActive, time.Time{}},
		"plan confirmed long ago":     {func(s *Sub, _ *Answer) { s.PlanConfirmedAt = t0.Add(-40 * day) }, StateUnconfirmed, t0.Add(-5 * day)},
		"never read: from first seen": {func(s *Sub, _ *Answer) { s.PlanConfirmedAt = time.Time{} }, StateUnconfirmed, t0.Add(-25 * day)},
		"gone from every device":      {func(s *Sub, _ *Answer) { s.LastSeen = t0.Add(-15 * day) }, StateUnconfirmed, t0.Add(-day)},
		"gone, a person confirmed": {func(s *Sub, a *Answer) {
			s.LastSeen = t0.Add(-15 * day)
			a.ConfirmedAt = t0.Add(-34 * day)
		}, StateActive, time.Time{}},
		"gone, confirmed too long ago": {func(s *Sub, a *Answer) {
			s.LastSeen = t0.Add(-40 * day)
			a.ConfirmedAt = t0.Add(-36 * day)
		}, StateUnconfirmed, t0.Add(-day)},
		"cancelled": {func(_ *Sub, a *Answer) { a.CancelledAt = t0.Add(-2 * day) }, StateEnded, t0.Add(-2 * day)},
		"cancelled, then confirmed": {func(_ *Sub, a *Answer) {
			a.CancelledAt, a.ConfirmedAt = t0.Add(-2*day), t0.Add(-day)
		}, StateActive, time.Time{}},
		"read free": {func(s *Sub, _ *Answer) { s.Plans = append(s.Plans, PlanAt{t0.Add(-3 * day), "free"}) }, StateEnded, t0.Add(-3 * day)},
		"cancelled, later read free": {func(s *Sub, a *Answer) {
			a.CancelledAt = t0.Add(-9 * day)
			s.Plans = append(s.Plans, PlanAt{t0.Add(-3 * day), "free"})
		}, StateEnded, t0.Add(-9 * day)},
		"free, then paid again": {func(s *Sub, _ *Answer) {
			s.Plans = append(s.Plans, PlanAt{t0.Add(-3 * day), "free"}, PlanAt{t0.Add(-day), "pro"})
		}, StateActive, time.Time{}},
		"a cancel of the one before": {func(s *Sub, a *Answer) {
			a.CancelledAt, s.Reopened = t0.Add(-40*day), t0.Add(-40*day)
		}, StateActive, time.Time{}},
	} {
		sub := sub60("claude", "ann@acme.dev", "pro")
		var a Answer
		c.edit(sub, &a)
		a.Renews, a.RenewsAt = "2026-11-01", t0.Add(-day)
		idle := t0.Add(-8 * day)
		r := Compute(Input{Now: t0, List: list, Sub: sub, Provider: "claude", Label: "ann@acme.dev", Answer: a, IdleSince: &idle})
		var since *time.Time
		switch c.state {
		case StateEnded:
			since = r.EndedAt
		case StateUnconfirmed:
			since = r.UnconfirmedSince
		}
		want := c.state
		if want == StateActive {
			want = StateIdle
		}
		if r.State != want || since != nil && !since.Equal(c.since) || (since == nil) != c.since.IsZero() {
			t.Errorf("%s: %s since %v", name, r.State, since)
		}
		if (r.NextCharge == nil) != (r.State == StateEnded) {
			t.Errorf("%s: next charge %v", name, r.NextCharge)
		}
	}
}

func TestEvents(t *testing.T) {
	list := Builtin()
	sub := sub60("claude", "leo@studio.dev", "max 5x")
	sub.FirstSeen = t0.Add(-45 * day)
	sub.Plans = []PlanAt{{t0.Add(-45 * day), "max 5x"}, {t0.Add(-40 * day), "pro"}, {t0.Add(-5 * day), "max 20x"}}
	sub.PriceChanges = []PriceChange{{t0.Add(-day), "max 20x", 200, 220}}
	i := Input{Now: t0, Loc: time.UTC, List: list, Sub: sub, Provider: "claude", Label: "leo@studio.dev",
		Answer: Answer{Renews: "2026-10-24", RenewsAt: t0}}
	r := Compute(i)
	evs := Events(i, r, t0.Add(-90*day))
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{EventPlanChanged, EventChargesSoon, EventPriceChanged}) {
		t.Fatalf("events %v", kinds)
	}
	pc := evs[0]
	if pc.From != "pro" || pc.To != "max 20x" || !near(pc.DeltaUSD(), 180) || !pc.At.Equal(t0.Add(-5*day)) {
		t.Fatalf("plan change %+v", pc)
	}
	if !near(evs[2].DeltaUSD(), 20) || !evs[1].At.Equal(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("price change %+v, charge %+v", evs[2], evs[1])
	}
	again := Events(i, Compute(i), t0.Add(-90*day))
	if !reflect.DeepEqual(again, evs) || evs[0].ID == evs[1].ID || evs[0].ID[:len(EventPlanChanged)] != EventPlanChanged {
		t.Fatal("event ids change between reports, or repeat")
	}

	// New since the store began, and ended; a charge 5 days off is no news.
	sub = sub60("codex", "bob@srv1", "pro")
	sub.FirstSeen = t0.Add(-2 * day)
	i = Input{Now: t0, List: list, Sub: sub, Provider: "codex", Label: "bob@srv1",
		Answer: Answer{CancelledAt: t0.Add(-time.Hour), Renews: "2026-10-27", RenewsAt: t0}}
	r = Compute(i)
	evs = Events(i, r, t0.Add(-90*day))
	if len(evs) != 2 || evs[0].Kind != EventNew || !near(evs[0].DeltaUSD(), 200) || evs[1].Kind != EventEnded || evs[1].DeltaUSD() != nil {
		t.Fatalf("events %+v", evs)
	}
	if evs := Events(i, r, t0.Add(-time.Hour)); len(evs) != 1 {
		t.Fatalf("a subscription the store's first look found is news: %+v", evs)
	}
	i.Answer = Answer{Renews: "2026-10-27", RenewsAt: t0}
	sub.FirstSeen, sub.PlanConfirmedAt = t0.Add(-60*day), t0.Add(-36*day)
	r = Compute(i)
	if evs := Events(i, r, t0); len(evs) != 1 || evs[0].Kind != EventUnconfirmed || !evs[0].At.Equal(t0.Add(-day)) {
		t.Fatalf("unconfirmed %+v", evs)
	}
}

func TestTotals(t *testing.T) {
	list := Builtin()
	run := func(sub *Sub, a Answer, cycles []Cycle, cs []Consumer) Result {
		return Compute(Input{Now: t0, List: list, Sub: sub, Provider: sub.Provider, Label: sub.Label, Answer: a, Cycles: cycles, Consumers: cs})
	}
	rs := []Result{
		run(sub60("claude", "mira@studio.dev", "max 20x"), Answer{}, weekly(50, 50, 50, 50), []Consumer{{Name: "mira@mbp", Slots: [Slots]int64{1, 1, 1, 1}}}),
		run(sub60("codex", "bots@studio.dev", "pro"), Answer{}, nil, []Consumer{{Name: "mira@mbp", Slots: [Slots]int64{1, 1, 1, 1}}, {Name: "bot@srv1", Slots: [Slots]int64{3, 3, 3, 3}}}),
		run(sub60("codex", "ann@acme.dev", "team"), Answer{}, nil, nil),
		run(sub60("grok", "ann@acme.dev", "SuperGrok"), Answer{CancelledAt: t0}, nil, nil),
	}
	tot := Sum(rs)
	if tot.USD != 400 || tot.WithoutPrice != 1 || tot.UsedUSD != 300 || tot.UnusedUSD != 100 || !near(tot.UsedPct, 75) || !tot.UsedEstimated {
		t.Fatalf("Sum = %+v", tot)
	}
	by := ByConsumer(rs)
	if len(by) != 2 || by[0].Name != "bot@srv1" || !near(by[0].USD, 150) || !near(by[1].USD, 150) || by[1].Tokens != 8 {
		t.Fatalf("ByConsumer = %+v", by)
	}
	if tot := Sum(nil); tot.UsedPct != nil || tot.USD != 0 {
		t.Fatal("Sum of nothing")
	}
}

func TestPending(t *testing.T) {
	sub := sub60("codex", "ann@acme.dev", "team")
	r := Result{Plan: "team", State: StateUnconfirmed}
	if got := Pending(r, sub, true, false); !reflect.DeepEqual(got, []string{AskRenewal, AskPrice, AskStillPaid}) {
		t.Fatalf("Pending = %v", got)
	}
	if got := Pending(r, sub, false, false); !reflect.DeepEqual(got, []string{AskStillPaid}) {
		t.Fatalf("Pending elsewhere = %v", got)
	}
	sub.Skip(AskPrice)
	sub.Skip(AskStillPaid)
	if got := Pending(r, sub, true, false); !reflect.DeepEqual(got, []string{AskRenewal}) {
		t.Fatalf("Pending after skips = %v", got)
	}
	r.Plan = "plus"
	usd := 20.0
	r.Price.USD = &usd
	if got := Pending(r, sub, true, false); !reflect.DeepEqual(got, []string{AskRenewal}) {
		t.Fatalf("Pending after a plan change = %v", got)
	}
	next := t0
	r.NextCharge, r.State = &next, StateActive
	if got := Pending(r, nil, true, false); got != nil {
		t.Fatalf("Pending with nothing to ask = %v", got)
	}
	r.NextCharge = nil
	if Pending(r, sub, true, true) != nil {
		t.Fatal("never ask asked")
	}
	// A question skipped before the tier was read stays skipped.
	coarse := sub60("claude", "ann@acme.dev", "max")
	coarse.Skip(AskRenewal)
	coarse.Plans[0].Plan = "max 20x"
	if got := Pending(Result{Plan: "max 20x", Price: r.Price, State: StateActive}, coarse, true, false); got != nil {
		t.Fatalf("Pending after the tier = %v", got)
	}
	r.State = StateEnded
	if Pending(r, sub, true, false) != nil {
		t.Fatal("an ended subscription asked")
	}
}
