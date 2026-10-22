package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The App Store reader takes the In-App Purchases from the page's data,
// once each, with the period a name gives, and skips a price not in USD.
func TestReadAppStore(t *testing.T) {
	got, err := readAppStore(fixture(t, "appstore.html"))
	if err != nil {
		t.Fatal(err)
	}
	want := []offer{
		{Name: "ChatGPT Plus", USD: 19.99},
		{Name: "ChatGPT Go", USD: 8},
		{Name: "100 Credits", USD: 4},
		{Name: "ChatGPT Plus", USD: 200},
		{Name: "ChatGPT Ultra", USD: 1200},
		{Name: "ChatGPT Team", USD: 30, Period: monthly},
		{Name: "ChatGPT Go", USD: 80, Period: annual},
	}
	if !slices.Equal(got, want) {
		t.Errorf("offers =\n%v\nwant\n%v", got, want)
	}
}

// A page that is not what a reader knows is an error, never no offers.
func TestReadersRefuseOtherPages(t *testing.T) {
	pages := map[string]string{
		"no data":      `<html><body>In-App Purchases $9.99</body></html>`,
		"broken data":  `<script type="application/json" id="serialized-server-data">{"data":[</script>`,
		"no purchases": `<script type="application/json" id="serialized-server-data">{"data":[{"title":"Size","summary":"1 MB"}]}</script>`,
		"no USD":       `<script type="application/json" id="serialized-server-data">{"x":{"title":"In-App Purchases","items":[{"leadingText":"Plus","trailingText":"€9.00"}]}}</script>`,
	}
	for name, p := range pages {
		if offers, err := readAppStore([]byte(p)); err == nil {
			t.Errorf("readAppStore(%s) = %v, want an error", name, offers)
		}
	}
	other := []byte(`<html><body><h3>Pro</h3><p>$20 a month</p><h3>Max</h3><p>Max 5x is $100</p></body></html>`)
	if offers, err := readClaudePricing(other); err == nil {
		t.Errorf("readClaudePricing = %v, want an error", offers)
	}
	if offers, err := readClaudeMax(other); err == nil {
		t.Errorf("readClaudeMax = %v, want an error", offers)
	}
}

// claude.com/pricing gives Pro's month and year from its own card, not a
// script's text, Max's "From", or Team's monthly price.
func TestReadClaudePricing(t *testing.T) {
	got, err := readClaudePricing(fixture(t, "claude-pricing.html"))
	if err != nil {
		t.Fatal(err)
	}
	want := []offer{{Name: "Pro", USD: 20, Period: monthly}, {Name: "Pro", USD: 200, Period: annual}}
	if !slices.Equal(got, want) {
		t.Errorf("offers = %v, want %v", got, want)
	}
}

func TestReadClaudeMax(t *testing.T) {
	got, err := readClaudeMax(fixture(t, "claude-max.html"))
	if err != nil {
		t.Fatal(err)
	}
	want := []offer{{Name: "Max 5x", USD: 100, Period: monthly}, {Name: "Max 20x", USD: 200, Period: monthly}}
	if !slices.Equal(got, want) {
		t.Errorf("offers = %v, want %v", got, want)
	}
}

func TestReaderFor(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://apps.apple.com/us/app/example/id123":                          true,
		"https://claude.com/pricing":                                           true,
		"https://support.claude.com/en/articles/11049741-what-is-the-max-plan": true,
		"https://support.claude.com/en/articles/123-something-else":            false,
		"http://apps.apple.com/us/app/example/id123":                           false,
		"https://news.example.com/x-premium-price":                             false,
		"https://claude.com/pricing/team":                                      false,
	} {
		if got := readerFor(u) != nil; got != ok {
			t.Errorf("readerFor(%s) = %v, want %v", u, got, ok)
		}
	}
}

const (
	store   = "https://apps.apple.com/us/app/chatgpt/id6448311069"
	pricing = "https://claude.com/pricing"
	news    = "https://news.example.com/premium"
)

func testList() list {
	return list{Updated: "2026-01-02", Plans: []listPlan{
		{Provider: "claude", Plan: "pro", Name: "Pro", USD: 20, AnnualUSD: 200, Source: pricing},
		{Provider: "codex", Plan: "plus", Name: "Plus", USD: 20, Source: store},
		{Provider: "codex", Plan: "go", Name: "Go", USD: 8, AnnualUSD: 80, Source: store},
		{Provider: "grok", Plan: "X Premium+", Name: "X Premium+", USD: 40, Source: news},
	}}
}

