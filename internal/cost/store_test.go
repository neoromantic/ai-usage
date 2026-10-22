package cost

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/state"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

const self = "d-0000self"

func see(provider, label, plan string, at time.Time, current bool, quota time.Time) Sighting {
	return Sighting{Provider: provider, Label: label, Plan: plan, At: at, Current: current, QuotaAt: quota}
}

func TestCurrentPlan(t *testing.T) {
	for name, c := range map[string]struct {
		ss   []Sighting
		want string
		at   time.Time
	}{
		"none": {nil, "", time.Time{}},
		"newest quota reading wins over a newer snapshot": {[]Sighting{
			see("claude", "ann@acme.dev", "pro", t0, true, t0.Add(-48*time.Hour)),
			see("claude", "ann@acme.dev", "max 5x", t0.Add(-time.Hour), true, t0.Add(-time.Hour)),
		}, "max 5x", t0.Add(-time.Hour)},
		"no quota: the newest": {[]Sighting{
			see("grok", "ann@acme.dev", "SuperGrok", t0.Add(-time.Hour), true, time.Time{}),
			see("grok", "ann@acme.dev", "SuperGrok Plus", t0, true, time.Time{}),
		}, "SuperGrok Plus", t0},
		"a coarse plan takes the tier another device read": {[]Sighting{
			see("claude", "ann@acme.dev", "max", t0, true, t0),
			see("claude", "ann@acme.dev", "max 20x", t0.Add(-time.Hour), true, t0.Add(-2*time.Hour)),
		}, "max 20x", t0},
		"unknown and empty plans are no plan": {[]Sighting{
			see("codex", "ann@acme.dev", "unknown", t0, true, t0),
			see("codex", "ann@acme.dev", "", t0, true, t0),
			see("codex", "ann@acme.dev", "plus", t0.Add(-time.Hour), true, time.Time{}),
		}, "plus", t0.Add(-time.Hour)},
	} {
		plan, at := CurrentPlan(c.ss)
		if plan != c.want || !at.Equal(c.at) {
			t.Errorf("%s: CurrentPlan = %q at %v", name, plan, at)
		}
	}
}

func TestObservePlans(t *testing.T) {
	list := Builtin()
	s := &Store{V: StoreVersion}
	s.fill()
	ann := Key("claude", "Ann@Acme.dev")
	s.Observe(self, []Sighting{
		see("claude", "Ann@Acme.dev", "max", t0, true, t0),
		see("claude", "ann@acme.dev", "max", t0.Add(-time.Hour), false, time.Time{}),
		see("codex", "api key", "", t0, true, time.Time{}),
		see("codex", "kim@acme.dev", "free", t0, true, t0),
		see("hermes", "bot", "pro", t0, true, t0),
	}, nil, list, t0)
	if len(s.Subs) != 1 || s.Subs[ann] == nil {
		t.Fatalf("subscriptions %v", s.Subs)
	}
	sub := s.Subs[ann]
	if !s.Since.Equal(t0) || !sub.FirstSeen.Equal(t0) || !sub.LastSeen.Equal(t0) || !sub.PlanConfirmedAt.Equal(t0) {
		t.Fatalf("since %v, sub %+v", s.Since, sub)
	}
	// The first read of the tier refines the plan.
	s.Observe(self, []Sighting{see("claude", "ann@acme.dev", "max 20x", t0.Add(time.Hour), true, t0.Add(time.Hour))}, nil, list, t0.Add(time.Hour))
	if want := []PlanAt{{t0, "max 20x"}}; !reflect.DeepEqual(sub.Plans, want) {
		t.Fatalf("after the tier: %v", sub.Plans)
	}
	// An older device without the tier does not take it back, nor does a
	// stale read of another plan.
	s.Observe(self, []Sighting{see("claude", "ann@acme.dev", "max", t0.Add(2*time.Hour), true, time.Time{})}, nil, list, t0.Add(2*time.Hour))
	sub.notePlan(list, "pro", t0.Add(-time.Hour))
	if want := []PlanAt{{t0, "max 20x"}}; !reflect.DeepEqual(sub.Plans, want) {
		t.Fatalf("after an older device: %v", sub.Plans)
	}
	// A real change, then a rename of the plan.
	s.Observe(self, []Sighting{see("claude", "ann@acme.dev", "max 5x", t0.Add(3*time.Hour), true, t0.Add(3*time.Hour))}, nil, list, t0.Add(3*time.Hour))
	if len(sub.Plans) != 2 || sub.Plans[1] != (PlanAt{t0.Add(3 * time.Hour), "max 5x"}) {
		t.Fatalf("after a change: %v", sub.Plans)
	}
	g := &Sub{Provider: "grok", Plans: []PlanAt{{t0, "XPremiumPlus"}}}
	g.notePlan(list, "X Premium+", t0.Add(time.Hour))
	if want := []PlanAt{{t0, "X Premium+"}}; !reflect.DeepEqual(g.Plans, want) {
		t.Fatalf("a rename: %v", g.Plans)
	}
	for i := range 20 {
		g.notePlan(list, []string{"SuperGrok", "SuperGrok Plus"}[i%2], t0.Add(time.Duration(i+2)*time.Hour))
	}
	if len(g.Plans) != maxPlans || g.Plans[len(g.Plans)-1].Plan != "SuperGrok Plus" {
		t.Fatalf("history of %d", len(g.Plans))
	}
	if got := sub.PlanAt(t0.Add(time.Hour)); got != "max 20x" {
		t.Errorf("PlanAt between = %q", got)
	}
	if got := sub.PlanAt(t0.Add(-time.Hour)); got != "max 20x" {
		t.Errorf("PlanAt before the first = %q", got)
	}
	if got := sub.PlanAt(t0.Add(4 * time.Hour)); got != "max 5x" {
		t.Errorf("PlanAt after = %q", got)
	}
}

