package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/neoromantic/ai-usage/internal/view"
)

// costOf is the cost object `cost --json` prints.
func (d *device) costOf() view.Cost {
	d.t.Helper()
	var c view.Cost
	if err := json.Unmarshal([]byte(d.ok("cost", "--json")), &c); err != nil {
		d.t.Fatal(err)
	}
	return c
}

// sub is the cost's one subscription.
func oneSub(t *testing.T, c view.Cost) view.CostSubscription {
	t.Helper()
	if len(c.Subscriptions) != 1 {
		t.Fatalf("subscriptions = %+v", c.Subscriptions)
	}
	return c.Subscriptions[0]
}

// TestCostCommands: a person types a price and a charge day, confirms,
// cancels, and resets, and the report shows each; a question skipped is not
// asked again, and never-ask asks none.
func TestCostCommands(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")

	c := d.costOf()
	s := oneSub(t, c)
	if s.Provider != "claude" || s.Account != "dev@example.com" || s.PriceSource != view.PriceUnknown || s.State != view.CostActive || !s.Current {
		t.Fatalf("before = %+v", s)
	}
	kinds := map[string]bool{}
	for _, q := range c.Questions {
		kinds[q.Kind] = true
		// Max without its tier is asked as one of its tiers.
		if q.Kind == view.AskPrice && (!slices.Equal(q.Choices, []string{"Max 5x $100", "Max 20x $200"}) || !strings.HasSuffix(q.Text, "Its tool did not say its tier: Max 5x $100 or Max 20x $200.")) {
			t.Fatalf("price question %+v", q)
		}
	}
	if !kinds[view.AskRenewal] || !kinds[view.AskPrice] {
		t.Fatalf("questions = %+v", c.Questions)
	}

	out := d.ok("cost", "set", "dev@example.com", "--price", "180", "--renews", "2099-01-03")
	if !strings.Contains(out, "claude dev@example.com: $180 a month for max, next charge") || !strings.Contains(out, "the team sees it") {
		t.Fatalf("set said %q", out)
	}
	c = d.costOf()
	s = oneSub(t, c)
	if s.PriceSource != view.PriceTyped || s.PriceUSD == nil || *s.PriceUSD != 180 || s.RenewsAt == nil || !s.RenewsConfirmed || c.TotalUSD != 180 {
		t.Fatalf("after set = %+v", s)
	}
	if len(c.Questions) != 0 {
		t.Fatalf("answered questions stay: %+v", c.Questions)
	}
	text := d.ok("cost", "--width", "100")
	if !strings.Contains(text, "COST  $180/mo") || !strings.Contains(text, "$180*") || !strings.Contains(text, "BY DEVICE") {
		t.Fatalf("cost:\n%s", text)
	}

	// Yearly: a year's price, a twelfth of it a month.
	d.ok("cost", "set", "claude:dev@example.com", "--price", "1200", "--yearly")
	if s = oneSub(t, d.costOf()); s.Cadence != "yearly" || *s.PriceUSD != 100 {
		t.Fatalf("yearly = %+v", s)
	}
	// A day of the month is no yearly charge day.
	if r := d.run("", "cost", "set", "dev@example.com", "--renews", "15"); r.code != 2 || !strings.Contains(r.stderr, "takes YYYY-MM-DD") {
		t.Fatalf("a day of the month, yearly: exit %d, %q", r.code, r.stderr)
	}
	// Billed monthly from now: the typed price is turned into a month's,
	// so the cost a month stays.
	if out := d.ok("cost", "set", "dev@example.com", "--monthly"); !strings.Contains(out, "billed monthly at $100 a month") {
		t.Fatalf("monthly said %q", out)
	}
	if s = oneSub(t, d.costOf()); s.Cadence != "monthly" || *s.PriceUSD != 100 {
		t.Fatalf("monthly = %+v", s)
	}
	d.ok("cost", "set", "claude:dev@example.com", "--price", "1200", "--yearly")

	if out := d.ok("cost", "cancel", "dev@example.com"); !strings.Contains(out, "is cancelled") {
		t.Fatalf("cancel said %q", out)
	}
	if c = d.costOf(); oneSub(t, c).State != view.CostEnded || c.TotalUSD != 0 {
		t.Fatalf("cancelled = %+v", c)
	}
	d.ok("cost", "confirm", "dev@example.com")
	if s = oneSub(t, d.costOf()); s.State != view.CostActive {
		t.Fatalf("confirmed = %+v", s)
	}
	d.ok("cost", "reset", "dev@example.com")
	c = d.costOf()
	if s = oneSub(t, c); s.PriceSource != view.PriceUnknown || s.RenewsAt != nil || s.Cadence != "monthly" {
		t.Fatalf("reset = %+v", s)
	}

	if out := d.ok("cost", "skip", "dev@example.com", "renewal"); !strings.Contains(out, "renewal question is not asked again") {
		t.Fatalf("skip said %q", out)
	}
	for _, q := range d.costOf().Questions {
		if q.Kind == view.AskRenewal {
			t.Fatalf("skipped question asked: %+v", q)
		}
	}
	d.ok("cost", "never-ask")
	if qs := d.costOf().Questions; len(qs) != 0 {
		t.Fatalf("never-ask still asks %+v", qs)
	}
	d.ok("cost", "never-ask", "--undo")
	if qs := d.costOf().Questions; len(qs) == 0 {
		t.Fatal("never-ask --undo asks nothing")
	}

	if out := d.ok("cost", "prices"); !strings.Contains(out, "price list of ") || !strings.Contains(out, "max 20x") {
		t.Fatalf("prices:\n%s", out)
	}
}

