package main

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/neoromantic/ai-usage/internal/cost"
)

// brands are the words a provider's store puts before a plan's name, as
// "ChatGPT Plus" is Codex's "Plus".
var brands = map[string]string{"claude": "Claude", "codex": "ChatGPT"}

// providerNames are the providers as people know them.
var providerNames = map[string]string{"claude": "Claude", "codex": "Codex", "grok": "Grok"}

// notPlanRe matches what a page sells that is not an individual plan: usage
// credits, and team, business, enterprise, and education plans, which the
// list leaves out.
var notPlanRe = regexp.MustCompile(`(?i)\b(credits?|team|business|enterprise|edu|education)\b`)

// report is what differs between the list and the providers' pages.
type report struct {
	Updated    string
	Differs    []difference
	New        []newPlan
	Missing    []listPlan
	Unreadable []unreadable
	Unchecked  []listPlan
}

// difference is a list price that no price on the plan's page matches.
type difference struct {
	Plan   listPlan
	Annual bool      // the year's price, else the month's
	Seen   []float64 // the plan's prices on the page that could be it
}

type newPlan struct {
	Provider string
	Offer    offer
	URL      string
}

type unreadable struct {
	URL    string
	Reason string
}

// findings says the maintainer has something to look at.
func (r report) findings() bool {
	return len(r.Differs)+len(r.New)+len(r.Missing)+len(r.Unreadable) > 0
}

// compare checks each plan of the list against the offers of its source
// page, and each page of a provider for plans the list lacks. A plan whose
// page has no reader is not checked; one whose page could not be read is
// left to that page's line in Unreadable.
func compare(l list, pages map[string]page) report {
	r := report{Updated: l.Updated}
	for _, p := range l.Plans {
		pg, ok := pages[p.Source]
		switch {
		case !ok || readerFor(p.Source) == nil:
			r.Unchecked = append(r.Unchecked, p)
			continue
		case pg.err != nil:
			continue
		}
		var offers []offer
		for _, o := range pg.offers {
			if matches(p, o.Name) {
				offers = append(offers, o)
			}
		}
		if len(offers) == 0 {
			r.Missing = append(r.Missing, p)
			continue
		}
		if seen, ok := priced(offers, p.USD, annual, p.AnnualUSD); !ok {
			r.Differs = append(r.Differs, difference{Plan: p, Seen: seen})
		}
		if p.AnnualUSD > 0 {
			if seen, ok := priced(offers, p.AnnualUSD, monthly, p.USD); !ok {
				r.Differs = append(r.Differs, difference{Plan: p, Annual: true, Seen: seen})
			}
		}
	}

	seen := map[string]bool{}
	for _, u := range sortedKeys(pages) {
		pg := pages[u]
		if pg.err != nil {
			r.Unreadable = append(r.Unreadable, unreadable{URL: u, Reason: pg.err.Error()})
			continue
		}
		for _, prov := range providersOf(l, u) {
			for _, o := range pg.offers {
				key := prov + "/" + cost.Norm(o.Name)
				if notPlanRe.MatchString(o.Name) || seen[key] || slices.ContainsFunc(l.Plans, func(p listPlan) bool {
					return p.Provider == prov && matches(p, o.Name)
				}) {
					continue
				}
				seen[key] = true
				r.New = append(r.New, newPlan{Provider: prov, Offer: o, URL: u})
			}
		}
	}
	return r
}

// priced says one of offers, of any period but other, has price usd, and
// else gives the prices they have, but for otherUSD, the plan's price for
// the other period, which a page that does not say the period also shows.
func priced(offers []offer, usd float64, other period, otherUSD float64) ([]float64, bool) {
	var seen []float64
	for _, o := range offers {
		if o.Period == other {
			continue
		}
		if same(o.USD, usd) {
			return nil, true
		}
		if !same(o.USD, otherUSD) && !slices.Contains(seen, o.USD) {
			seen = append(seen, o.USD)
		}
	}
	return seen, false
}

// same says two prices are one: the App Store's $19.99 is the web's $20.
func same(a, b float64) bool { return math.Abs(a-b) < 0.015 }