// A list price that changes for the same plan is recorded; a plan change
// with a new price is not a price change.
func TestObserveListPrice(t *testing.T) {
	cheap := Prices{V: 1, Updated: "2026-10-01", Currency: "USD", Plans: []Plan{{Provider: "codex", Plan: "plus", USD: 20}, {Provider: "codex", Plan: "pro", USD: 200}}}
	dear := cheap
	dear.Plans = []Plan{{Provider: "codex", Plan: "plus", USD: 25}, {Provider: "codex", Plan: "pro", USD: 200}}
	s := (&Store{}).fill()
	plus := []Sighting{see("codex", "ann@acme.dev", "plus", t0, true, t0)}
	s.Observe(self, plus, nil, cheap, t0)
	s.Observe(self, plus, nil, cheap, t0.Add(time.Hour))
	sub := s.Subs[Key("codex", "ann@acme.dev")]
	if len(sub.PriceChanges) != 0 || *sub.ListUSD != 20 {
		t.Fatalf("no change: %+v", sub)
	}
	s.Observe(self, plus, nil, dear, t0.Add(2*time.Hour))
	if want := []PriceChange{{t0.Add(2 * time.Hour), "plus", 20, 25}}; !reflect.DeepEqual(sub.PriceChanges, want) {
		t.Fatalf("price changes %v", sub.PriceChanges)
	}
	s.Observe(self, []Sighting{see("codex", "ann@acme.dev", "pro", t0.Add(3*time.Hour), true, t0.Add(3*time.Hour))}, nil, dear, t0.Add(3*time.Hour))
	s.Observe(self, []Sighting{see("codex", "ann@acme.dev", "team", t0.Add(4*time.Hour), true, t0.Add(4*time.Hour))}, nil, dear, t0.Add(4*time.Hour))
	if len(sub.PriceChanges) != 1 || sub.ListUSD != nil || sub.ListPlan != "team" {
		t.Fatalf("after plan changes: %+v", sub)
	}
}

// A subscription nobody logs in to any more stays; one that ended goes
// after Kept, and a cancel of it does not end the one read again after.
func TestObserveKeepsAndDrops(t *testing.T) {
	list := Builtin()
	s := (&Store{}).fill()
	seen := func(at time.Time) []Sighting {
		return []Sighting{see("codex", "ann@acme.dev", "pro", at, true, at), see("claude", "kim@acme.dev", "pro", at, true, at)}
	}
	s.Observe(self, seen(t0), nil, list, t0)
	later := t0.Add(200 * 24 * time.Hour)
	s.Observe(self, nil, nil, list, later)
	if len(s.Subs) != 2 {
		t.Fatal("a subscription gone from every device was dropped")
	}
	kim := s.Subs[Key("claude", "kim@acme.dev")]
	kim.Answer.Cancel(later)
	s.Observe(self, []Sighting{see("codex", "ann@acme.dev", "free", later, true, later)}, nil, list, later)
	if len(s.Subs) != 2 {
		t.Fatal("an ended subscription was dropped at once")
	}
	gone := later.Add(Kept + time.Hour)
	s.Observe(self, nil, nil, list, gone)
	if len(s.Subs) != 0 || len(s.Dropped) != 2 {
		t.Fatalf("after Kept: %d subscriptions, dropped %v", len(s.Subs), s.Dropped)
	}
	// Read again on a paid plan, with the old cancel still on the wire.
	heard := map[string]map[string]Answer{Key("claude", "kim@acme.dev"): {"d-other": {CancelledAt: later}}}
	s.Observe(self, seen(gone), heard, list, gone)
	kim = s.Subs[Key("claude", "kim@acme.dev")]
	if kim == nil || !kim.Reopened.Equal(later) || !kim.FirstSeen.Equal(gone) {
		t.Fatalf("read again: %+v", kim)
	}
	if _, ended := kim.EndedAt(self); ended {
		t.Fatal("the old cancel ended the new subscription")
	}
	if len(s.Dropped) != 0 {
		t.Fatalf("dropped %v", s.Dropped)
	}
	s.Observe(self, nil, nil, list, gone.Add(Kept))
	if s.Subs[Key("claude", "kim@acme.dev")] == nil {
		t.Fatal("the new subscription was dropped")
	}
	s.Dropped = map[string]time.Time{"x": gone}
	s.Observe(self, nil, nil, list, gone.Add(droppedKept+time.Hour))
	if len(s.Dropped) != 0 {
		t.Fatal("a dropped subscription was remembered too long")
	}
}

