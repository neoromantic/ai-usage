package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/cost"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/relay"
)

// typed has a person on the device of dir say something of a subscription.
func typed(t *testing.T, dir state.Dir, provider, label string, edit func(*cost.Answer)) {
	t.Helper()
	if _, err := cost.EditStore(dir, func(s *cost.Store) error {
		edit(&s.Sub(provider, label, t0).Answer)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A device sends the costs people typed on it once a read shows every
// teammate reads them, and goes without them for a day when the relay
// refuses them, still sending the hours when the relay takes those.
func TestPublishSendsCostsBehindTheGate(t *testing.T) {
	for _, c := range []struct {
		name                     string
		teammate                 string
		rejectHours, rejectCosts bool
		second                   []string // what the second run put
	}{
		{"a teammate on v0.9.1", "v0.9.1", false, false, []string{"hours"}},
		{"everyone on v0.10.0", "v0.10.0", false, false, []string{"costs"}},
		{"a relay on v0.9.1", "v0.10.0", false, true, []string{"costs", "hours"}},
		{"a relay on v0.6.2", "v0.10.0", true, true, []string{"costs", "hours", "cycles"}},
	} {
		w, o := newWorld(t)
		o.Version = "v0.10.0"
		h := w.home(t, "claude")
		w.login("claude", h, "ann@acme.dev", quota(t0, 10))
		w.sessions("claude", h, sess("a4c2e917", "/p", 100, t0))
		typed(t, o.Dir, "claude", "ann@acme.dev", func(a *cost.Answer) { a.Confirm(t0.Add(-time.Hour)) })
		r, rec := newRecordingRelay(t, w)
		o.Relay = r.client(w)
		key, _ := LoadKey(o.Dir)
		publishRelease(t, r, w, key, "d-sambook-0002", c.teammate, ledger(), t0)
		rec.mu.Lock()
		rec.rejectHours, rec.rejectCosts = c.rejectHours, c.rejectCosts
		rec.mu.Unlock()

		// No read of the team yet: nothing vouches for the teammates.
		res := run(t, o)
		self := res.Config.Device
		if got := rec.wire(self, 0); !slices.Equal(got, []string{"legacy"}) {
			t.Fatalf("%s: the first run put %v", c.name, got)
		}
		if !res.Doc.HasCosts() {
			t.Fatalf("%s: the run's own snapshot has no costs", c.name)
		}

		w.now = t0.Add(15 * time.Minute)
		from := rec.count()
		res = run(t, o)
		if got := rec.wire(self, from); !slices.Equal(got, c.second) {
			t.Fatalf("%s: the second run put %v, want %v", c.name, got, c.second)
		}
		rs := res.State.Relay
		if rs.LastError != "" || rs.Pending || !rs.LastPushAt.Equal(w.now) {
			t.Fatalf("%s: relay state = %+v", c.name, rs)
		}
		var noCost time.Time
		if c.rejectCosts {
			noCost = w.now.Add(24 * time.Hour)
		}
		if !rs.NoCostUntil.Equal(noCost) {
			t.Fatalf("%s: no costs until %v", c.name, rs.NoCostUntil)
		}

		// A day without them after a refusal.
		w.now = t0.Add(30 * time.Minute)
		from = rec.count()
		run(t, o)
		if got, want := rec.wire(self, from), c.second[len(c.second)-1:]; !slices.Equal(got, want) {
			t.Fatalf("%s: the third run put %v, want %v", c.name, got, want)
		}
	}
	// A relay that refused the hours, or what an older release added,
	// refuses the costs with them.
	for _, r := range []state.Relay{
		{NoCostUntil: t0.Add(time.Hour)},
		{NoHoursUntil: t0.Add(time.Hour)},
		{NoExtUntil: t0.Add(time.Hour), NoCostUntil: t0.Add(-time.Hour)},
	} {
		if got := NoCostUntil(r); !got.Equal(t0.Add(time.Hour)) {
			t.Errorf("NoCostUntil(%+v) = %v", r, got)
		}
	}
	if !BeforeCost("v0.9.1") || BeforeCost("v0.10.0") || !BeforeCost("v0.9.9-local") || BeforeCost("devel") {
		t.Error("BeforeCost")
	}
}

// A run records the team's subscriptions in the cost store: this device's
// and those of each teammate's snapshot, with what the teammate's people
// said of them; and its own snapshot carries what was typed here.
func TestRunNotesTheTeamsCosts(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.readings[state.Key("claude", h)] = probe.Reading{Account: "ann@acme.dev", Plan: "max 20x", Quota: quota(t0.Add(-time.Minute), 10)}
	r := newRelay(t, w)
	o.Relay = r.client(w)
	key, _ := LoadKey(o.Dir)

	// The teammate's people typed a price for its subscription.
	mate := t.TempDir()
	typed(t, state.Dir(mate), "claude", "ann@example.com", func(a *cost.Answer) {
		p := 180.0
		_ = a.SetPrice(&p, "max", t0.Add(-time.Hour))
	})
	store, err := cost.LoadStore(state.Dir(mate))
	if err != nil {
		t.Fatal(err)
	}
	doc := BuildDoc(ledger(), nil, key, state.Config{Device: "d-sambook-0002"}, "otherbox", "kim", "v0.10.0", t0)
	withCosts(&doc, store, key)
	body, _ := json.Marshal(doc)
	c := &relay.Client{BaseURL: r.srv.URL, HTTP: r.srv.Client(), Key: key, Now: func() time.Time { return w.now }}
	if err := c.Publish(context.Background(), doc.Device, body); err != nil {
		t.Fatal(err)
	}
	typed(t, o.Dir, "claude", "ann@acme.dev", func(a *cost.Answer) { _ = a.SetRenews("2026-10-14", t0.Add(-time.Hour)) })

	res := run(t, o)
	s, err := cost.LoadStore(o.Dir)
	if err != nil {
		t.Fatal(err)
	}
	own, mates := s.Subs[cost.Key("claude", "ann@acme.dev")], s.Subs[cost.Key("claude", "ann@example.com")]
	if own == nil || mates == nil || len(s.Subs) != 2 {
		t.Fatalf("subscriptions %v", s.Subs)
	}
	if own.Plan() != "max 20x" || !own.LastSeen.Equal(t0) || !own.PlanConfirmedAt.Equal(t0.Add(-time.Minute)) || own.Answer.Renews != "2026-10-14" {
		t.Fatalf("this device's %+v", own)
	}
	if mates.Plan() != "max" || !mates.LastSeen.Equal(t0) || !mates.PlanConfirmedAt.Equal(t0) || mates.Heard["d-sambook-0002"].Price == nil {
		t.Fatalf("the teammate's %+v", mates)
	}
	if m := mates.Merged(res.Config.Device); *m.Price != 180 || m.PriceFor != "max" {
		t.Fatalf("merged %+v", m)
	}
	if len(res.Doc.Costs) != 1 || res.Doc.Costs[0].Provider != "claude" {
		t.Fatalf("own costs %+v", res.Doc.Costs)
	}
	label, _ := key.Open(res.Doc.Costs[0].Label)
	data, _ := key.Open(res.Doc.Costs[0].Data)
	if a := cost.DecodeAnswer(data, t0); label != "ann@acme.dev" || a.Renews != "2026-10-14" {
		t.Fatalf("own cost %s: %s", label, data)
	}
	if res.State.LastError != "" {
		t.Fatalf("last error %q", res.State.LastError)
	}
}

// The relay's price list is asked for at most once a day, and a relay that
// has none says nothing of it.
func TestRunFetchesPrices(t *testing.T) {
	for _, missing := range []bool{false, true} {
		w, o := newWorld(t)
		h := w.home(t, "claude")
		w.login("claude", h, "ann@acme.dev", quota(t0, 10))
		r := newRelay(t, w)
		var mu sync.Mutex
		asks := 0
		list := `{"v":1,"updated":"2099-01-01","currency":"USD","plans":[{"provider":"claude","plan":"pro","usd":21}]}`
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/v1/prices" {
				mu.Lock()
				asks++
				mu.Unlock()
				if missing {
					http.NotFound(rw, req)
					return
				}
				_, _ = rw.Write([]byte(list))
				return
			}
			r.srv.Config.Handler.ServeHTTP(rw, req)
		}))
		t.Cleanup(srv.Close)
		o.Relay = &relay.Client{BaseURL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return w.now }}
		for _, at := range []time.Duration{0, time.Hour, 23 * time.Hour, 24 * time.Hour} {
			w.now = t0.Add(at)
			res := run(t, o)
			if res.State.LastError != "" || res.State.Relay.LastError != "" {
				t.Fatalf("missing %v: last error %q, relay %q", missing, res.State.LastError, res.State.Relay.LastError)
			}
		}
		if asks != 2 {
			t.Fatalf("missing %v: asked %d times", missing, asks)
		}
		p, src := cost.Current(o.Dir)
		if pl, _ := p.Find("claude", "pro"); missing != (src == cost.SourceBuiltin) || !missing && pl.USD != 21 {
			t.Fatalf("missing %v: %s list, pro at %v", missing, src, pl.USD)
		}
	}
	// No relay, no ask.
	w, o := newWorld(t)
	_ = w
	run(t, o)
	if !cost.FetchDue(o.Dir, t0) {
		t.Fatal("a run without a relay asked for prices")
	}
}