func TestCostCommandErrors(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	for _, c := range []struct {
		args []string
		code int
		says string
	}{
		{[]string{"cost", "set", "dev@example.com"}, 2, "cost set takes --price"},
		{[]string{"cost", "set", "dev@example.com", "--price", "lots"}, 2, "--price takes dollars"},
		{[]string{"cost", "set", "dev@example.com", "--monthly", "--yearly"}, 2, "not both"},
		{[]string{"cost", "set", "dev@example.com", "--renews", "32"}, 2, "from 1 to 31"},
		{[]string{"cost", "set", "dev@example.com", "--yearly", "--renews", "15"}, 2, "--renews takes YYYY-MM-DD"},
		{[]string{"cost", "set", "--price", "20"}, 2, "takes an account first"},
		{[]string{"cost", "skip", "dev@example.com", "later"}, 2, "renewal, price, or still_paid"},
		{[]string{"cost", "confirm", "nobody@example.com"}, 2, "no account this device knows"},
		{[]string{"cost", "renew", "dev@example.com"}, 2, `unknown cost command "renew"`},
	} {
		r := d.run("", c.args...)
		if r.code != c.code || !strings.Contains(r.stderr, c.says) {
			t.Errorf("%v: exit %d, %q", c.args, r.code, r.stderr)
		}
	}
}

// TestAskCost: the first run's questions, answered on the terminal: the
// suggestion taken with Enter, a skip, and never-ask, which ends them.
func TestAskCost(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	d.env()
	sugg := "2099-02-01"
	qs := []view.Question{
		{Kind: view.AskRenewal, Provider: "claude", Account: "dev@example.com", Text: "When does it charge?", Suggestion: &sugg},
		{Kind: view.AskPrice, Provider: "claude", Account: "dev@example.com", Text: "What does it cost?", Choices: []string{"Max 5x $100", "Max 20x $200"}},
		{Kind: view.AskStillPaid, Provider: "claude", Account: "dev@example.com", Text: "Still paid?"},
	}
	var out strings.Builder
	askCost(qs, strings.NewReader("\ns\nS\n"), &out)
	s := oneSub(t, d.costOf())
	if s.RenewsAt == nil || s.RenewsAt.Local().Format("2006-01-02") != sugg {
		t.Fatalf("Enter did not take the suggestion: %+v\n%s", s, out.String())
	}
	if !strings.Contains(out.String(), "price a month, in USD, as Max 5x $100 or Max 20x $200: ") || !strings.Contains(out.String(), "price question is not asked again") || !strings.Contains(out.String(), "asks nothing about the cost") {
		t.Fatalf("asked:\n%s", out.String())
	}
}

// TestAskCostYearly: the cadence is asked first, and yearly is offered the
// subscription's own date of the year.
func TestAskCostYearly(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	d.env()
	month, year := "2099-02-01", "2099-06-01"
	qs := []view.Question{{Kind: view.AskRenewal, Provider: "claude", Account: "dev@example.com", Text: "When does it charge?",
		Suggestion: &month, SuggestionYearly: &year, Choices: []string{"monthly", "yearly"}}}
	var out strings.Builder
	askCost(qs, strings.NewReader("y\n\n"), &out)
	s := oneSub(t, d.costOf())
	if s.Cadence != "yearly" || s.RenewsAt == nil || s.RenewsAt.Local().Format("2006-01-02") != year {
		t.Fatalf("yearly: %+v\n%s", s, out.String())
	}
	if !strings.Contains(out.String(), "next charge, YYYY-MM-DD; Enter takes "+year) {
		t.Fatalf("asked:\n%s", out.String())
	}
}
