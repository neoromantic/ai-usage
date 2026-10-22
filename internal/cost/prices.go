// Package cost is what the team's subscriptions cost: the price list, the
// answers people typed, the plans each subscription had, and the numbers a
// report shows from them. It is a budget, not accounting: prices are in USD
// a month, and a subscription's charge is the date a person confirmed.
package cost

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// PricesJSON is the price list built into this release. The relay serves
// its own copy at /v1/prices, so a fixed price reaches every collector
// without a release.
//
//go:embed prices.json
var PricesJSON []byte

// PricesVersion is the shape of the price list this build reads. A list of
// another version is not used.
const PricesVersion = 1

// MaxPricesBytes is the most of a price list a collector reads from a relay.
const MaxPricesBytes = 64 << 10

// MaxUSD is the highest price a list or a person may give a plan, a month or
// a year.
const MaxUSD = 100000

// Prices is a price list: the list price of each plan of each provider.
type Prices struct {
	V int `json:"v"`
	// Updated is the day the list was last checked, YYYY-MM-DD. Of two
	// lists, the one updated later is used.
	Updated  string `json:"updated"`
	Currency string `json:"currency"`
	Plans    []Plan `json:"plans"`
}

// Plan is one plan's list price. Plan is the plan as the provider's tool
// reports it, matched by Norm; Was lists the ids the provider used for the
// same plan before, so a rename is not a change of plan.
type Plan struct {
	Provider string `json:"provider"`
	Plan     string `json:"plan"`
	Name     string `json:"name,omitempty"`
	// USD is the price a month, billed monthly, and AnnualUSD the price a
	// year, when the plan is billed yearly too.
	USD       float64  `json:"usd"`
	AnnualUSD float64  `json:"annual_usd,omitempty"`
	Was       []string `json:"was,omitempty"`
}

// Monthly is the plan's price a month at cadence: a year's price over 12
// when it is billed yearly and the list has a yearly price, else the
// monthly one.
func (p Plan) Monthly(cadence string) float64 {
	if cadence == Yearly && p.AnnualUSD > 0 {
		return p.AnnualUSD / 12
	}
	return p.USD
}

// DisplayName is how the plan is called: its name, else its id.
func (p Plan) DisplayName() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Plan
}

var dayRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// ParsePrices reads a price list leniently: unknown keys are ignored, and so
// are plans of a provider this build does not know, so that a newer list
// still serves an older collector. A list of another version, without a
// valid updated day, not in USD, or with a plan whose price is out of range,
// is an error.
func ParsePrices(b []byte) (Prices, error) {
	var p Prices
	if len(b) > MaxPricesBytes {
		return p, fmt.Errorf("price list is %d bytes, limit %d", len(b), MaxPricesBytes)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return Prices{}, fmt.Errorf("price list: %w", err)
	}
	if p.V != PricesVersion {
		return Prices{}, fmt.Errorf("price list version %d, want %d", p.V, PricesVersion)
	}
	if _, err := parseDay(p.Updated); err != nil {
		return Prices{}, errors.New("price list has no updated day")
	}
	if p.Currency != "USD" {
		return Prices{}, errors.New("price list is not in USD")
	}
	plans := p.Plans[:0:0]
	for i, pl := range p.Plans {
		if !costProvider(pl.Provider) {
			continue
		}
		if err := pl.check(); err != nil {
			return Prices{}, fmt.Errorf("price list plans[%d]: %w", i, err)
		}
		plans = append(plans, pl)
	}
	p.Plans = plans
	return p, nil
}

func (p Plan) check() error {
	price := func(n float64) bool { return n >= 0 && n <= MaxUSD && !math.IsNaN(n) && !math.IsInf(n, 0) }
	switch {
	case Norm(p.Plan) == "" || snapshot.PlainLabel(p.Plan) != p.Plan:
		return errors.New("plan is not a short label")
	case snapshot.PlainLabel(p.Name) != p.Name:
		// A relay's list reaches every report: its words are plain.
		return errors.New("name is not a short label")
	case !price(p.USD) || !price(p.AnnualUSD):
		return errors.New("price out of range")
	case slices.ContainsFunc(p.Was, func(w string) bool { return Norm(w) == "" || snapshot.PlainLabel(w) != w }):
		return errors.New("was names a plan that is not a short label")
	}
	return nil
}

// costProvider says a provider's accounts are subscriptions with a price:
// Hermes borrows the quota of other logins.
func costProvider(p string) bool { return snapshot.KnownProvider(p) && p != "hermes" }

var builtin = sync.OnceValue(func() Prices {
	p, err := ParsePrices(PricesJSON)
	if err != nil {
		panic("built-in " + err.Error())
	}
	return p
})

// Builtin is the price list built into this release.
func Builtin() Prices { return builtin() }

// Norm is the key plans match by: lower case, + as "plus", and only letters
// and digits, so that "X Premium+" and "XPremiumPlus" are one plan.
func Norm(plan string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(plan) {
		switch {
		case r == '+':
			b.WriteString("plus")
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Free says a plan costs nothing: a subscription read on it has ended.
func Free(plan string) bool { return Norm(plan) == "free" }

// Find is the list's entry for a provider's plan, matched by Norm on its id
// and on the ids it was before.
func (p Prices) Find(provider, plan string) (Plan, bool) {
	key := Norm(plan)
	if key == "" {
		return Plan{}, false
	}
	for _, pl := range p.Plans {
		if pl.Provider != provider {
			continue
		}
		if Norm(pl.Plan) == key || slices.ContainsFunc(pl.Was, func(w string) bool { return Norm(w) == key }) {
			return pl, true
		}
	}
	return Plan{}, false
}

// Name is how a provider's plan is called: the list's name for it, else the
// plan as the tool reported it.
func (p Prices) Name(provider, plan string) string {
	if pl, ok := p.Find(provider, plan); ok {
		return pl.DisplayName()
	}
	return plan
}

// Tiers are the list's plans of a provider that are plan with a tier added,
// as "max 5x" and "max 20x" are "max", in the list's order: what a plan
// read without its tier may be.
func (p Prices) Tiers(provider, plan string) []Plan {
	var out []Plan
	for _, pl := range p.Plans {
		if pl.Provider == provider && Refines(plan, pl.Plan) {
			out = append(out, pl)
		}
	}
	return out
}

// Same says two plans a provider's tools reported are one: the same by Norm,
// one an older id of the other in the list, or one the other with its tier,
// as "max" and "max 20x" are.
func (p Prices) Same(provider, a, b string) bool {
	if Norm(a) == Norm(b) || Refines(a, b) || Refines(b, a) {
		return true
	}
	pa, okA := p.Find(provider, a)
	pb, okB := p.Find(provider, b)
	return okA && okB && pa.Plan == pb.Plan
}

var tierRe = regexp.MustCompile(`^[0-9]+x$`)

// Refines says fine is coarse with a tier added, as "max 20x" is "max": the
// first read of the tier refines the plan, and does not change it.
func Refines(coarse, fine string) bool {
	c, f := Norm(coarse), Norm(fine)
	return c != "" && strings.HasPrefix(f, c) && tierRe.MatchString(f[len(c):])
}

// Finer is the one of two spellings of the same plan that says more: the
// one with its tier, else b, the newer.
func Finer(a, b string) string {
	if Refines(b, a) {
		return a
	}
	return b
}

// parseDay reads a YYYY-MM-DD day.
func parseDay(s string) (time.Time, error) {
	if !dayRe.MatchString(s) {
		return time.Time{}, errors.New("not a day")
	}
	return time.Parse(time.DateOnly, s)
}