// The costs are the newest answers, as many as a snapshot holds, sealed;
// those that do not fit go after the projects, and no account goes for
// them.
func TestCostsFit(t *testing.T) {
	key := mustKey(t)
	s := &cost.Store{V: cost.StoreVersion, Subs: map[string]*cost.Sub{}}
	for i := range snapshot.MaxCosts + 6 {
		sub := s.Sub("codex", fmt.Sprintf("user-%02d@acme.dev", i), t0)
		sub.Answer.Confirm(t0.Add(time.Duration(i) * time.Minute))
	}
	s.Sub("hermes", "bot", t0).Answer.Confirm(t0.Add(time.Hour))
	s.Sub("grok", "said-nothing@acme.dev", t0)
	doc := snapshot.Doc{Accounts: []snapshot.Account{}}
	withCosts(&doc, s, key)
	if len(doc.Costs) != snapshot.MaxCosts {
		t.Fatalf("%d costs", len(doc.Costs))
	}
	for i, c := range doc.Costs {
		label, err := key.Open(c.Label)
		want := fmt.Sprintf("user-%02d@acme.dev", snapshot.MaxCosts+5-i)
		if err != nil || label != want || c.Provider != "codex" || !c.At.Equal(t0.Add(time.Duration(snapshot.MaxCosts+5-i)*time.Minute)) {
			t.Fatalf("cost %d is %q at %v", i, label, c.At)
		}
		data, err := key.Open(c.Data)
		if a := cost.DecodeAnswer(data, t0.Add(time.Hour)); err != nil || !a.ConfirmedAt.Equal(c.At) {
			t.Fatalf("cost %d data %q", i, data)
		}
	}
	if withCosts(&doc, nil, key); doc.Costs != nil {
		t.Fatal("no store, costs")
	}

	// Documents that fill the limit without their costs: the projects go
	// first, then the oldest costs.
	big := strings.Repeat("A", snapshot.MaxSealed)
	full := func(projects int) snapshot.Doc {
		doc := snapshot.Doc{Accounts: []snapshot.Account{}}
		a := snapshot.Account{Provider: "claude", Label: big, Windows: []snapshot.Window{}, Projects: []snapshot.Project{}}
		for range projects {
			a.Projects = append(a.Projects, snapshot.Project{Path: big})
		}
		doc.Accounts = append(doc.Accounts, a)
		for b, _ := json.Marshal(doc); len(b) < snapshot.MaxBytes-1000; b, _ = json.Marshal(doc) {
			doc.Accounts = append(doc.Accounts, snapshot.Account{Provider: "claude", Label: big, Windows: []snapshot.Window{}, Projects: []snapshot.Project{}})
		}
		return doc
	}
	for _, projects := range []int{30, 0} {
		doc := full(projects)
		accounts := len(doc.Accounts)
		withCosts(&doc, s, key)
		b, _ := json.Marshal(doc)
		kept := len(doc.Accounts[0].Projects)
		if len(b) > snapshot.MaxBytes || len(doc.Accounts) != accounts || len(doc.Costs) == 0 {
			t.Fatalf("%d projects: %d bytes, %d accounts, %d costs", projects, len(b), len(doc.Accounts), len(doc.Costs))
		}
		if projects > 0 && (kept == projects || kept == 0 || len(doc.Costs) != snapshot.MaxCosts) {
			t.Fatalf("%d projects: kept %d projects and %d costs", projects, kept, len(doc.Costs))
		}
		if label, _ := key.Open(doc.Costs[0].Label); projects == 0 && (len(doc.Costs) == snapshot.MaxCosts || label != fmt.Sprintf("user-%02d@acme.dev", snapshot.MaxCosts+5)) {
			t.Fatalf("without projects: %d costs, the first %q", len(doc.Costs), label)
		}
	}
}

// A read without the tier keeps the plan read with it; who bills the
// subscription and when it began are kept as the tool last said.
func TestReadingKeepsTheTier(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	began := time.Date(2025, 3, 9, 10, 0, 0, 0, time.UTC)
	k := state.Key("claude", h)
	for i, c := range []struct {
		r    probe.Reading
		plan string
	}{
		{probe.Reading{Account: "ann@acme.dev", Plan: "max 20x", Billing: "apple_subscription", SubscribedAt: began}, "max 20x"},
		{probe.Reading{Account: "ann@acme.dev", Plan: "max"}, "max 20x"},
		{probe.Reading{Account: "ann@acme.dev", Plan: "pro"}, "pro"},
	} {
		w.readings[k] = c.r
		w.now = t0.Add(time.Duration(i) * time.Hour)
		res := run(t, o)
		a := res.State.Accounts[state.Key("claude", "ann@acme.dev")]
		if a.Plan != c.plan || a.Billing != "apple_subscription" || !a.SubscribedAt.Equal(began) {
			t.Fatalf("read %d: %+v", i, a)
		}
	}
}