// An account a device logged out of long ago, as a device keeps it for 90
// days, makes no subscription; nor does one that was cancelled and dropped
// come back from the same old reading.
func TestObserveStale(t *testing.T) {
	list := Builtin()
	s := (&Store{}).fill()
	old := t0.Add(-60 * day)
	stale := []Sighting{see("claude", "ann@acme.dev", "pro", old, false, old), see("claude", "ann@acme.dev", "pro", t0, false, old)}
	s.Observe(self, stale, nil, list, t0)
	if len(s.Subs) != 0 {
		t.Fatalf("a stale account became a subscription: %+v", s.Subs)
	}
	// Logged out, read 20 days ago: a subscription, gone from every device.
	s.Observe(self, []Sighting{see("codex", "kim@acme.dev", "plus", t0, false, t0.Add(-20*day))}, nil, list, t0)
	kim := s.Subs[Key("codex", "kim@acme.dev")]
	if kim == nil || !kim.LastSeen.Equal(t0.Add(-20*day)) {
		t.Fatalf("kim %+v", kim)
	}
	if r := Compute(Input{Now: t0, List: list, Sub: kim, Provider: "codex", Label: "kim@acme.dev"}); r.State != StateUnconfirmed {
		t.Fatalf("gone 20 days: %s", r.State)
	}
	// Found today with a quota last read 60 days ago: unconfirmed at once.
	s.Observe(self, []Sighting{see("claude", "leo@acme.dev", "pro", t0, true, old)}, nil, list, t0)
	leo := s.Subs[Key("claude", "leo@acme.dev")]
	if r := Compute(Input{Now: t0, List: list, Sub: leo, Provider: "claude", Label: "leo@acme.dev"}); r.State != StateUnconfirmed {
		t.Fatalf("confirmed 60 days ago: %s", r.State)
	}
	// Cancelled 40 days ago and dropped: the reading it had before the
	// drop does not open it again, and one after does.
	leo.Answer.Cancel(t0.Add(-40 * day))
	s.Observe(self, nil, nil, list, t0)
	if s.Subs[Key("claude", "leo@acme.dev")] != nil {
		t.Fatal("the cancelled subscription was not dropped")
	}
	again := []Sighting{see("claude", "leo@acme.dev", "pro", t0, true, t0.Add(-20*day))}
	s.Observe(self, again, nil, list, t0.Add(time.Hour))
	if s.Subs[Key("claude", "leo@acme.dev")] != nil {
		t.Fatal("an old reading opened a dropped subscription again")
	}
	again[0].QuotaAt = t0.Add(time.Hour)
	s.Observe(self, again, nil, list, t0.Add(time.Hour))
	if sub := s.Subs[Key("claude", "leo@acme.dev")]; sub == nil || !sub.Reopened.Equal(t0.Add(-40*day)) {
		t.Fatalf("a new reading after the drop: %+v", sub)
	}
}

func TestObserveHeard(t *testing.T) {
	s := (&Store{}).fill()
	k := Key("codex", "ann@acme.dev")
	price := 180.0
	heard := map[string]map[string]Answer{
		k:                      {"d-other": {Price: &price, PriceAt: t0}, self: {CancelledAt: t0}, "d-empty": {}},
		Key("codex", "nobody"): {"d-other": {ConfirmedAt: t0}},
	}
	s.Observe(self, []Sighting{see("codex", "ann@acme.dev", "pro", t0, true, t0)}, heard, Builtin(), t0)
	sub := s.Subs[k]
	if len(s.Subs) != 1 || len(sub.Heard) != 1 || *sub.Heard["d-other"].Price != 180 {
		t.Fatalf("heard %v", sub.Heard)
	}
	sub.Answer.Confirm(t0.Add(time.Minute))
	if m := sub.Merged(self); *m.Price != 180 || !m.ConfirmedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("merged %+v", m)
	}
}