// matches says a name on a page is the plan: its name, its id, or an id it
// had, with or without the provider's brand before it.
func matches(p listPlan, name string) bool {
	n := cost.Norm(name)
	if n == "" {
		return false
	}
	for _, k := range append([]string{p.Name, p.Plan}, p.Was...) {
		if k != "" && (n == cost.Norm(k) || n == cost.Norm(brands[p.Provider]+k)) {
			return true
		}
	}
	return false
}

// providersOf are the providers whose plans a page names: those whose plans
// cite it, and the one whose store it is.
func providersOf(l list, u string) []string {
	var out []string
	for _, p := range l.Plans {
		if p.Source == u || stores[p.Provider] == u {
			out = append(out, p.Provider)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func sortedKeys(m map[string]page) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func usd(n float64) string {
	if n == math.Trunc(n) {
		return fmt.Sprintf("$%.0f", n)
	}
	return fmt.Sprintf("$%.2f", n)
}

func planName(p listPlan) string {
	name := p.Name
	if name == "" {
		name = p.Plan
	}
	return providerNames[p.Provider] + " " + name
}

func link(u string) string {
	return "[" + strings.TrimPrefix(u, "https://") + "](" + u + ")"
}

// cell keeps a page's words from breaking a Markdown table.
func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ", "\r", " ").Replace(s)
}

// markdown is the report as the issue shows it.
func (r report) markdown() string {
	var b strings.Builder
	if r.findings() {
		fmt.Fprintf(&b, "The price list, `internal/cost/prices.json` (updated %s), differs from the providers' pages, or a page could not be read.\n", r.Updated)
	} else {
		fmt.Fprintf(&b, "Every price checked in `internal/cost/prices.json` (updated %s) matches the providers' pages.\n", r.Updated)
	}
	if len(r.Differs) > 0 {
		b.WriteString("\n### Prices that differ\n\n| Plan | The list | The page | Page |\n| --- | --- | --- | --- |\n")
		for _, d := range r.Differs {
			list, per := d.Plan.USD, "a month"
			if d.Annual {
				list, per = d.Plan.AnnualUSD, "a year"
			}
			var seen []string
			for _, n := range d.Seen {
				seen = append(seen, usd(n))
			}
			shown := strings.Join(seen, ", ")
			if shown == "" {
				shown = "no price " + per
			}
			fmt.Fprintf(&b, "| %s | %s %s | %s | %s |\n", cell(planName(d.Plan)), usd(list), per, shown, link(d.Plan.Source))
		}
	}
	if len(r.New) > 0 {
		b.WriteString("\n### Paid plans the list lacks\n\n| Plan | Price on the page | Page |\n| --- | --- | --- |\n")
		for _, n := range r.New {
			per := ""
			switch n.Offer.Period {
			case monthly:
				per = " a month"
			case annual:
				per = " a year"
			}
			fmt.Fprintf(&b, "| %s %s | %s%s | %s |\n", providerNames[n.Provider], cell(n.Offer.Name), usd(n.Offer.USD), per, link(n.URL))
		}
	}
	if len(r.Missing) > 0 {
		b.WriteString("\n### Plans their page no longer shows\n\n| Plan | Page |\n| --- | --- |\n")
		for _, m := range r.Missing {
			fmt.Fprintf(&b, "| %s | %s |\n", cell(planName(m)), link(m.Source))
		}
	}
	if len(r.Unreadable) > 0 {
		b.WriteString("\n### Pages that could not be read\n\nThe prices on these pages were not checked.\n\n| Page | Why |\n| --- | --- |\n")
		for _, u := range r.Unreadable {
			fmt.Fprintf(&b, "| %s | %s |\n", link(u.URL), cell(u.Reason))
		}
	}
	if len(r.Unchecked) > 0 {
		b.WriteString("\n### Not checked\n\nNo reader knows these plans' pages; check them by hand.\n\n")
		for _, u := range r.Unchecked {
			fmt.Fprintf(&b, "- %s, %s a month: %s\n", planName(u), usd(u.USD), u.Source)
		}
	}
	if r.findings() {
		b.WriteString("\nFix the list as [Prices](https://github.com/neoromantic/ai-usage/blob/main/docs/releasing.md#prices) in docs/releasing.md says.\n")
	}
	return b.String()
}