// Pages that agree with the list: the App Store's $19.99 is $20, a name
// with its brand is the plan, credits and team plans are not new plans,
// and a plan whose page has no reader is listed, but is no finding.
func TestCompareMatches(t *testing.T) {
	offers, err := readAppStore(fixture(t, "appstore.html"))
	if err != nil {
		t.Fatal(err)
	}
	offers = slices.DeleteFunc(offers, func(o offer) bool { return o.Name == "ChatGPT Ultra" })
	r := compare(testList(), map[string]page{
		store:   {offers: offers},
		pricing: {offers: []offer{{Name: "Pro", USD: 20, Period: monthly}, {Name: "Pro", USD: 200, Period: annual}}},
	})
	if r.findings() {
		t.Errorf("findings in\n%s", r.markdown())
	}
	if len(r.Unchecked) != 1 || r.Unchecked[0].Plan != "X Premium+" {
		t.Errorf("unchecked = %v, want X Premium+", r.Unchecked)
	}
	md := r.markdown()
	for _, s := range []string{"matches the providers' pages", "### Not checked", "Grok X Premium+, $40 a month: " + news} {
		if !strings.Contains(md, s) {
			t.Errorf("report has no %q:\n%s", s, md)
		}
	}
}

// A changed price, a new tier, a plan the page dropped, and a page that
// could not be read are each reported; the unreadable page's plans are not
// reported as changed.
func TestCompareFindings(t *testing.T) {
	l := testList()
	l.Plans = append(l.Plans, listPlan{Provider: "codex", Plan: "pro", Name: "Pro 200", USD: 200, Source: store})
	r := compare(l, map[string]page{
		store: {offers: []offer{
			{Name: "ChatGPT Plus", USD: 24.99},
			{Name: "ChatGPT Plus", USD: 200},
			{Name: "ChatGPT Go", USD: 8},
			{Name: "ChatGPT Go", USD: 90, Period: annual},
			{Name: "ChatGPT Go", USD: 7, Period: annual},
			{Name: "ChatGPT Ultra", USD: 1200},
			{Name: "500 Credits", USD: 20},
		}},
		pricing: {err: errors.New("HTTP 403")},
	})
	if !r.findings() {
		t.Fatal("no findings")
	}
	if len(r.Differs) != 2 || r.Differs[0].Plan.Plan != "plus" || r.Differs[0].Annual ||
		!slices.Equal(r.Differs[0].Seen, []float64{24.99, 200}) ||
		r.Differs[1].Plan.Plan != "go" || !r.Differs[1].Annual || !slices.Equal(r.Differs[1].Seen, []float64{90, 7}) {
		t.Errorf("differs = %+v", r.Differs)
	}
	if len(r.New) != 1 || r.New[0].Offer.Name != "ChatGPT Ultra" || r.New[0].Provider != "codex" {
		t.Errorf("new = %+v, want ChatGPT Ultra", r.New)
	}
	if len(r.Missing) != 1 || r.Missing[0].Name != "Pro 200" {
		t.Errorf("missing = %+v, want Pro 200", r.Missing)
	}
	if len(r.Unreadable) != 1 || r.Unreadable[0].URL != pricing {
		t.Errorf("unreadable = %+v, want %s", r.Unreadable, pricing)
	}
	md := r.markdown()
	for _, s := range []string{
		"| Codex Plus | $20 a month | $24.99, $200 |",
		"| Codex Go | $80 a year | $90, $7 |",
		"| Codex ChatGPT Ultra | $1200 |",
		"### Plans their page no longer shows",
		"| [claude.com/pricing](https://claude.com/pricing) | HTTP 403 |",
		"docs/releasing.md#prices",
	} {
		if !strings.Contains(md, s) {
			t.Errorf("report has no %q:\n%s", s, md)
		}
	}
	if strings.Contains(md, "Claude Pro") {
		t.Errorf("report names a plan of the unreadable page:\n%s", md)
	}
}

// A page's text cannot break the report's tables.
func TestCell(t *testing.T) {
	if got := cell("a|b\nc"); got != `a\|b c` {
		t.Errorf("cell = %q", got)
	}
}