func TestAnswerEdits(t *testing.T) {
	var a Answer
	if !a.IsZero() || a.Cancelled() {
		t.Fatal("zero answer")
	}
	p := 180.0
	at := t0.Add(1500 * time.Millisecond)
	if err := a.SetPrice(&p, "max 20x", at); err != nil || a.PriceFor != "max 20x" || !a.PriceAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("SetPrice: %+v, %v", a, err)
	}
	for _, bad := range []float64{-1, MaxUSD + 1} {
		if err := a.SetPrice(&bad, "x", at); err == nil {
			t.Errorf("SetPrice(%v) took it", bad)
		}
	}
	if a.SetCadence("weekly", at) == nil || a.SetRenews("14", at) == nil || a.SetRenews("2026-02-30", at) == nil {
		t.Fatal("a bad cadence or day was taken")
	}
	if a.SetCadence(Yearly, at) != nil || a.SetRenews("2026-11-14", at) != nil {
		t.Fatal("a good cadence or day was refused")
	}
	a.Cancel(t0.Add(time.Hour))
	if !a.Cancelled() || !a.At().Equal(t0.Add(time.Hour)) {
		t.Fatal("Cancel")
	}
	a.Confirm(t0.Add(2 * time.Hour))
	if a.Cancelled() {
		t.Fatal("a confirm did not undo the cancel")
	}
	a.Reset(t0.Add(3 * time.Hour))
	if a.Price != nil || a.Cadence != "" || a.Renews != "" || !a.PriceAt.Equal(t0.Add(3*time.Hour)) || a.IsZero() {
		t.Fatalf("Reset: %+v", a)
	}
	sub := &Sub{Plans: []PlanAt{{t0, "plus"}}}
	sub.Skip(AskPrice)
	sub.Ask(AskRenewal, t0)
	if sub.Skipped[AskPrice] != "plus" || !sub.Asked[AskRenewal].Equal(t0) {
		t.Fatal("Skip or Ask")
	}
	s := (&Store{}).fill()
	if !s.Show("new-0a", t0) || s.Show("new-0a", t0.Add(time.Hour)) || !s.Shown["new-0a"].Equal(t0) {
		t.Fatal("Show")
	}
	s.Observe(self, nil, nil, Builtin(), t0.Add(EventsKept))
	if s.Show("new-0a", t0.Add(EventsKept)) {
		t.Fatal("an event shown a month ago was forgotten")
	}
	s.Observe(self, nil, nil, Builtin(), t0.Add(2*EventsKept+time.Hour))
	if len(s.Shown) != 0 {
		t.Fatal("an event shown long ago is remembered")
	}
}

func TestEditStore(t *testing.T) {
	dir := state.Dir(t.TempDir())
	if s, err := LoadStore(dir); err != nil || len(s.Subs) != 0 {
		t.Fatalf("LoadStore of nothing = %+v, %v", s, err)
	}
	_, err := EditStore(dir, func(s *Store) error {
		s.Sub("codex", "ann@acme.dev", t0).Answer.Confirm(t0)
		s.NeverAsk = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadStore(dir)
	if err != nil || !s.NeverAsk || s.Subs[Key("codex", "ANN@acme.dev")] == nil || s.V != StoreVersion {
		t.Fatalf("read back %+v, %v", s, err)
	}
	stop := errors.New("stop")
	if _, err := EditStore(dir, func(s *Store) error { s.NeverAsk = false; return stop }); !errors.Is(err, stop) {
		t.Fatalf("a failed edit: %v", err)
	}
	if s, _ := LoadStore(dir); !s.NeverAsk {
		t.Fatal("a failed edit was saved")
	}
	if err := os.WriteFile(dir.Path(storeFile), []byte(`{"v":2,"subscriptions":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EditStore(dir, func(*Store) error { return nil }); !errors.Is(err, ErrNewer) {
		t.Fatalf("a newer store: %v", err)
	}
	if err := os.WriteFile(dir.Path(storeFile), []byte(`{"v":1,`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStore(dir); err == nil {
		t.Fatal("a damaged store loaded")
	}
	if s, err := EditStore(dir, func(*Store) error { return nil }); err != nil || len(s.Subs) != 0 {
		t.Fatalf("an edit of a damaged store: %v", err)
	}
	if b, err := os.ReadFile(dir.Path(storeFile + ".bad")); err != nil || string(b) != `{"v":1,` {
		t.Fatalf("cost.json.bad: %q, %v", b, err)
	}
	if _, err := LoadStore(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir.Path(storeFile), []byte(`{"v":1,"subscriptions":{"x":{"provider":"codex","label":"Ann@acme.dev"},"y":null}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadStore(dir); err != nil || len(s.Subs) != 1 || s.Subs[Key("codex", "ann@acme.dev")] == nil {
		t.Fatalf("a store edited by hand: %v, %v", s.Subs, err)
	}
}
