package view

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/cost"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// NewsFor is how long a new subscription, a change of plan, or a change of
// a list price stays in ATTENTION.
const NewsFor = 3 * 24 * time.Hour

// Cost is what the team's subscriptions cost a month, how much of it the
// team used over the last 4 weeks, and who used it. It is a budget, not
// accounting: prices are the list's or the ones people typed, in USD a
// month.
type Cost struct {
	Currency string `json:"currency"`
	// TotalUSD is the price a month of every subscription that has not
	// ended and has a price, and WithoutPrice counts those that have none.
	TotalUSD     float64 `json:"total_usd"`
	WithoutPrice int     `json:"without_price"`
	// UsedUSD and UnusedUSD add up the subscriptions whose Used is known,
	// and UsedPct is UsedUSD over their prices; all nil when none is.
	// UsedEstimated says an estimate is among them.
	UsedUSD       *float64 `json:"used_usd"`
	UsedPct       *float64 `json:"used_pct"`
	UnusedUSD     *float64 `json:"unused_usd"`
	UsedEstimated bool     `json:"used_estimated"`
	// NextCharge is the soonest charge a person confirmed.
	NextCharge *Charge `json:"next_charge"`
	Prices     Prices  `json:"prices"`
	// Subscriptions are by provider, in the order of SUBSCRIPTIONS, then
	// the ones no device has any more, then those that ended.
	Subscriptions []CostSubscription `json:"subscriptions"`
	// Devices are what each device used of the subscriptions, the most
	// first.
	Devices   []CostDevice `json:"devices"`
	Questions []Question   `json:"questions"`
	Events    []CostEvent  `json:"events"`
}

// Prices is the price list in use: the day it was last checked, and where
// it came from, "built in" or "relay".
type Prices struct {
	Updated string `json:"updated"`
	Source  string `json:"source"`
}

// Charge is a subscription's next charge.
type Charge struct {
	Provider string    `json:"provider"`
	Account  string    `json:"account"`
	At       time.Time `json:"at"`
	USD      *float64  `json:"usd"`
}

// Where a subscription's price comes from, in CostSubscription.PriceSource.
const (
	PriceList    = cost.PriceList
	PriceTyped   = cost.PriceTyped
	PriceStale   = cost.PriceStale
	PriceUnknown = cost.PriceUnknown
)

// A subscription's state, in CostSubscription.State.
const (
	CostActive      = cost.StateActive
	CostIdle        = cost.StateIdle
	CostUnconfirmed = cost.StateUnconfirmed
	CostEnded       = cost.StateEnded
)

// CostSubscription is one subscription's cost.
type CostSubscription struct {
	Provider string `json:"provider"`
	Account  string `json:"account"`
	// Name and Alias are as on the team account; a subscription no device
	// has any more goes by its short name.
	Name  string  `json:"name"`
	Alias *string `json:"alias"`
	// Current says it is logged in on this device.
	Current bool `json:"current"`
	// Plan is its plan as its tool reports it, and PlanName as the price
	// list calls it.
	Plan     *string `json:"plan"`
	PlanName *string `json:"plan_name"`
	// PriceUSD is its price a month, from PriceSource: the list, a person
	// (typed), the list beside a price typed for another plan, TypedFor
	// (stale), or nowhere (unknown, and PriceUSD nil).
	PriceUSD    *float64 `json:"price_usd"`
	PriceSource string   `json:"price_source"`
	TypedFor    *string  `json:"typed_for"`
	// Cadence is how it is billed: monthly, or yearly when a person said.
	Cadence string `json:"cadence"`
	// RenewsAt is the next charge, midnight of its day in this device's
	// time zone, which RenewsConfirmed says a person confirmed; nil when
	// nobody did. SuggestedRenewalDay is the day of the month its tool says
	// it began, offered when asking, nil when none.
	RenewsAt            *time.Time `json:"renews_at"`
	RenewsConfirmed     bool       `json:"renews_confirmed"`
	SuggestedRenewalDay *int       `json:"suggested_renewal_day"`
	// UsedPct is how full its main window got over the last 4 weeks, and
	// UsedUSD that part of what it cost; UnusedUSD is the rest of its
	// price. UsedEstimated says UsedUSD is its whole price, split by
	// tokens, as no week of its Used is known.
	UsedUSD       *float64 `json:"used_usd"`
	UsedPct       *float64 `json:"used_pct"`
	UsedEstimated bool     `json:"used_estimated"`
	UnusedUSD     *float64 `json:"unused_usd"`
	// State is active, idle, unconfirmed, or ended; IdleSince,
	// UnconfirmedSince, and EndedAt say since when, as it says.
	State            string     `json:"state"`
	IdleSince        *time.Time `json:"idle_since"`
	UnconfirmedSince *time.Time `json:"unconfirmed_since"`
	EndedAt          *time.Time `json:"ended_at"`
	// PlanConfirmedAt is the team's newest reading of its quota, which
	// proves the paid plan was live then; FirstSeen is when this device
	// first found it, LastSeen the newest report of a device that had it
	// logged in.
	PlanConfirmedAt *time.Time `json:"plan_confirmed_at"`
	FirstSeen       *time.Time `json:"first_seen"`
	LastSeen        *time.Time `json:"last_seen"`
	// BilledThrough is the store that bills it, when not its provider:
	// "App Store" or "Google Play", where its price may differ.
	BilledThrough *string `json:"billed_through"`
	// Consumers split UsedUSD by their weighted tokens over the 4 weeks,
	// the most first. USDPerMWeighted is its price a month for a million
	// of them.
	Consumers       []CostConsumer `json:"consumers"`
	USDPerMWeighted *float64       `json:"usd_per_m_weighted"`
	// Plans are the plans it had, oldest first, each with when it was
	// first read.
	Plans []PlanAt `json:"plans"`
}

// CostConsumer is a device's part of a subscription's Used: its share of
// the weighted tokens over the last 4 weeks, in percent, and that part of
// the used dollars, nil when the price is unknown.
type CostConsumer struct {
	Device   string   `json:"device"`
	DeviceID string   `json:"device_id"`
	USD      *float64 `json:"usd"`
	Share    float64  `json:"share"`
}

// CostDevice is what a device used of every subscription that has not
// ended, nil when no price of them is known.
type CostDevice struct {
	Device   string   `json:"device"`
	DeviceID string   `json:"device_id"`
	USD      *float64 `json:"usd"`
}

// PlanAt is a plan, and when it was first read.
type PlanAt struct {
	At   time.Time `json:"at"`
	Plan string    `json:"plan"`
}

// The questions of a subscription, in Question.Kind.
const (
	AskRenewal   = cost.AskRenewal
	AskPrice     = cost.AskPrice
	AskStillPaid = cost.AskStillPaid
)

// Question is what this device asks a person of a subscription. Its ID
// stays the same while the question stands.
type Question struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Account  string `json:"account"`
	// Text is the question in words.
	Text string `json:"text"`
	// Suggestion is the answer offered, a YYYY-MM-DD day for a renewal at
	// the cadence it has now; nil when there is none. SuggestionYearly is
	// the one to offer when a person chooses yearly from Choices.
	Suggestion       *string `json:"suggestion"`
	SuggestionYearly *string `json:"suggestion_yearly"`
	// Choices are the cadences to choose from, for a renewal of a plan the
	// list prices yearly too; for the price of a plan read without its
	// tier, the list's tiers of it, each its name and price a month, as
	// "Max 5x $100", whose price an answer types.
	Choices []string `json:"choices"`
}

// The events of a subscription, in CostEvent.Kind.
const (
	EventNew          = cost.EventNew
	EventPlanChanged  = cost.EventPlanChanged
	EventPriceChanged = cost.EventPriceChanged
	EventEnded        = cost.EventEnded
	EventChargesSoon  = cost.EventChargesSoon
	EventUnconfirmed  = cost.EventUnconfirmed
)

// CostEvent is something that happened to a subscription in the last 30
// days, with an ID that stays the same in every report, so that a reader
// tells it once.
type CostEvent struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Provider string    `json:"provider"`
	Account  string    `json:"account"`
	// From and To are the plans of a change, To the plan of a new
	// subscription and of a charge, and FromUSD and ToUSD their prices a
	// month, a list price change's the list's; DeltaUSD is how much a month
	// it changed the cost by. Each is nil when not known.
	From     *string  `json:"from"`
	To       *string  `json:"to"`
	FromUSD  *float64 `json:"from_usd"`
	ToUSD    *float64 `json:"to_usd"`
	DeltaUSD *float64 `json:"delta_usd"`
	Text     string   `json:"text"`
}

// costSeen is what the team found of each of its subscriptions, for their
// cost: the plan, whether it is logged in here, the periods of its main
// window, and each device's weighted tokens on it by week, what Hermes spent
// through a login included, by device id. The unknown account is no
// subscription to pay for.
func (m *merger) costSeen(t Team) []cost.Seen {
	tokens := map[*teamAccount]map[string][cost.Slots]int64{}
	for _, dv := range m.devs {
		for _, a := range dv.accts {
			x := billsTo(a, m.byProv)
			if x == nil || !x.ta.Subscription {
				continue
			}
			if tokens[x] == nil {
				tokens[x] = map[string][cost.Slots]int64{}
			}
			s := tokens[x][dv.dev.Device]
			for i, n := range cost.SlotTokens(a.burnDays, dv.collectedAt, m.now) {
				s[i] += n
			}
			tokens[x][dv.dev.Device] = s
		}
	}
	var out []cost.Seen
	for _, tp := range t.Providers {
		for _, a := range tp.Accounts {
			if !a.Subscription || a.Label == collect.UnknownAccount {
				continue
			}
			g := cost.Seen{Provider: tp.Provider, Label: a.Label, Here: a.Current, IdleSince: idleSince(a, m.now)}
			if a.Plan != nil {
				g.Plan = *a.Plan
			}
			if a.Quota != nil {
				g.QuotaAt = a.Quota.ObservedAt
			}
			g.LoggedIn = slices.ContainsFunc(a.PerDevice, func(d DeviceUsage) bool { return d.Current })
			if w := costWindow(a); w != "" {
				for _, u := range a.Utilization {
					if u.Window != w {
						continue
					}
					for _, c := range u.Cycles {
						g.Cycles = append(g.Cycles, cost.Cycle{ResetsAt: c.ResetsAt, Percent: c.Percent})
					}
				}
			}
			byDev := tokens[m.byProv[tp.Provider][snapshot.LabelKey(a.Label)]]
			for _, id := range slices.Sorted(maps.Keys(byDev)) {
				g.Consumers = append(g.Consumers, cost.Consumer{Name: id, Slots: byDev[id]})
			}
			if a.Current {
				g.Billing, g.SubscribedAt = m.billing(tp.Provider, a.Label)
			}
			out = append(out, g)
		}
	}
	return out
}

// costWindow is the window an account's Used is counted in, of those its
// reading and its history name: the weekly one, else the longest. A window
// shorter than a day, as Claude's 5-hour one, fills and empties many times
// a week and says nothing of how much of the week was used, so it is never
// the one: "" when the account has no other. A week whose window has not
// reset yet is then not known, rather than taken from a shorter window.
func costWindow(a TeamAccount) string {
	var rs []reading
	if a.Quota != nil {
		for _, w := range a.Quota.Windows {
			rs = append(rs, reading{Window: snapshot.Window{Name: w.Name, Minutes: w.Minutes}})
		}
	}
	for _, u := range a.Utilization {
		rs = append(rs, reading{Window: snapshot.Window{Name: u.Window}})
	}
	rs = slices.DeleteFunc(rs, func(r reading) bool { return r.Length() > 0 && r.Length() < 24*time.Hour })
	if i := mainIndex(rs); i >= 0 {
		return rs[i].Name
	}
	return ""
}

// billing is who bills this device's account of provider with label, and
// when its tool says it began, as this device read them.
func (m *merger) billing(provider, label string) (string, time.Time) {
	for _, a := range m.in.State.Accounts {
		if a != nil && a.Provider == provider && snapshot.LabelKey(a.Label) == snapshot.LabelKey(label) {
			return a.Billing, a.SubscribedAt
		}
	}
	return "", time.Time{}
}

// buildCost is the cost of the team's subscriptions: those this device's
// cost store keeps, and those the team found on a paid plan, seen, with the
// price list in, as of now in loc.
func buildCost(in Input, t Team, seen []cost.Seen, now time.Time, loc *time.Location) *Cost {
	list, source := in.Prices, in.PricesFrom
	if list.V == 0 {
		list, source = cost.Builtin(), cost.SourceBuiltin
	}
	b := cost.Build(in.Cost, list, source, in.Config.Device, seen, now, loc)
	c := &Cost{
		Currency:      cmp.Or(list.Currency, "USD"),
		TotalUSD:      b.Totals.USD,
		WithoutPrice:  b.Totals.WithoutPrice,
		UsedEstimated: b.Totals.UsedEstimated,
		Prices:        Prices{Updated: list.Updated, Source: source},
		Subscriptions: []CostSubscription{},
		Devices:       []CostDevice{},
		Questions:     []Question{},
		Events:        []CostEvent{},
	}
	if b.Totals.UsedPct != nil {
		used, unused, pct := b.Totals.UsedUSD, b.Totals.UnusedUSD, *b.Totals.UsedPct
		c.UsedUSD, c.UnusedUSD, c.UsedPct = &used, &unused, &pct
	}
	devName := map[string]string{}
	for _, d := range t.Devices {
		devName[d.Device] = d.Name
	}
	name := func(id string) string { return cmp.Or(devName[id], id) }
	for _, r := range b.Subs {
		c.Subscriptions = append(c.Subscriptions, costSubscription(t, r, name))
	}
	sortCost(t, c.Subscriptions)
	for _, s := range b.Consumers {
		c.Devices = append(c.Devices, CostDevice{Device: name(s.Name), DeviceID: s.Name, USD: s.USD})
	}
	for _, s := range c.Subscriptions {
		if s.RenewsAt != nil && s.State != CostEnded && (c.NextCharge == nil || s.RenewsAt.Before(c.NextCharge.At)) {
			c.NextCharge = &Charge{Provider: s.Provider, Account: s.Account, At: *s.RenewsAt, USD: chargeUSD(s)}
		}
	}
	for _, q := range b.Questions {
		qv := Question{ID: q.ID, Kind: q.Kind, Provider: q.Provider, Account: q.Label, Suggestion: strPtr(q.Suggestion),
			SuggestionYearly: strPtr(q.SuggestionYearly), Choices: q.Choices}
		for _, pl := range q.Tiers {
			qv.Choices = append(qv.Choices, pl.DisplayName()+" "+usd(pl.USD))
		}
		if qv.Choices == nil {
			qv.Choices = []string{}
		}
		c.Questions = append(c.Questions, qv)
	}
	for _, e := range b.Events {
		c.Events = append(c.Events, CostEvent{ID: e.ID, Kind: e.Kind, At: e.At.UTC(), Provider: e.Provider, Account: e.Label,
			From: strPtr(e.From), To: strPtr(e.To), FromUSD: e.FromUSD, ToUSD: e.ToUSD, DeltaUSD: e.DeltaUSD()})
	}
	return c
}

// costSubscription is one subscription's numbers r as the report has them,
// with each device by the name that names it.
func costSubscription(t Team, r cost.Result, name func(id string) string) CostSubscription {
	s := CostSubscription{
		Provider: r.Provider, Account: r.Label, Name: ShortName(r.Label),
		Plan: strPtr(r.Plan), PlanName: strPtr(r.PlanName),
		PriceUSD: r.Price.USD, PriceSource: r.Price.Source, TypedFor: strPtr(r.Price.StaleFor),
		Cadence: r.Cadence, RenewsAt: timeOf(r.NextCharge), RenewsConfirmed: r.NextCharge != nil,
		UsedUSD: r.UsedUSD, UsedPct: r.UsedPct, UsedEstimated: r.UsedEstimated, UnusedUSD: r.UnusedUSD,
		State: r.State, IdleSince: timeOf(r.IdleSince), UnconfirmedSince: timeOf(r.UnconfirmedSince), EndedAt: timeOf(r.EndedAt),
		PlanConfirmedAt: timePtr(r.PlanConfirmedAt), FirstSeen: timePtr(r.FirstSeen), LastSeen: timePtr(r.LastSeen),
		BilledThrough: strPtr(r.BilledThrough), Consumers: []CostConsumer{}, USDPerMWeighted: r.USDPerMWeighted, Plans: []PlanAt{},
	}
	if r.SuggestedDay > 0 {
		d := r.SuggestedDay
		s.SuggestedRenewalDay = &d
	}
	if a := accountOf(&t, r.Provider, r.Label); a != nil {
		s.Account, s.Name, s.Alias, s.Current = a.Label, a.Name, a.Alias, a.Current
	}
	for _, sp := range r.Consumers {
		s.Consumers = append(s.Consumers, CostConsumer{Device: name(sp.Name), DeviceID: sp.Name, USD: sp.USD, Share: sp.Share * 100})
	}
	for _, p := range r.Plans {
		s.Plans = append(s.Plans, PlanAt{At: p.At.UTC(), Plan: p.Plan})
	}
	return s
}

// chargeUSD is what a subscription's next charge costs: its price a month,
// or a year's when it is billed yearly; nil when unknown.
func chargeUSD(s CostSubscription) *float64 {
	if s.PriceUSD == nil {
		return nil
	}
	v := *s.PriceUSD
	if s.Cadence == cost.Yearly {
		v *= 12
	}
	return &v
}

// sortCost puts subscriptions by provider, in the order of the team's
// providers, then each provider's in the order of SUBSCRIPTIONS, those no
// device has any more after, by label, and those that ended last.
func sortCost(t Team, ss []CostSubscription) {
	rank := func(s CostSubscription) (int, int) {
		for i, tp := range t.Providers {
			if tp.Provider != s.Provider {
				continue
			}
			for j, a := range tp.Accounts {
				if snapshot.LabelKey(a.Label) == snapshot.LabelKey(s.Account) {
					return i, j
				}
			}
			return i, len(tp.Accounts)
		}
		return len(t.Providers) + slices.Index(snapshot.Providers, s.Provider), 0
	}
	slices.SortStableFunc(ss, func(a, b CostSubscription) int {
		pa, ia := rank(a)
		pb, ib := rank(b)
		return cmp.Or(cmp.Compare(pa, pb), cmp.Compare(btoi(a.State == CostEnded), btoi(b.State == CostEnded)),
			cmp.Compare(ia, ib), cmp.Compare(snapshot.LabelKey(a.Account), snapshot.LabelKey(b.Account)))
	})
}

// costAttention is what the cost adds to ATTENTION: a subscription nobody
// can say is still paid, and, for NewsFor, a new subscription, a change of
// plan, and a change of a list price.
func costAttention(c *Cost, now time.Time) []Attention {
	if c == nil {
		return nil
	}
	var out []Attention
	for _, s := range c.Subscriptions {
		if s.State == CostUnconfirmed {
			out = append(out, Attention{Kind: AttentionCheck, Provider: s.Provider, Account: s.Account, Name: s.Name, At: confirmedAt(s)})
		}
	}
	for _, e := range c.Events {
		switch e.Kind {
		case EventNew, EventPlanChanged, EventPriceChanged:
		default:
			continue
		}
		if now.Sub(e.At) > NewsFor {
			continue
		}
		s := c.Subscription(e.Provider, e.Account)
		name := ShortName(e.Account)
		if s != nil {
			name = s.Name
		}
		out = append(out, Attention{Kind: AttentionCost, Provider: e.Provider, Account: e.Account, Name: name, At: timePtr(e.At), Event: e.ID})
	}
	return out
}

// confirmedAt is when a subscription's paid plan was last known live: the
// team's newest reading of its quota, else when it was first found.
func confirmedAt(s CostSubscription) *time.Time {
	if s.PlanConfirmedAt != nil {
		return s.PlanConfirmedAt
	}
	return s.FirstSeen
}

// Subscription is the cost of the subscription of provider with label, or
// nil.
func (c *Cost) Subscription(provider, label string) *CostSubscription {
	if c == nil {
		return nil
	}
	for i := range c.Subscriptions {
		s := &c.Subscriptions[i]
		if s.Provider == provider && snapshot.LabelKey(s.Account) == snapshot.LabelKey(label) {
			return s
		}
	}
	return nil
}

// event is the cost event with the ID, or nil.
func (c *Cost) event(id string) *CostEvent {
	if c == nil {
		return nil
	}
	for i := range c.Events {
		if c.Events[i].ID == id {
			return &c.Events[i]
		}
	}
	return nil
}

// describeCost gives each question and event of c its words, with dates in
// loc, where it has none.
func describeCost(c *Cost, loc *time.Location) {
	if c == nil {
		return
	}
	for i := range c.Questions {
		if q := &c.Questions[i]; q.Text == "" {
			q.Text = c.questionText(*q, loc)
		}
	}
	for i := range c.Events {
		if e := &c.Events[i]; e.Text == "" {
			e.Text = c.eventText(*e, loc)
		}
	}
}

// subjectOf names a subscription in words: its provider and the label it
// goes by.
func (c *Cost) subjectOf(provider, label string) string {
	shown := shortID(label)
	if s := c.Subscription(provider, label); s != nil && s.Alias != nil && s.Name == *s.Alias {
		shown = *s.Alias
	}
	return provider + " " + snapshot.Printable(shown)
}

// planWords is a subscription's plan, as the list calls it, and its price
// a month when known: "Max 20x at $200/mo".
func planWords(s *CostSubscription) string {
	if s == nil {
		return ""
	}
	plan := ""
	switch {
	case s.PlanName != nil:
		plan = *s.PlanName
	case s.Plan != nil:
		plan = *s.Plan
	}
	if s.PriceUSD != nil {
		if plan != "" {
			plan += " at "
		}
		plan += usd(*s.PriceUSD) + "/mo"
	}
	return plan
}

// questionText is a question in words.
func (c *Cost) questionText(q Question, loc *time.Location) string {
	s := c.Subscription(q.Provider, q.Account)
	who := c.subjectOf(q.Provider, q.Account)
	if p := planWords(s); p != "" {
		who += " (" + p + ")"
	}
	switch q.Kind {
	case AskRenewal:
		out := "When does " + who + " charge next?"
		if q.Suggestion != nil {
			out += " Its tool suggests " + dayWords(*q.Suggestion, loc) + "."
		}
		return out
	case AskPrice:
		out := "What does " + who + " cost a month?"
		if len(q.Choices) > 0 {
			return out + " Its tool did not say its tier: " + orWords(q.Choices) + "."
		}
		return out + " The price list has no price for its plan."
	case AskStillPaid:
		out := "Is " + who + " still paid?"
		if s != nil {
			if at := confirmedAt(*s); at != nil {
				out += " Last confirmed " + at.In(loc).Format("2 Jan") + "."
			}
		}
		return out
	}
	return who
}

// orWords joins choices as words: "a, b or c".
func orWords(choices []string) string {
	if len(choices) < 2 {
		return strings.Join(choices, "")
	}
	return strings.Join(choices[:len(choices)-1], ", ") + " or " + choices[len(choices)-1]
}

// dayWords is a YYYY-MM-DD day as a date in words: "Thu 23 Oct".
func dayWords(day string, loc *time.Location) string {
	t, err := time.ParseInLocation(time.DateOnly, day, loc)
	if err != nil {
		return day
	}
	return t.Format("Mon 2 Jan")
}

// eventText is an event in words.
func (c *Cost) eventText(e CostEvent, loc *time.Location) string {
	s := c.Subscription(e.Provider, e.Account)
	who := c.subjectOf(e.Provider, e.Account)
	plan := func(p *string) string {
		if p == nil {
			return "?"
		}
		return cost.Builtin().Name(e.Provider, *p)
	}
	switch e.Kind {
	case EventNew:
		out := "new subscription " + who
		if p := planWords(s); p != "" {
			out += ": " + p
		}
		return out
	case EventPlanChanged:
		out := who + " changed plan: " + plan(e.From) + " to " + plan(e.To)
		if e.DeltaUSD != nil {
			out += ", " + signedUSD(*e.DeltaUSD) + "/mo"
		}
		return out
	case EventPriceChanged:
		out := "the list price of " + who + "'s " + plan(e.To) + " changed"
		if e.FromUSD != nil && e.ToUSD != nil {
			was := *e.FromUSD
			out += " from " + usd(was) + " to " + usd(*e.ToUSD) + "/mo; the new price applies from now on, and `ai-usage cost set " +
				e.Provider + ":" + snapshot.Printable(e.Account) + " --price " + strconv.FormatFloat(was, 'f', -1, 64) + "` keeps the old one"
		}
		return out
	case EventEnded:
		out := who + " ended"
		if s != nil && s.PriceUSD != nil {
			out += "; its " + usd(*s.PriceUSD) + "/mo no longer counts"
		}
		return out
	case EventChargesSoon:
		out := who + " charges"
		if s != nil {
			if v := chargeUSD(*s); v != nil {
				out += " " + usd(*v)
			}
		}
		if s != nil && s.RenewsAt != nil {
			out += " on " + s.RenewsAt.In(loc).Format("Mon 2 Jan")
		}
		if s != nil && s.UsedPct != nil {
			out += "; used " + wholePercent(*s.UsedPct) + "% over the last 4 weeks"
		}
		if s != nil && s.IdleSince != nil {
			out += ", unused " + strconv.Itoa(int(e.At.Sub(*s.IdleSince).Hours()/24)) + " days"
		}
		return out
	case EventUnconfirmed:
		out := "nobody can say " + who + " is still paid"
		if s != nil {
			if at := confirmedAt(*s); at != nil {
				out += "; last confirmed " + at.In(loc).Format("2 Jan")
			}
		}
		return out
	}
	return who
}

// usd prints dollars in whole dollars: $200, $1,200, <$1 under one, $0
// for none.
func usd(v float64) string {
	switch {
	case v <= 0:
		return "$0"
	case v < 1:
		return "<$1"
	}
	n := strconv.FormatFloat(v, 'f', 0, 64)
	for i := len(n) - 3; i > 0; i -= 3 {
		n = n[:i] + "," + n[i:]
	}
	return "$" + n
}

// signedUSD prints a change of dollars with its sign: +$100, -$20.
func signedUSD(v float64) string {
	if v < 0 {
		return "-" + usd(-v)
	}
	return "+" + usd(v)
}

// cents prints a small price to the cent, such as $0.41.
func cents(v float64) string {
	if v >= 100 {
		return usd(v)
	}
	return "$" + strconv.FormatFloat(v, 'f', 2, 64)
}

// planOf is how a subscription's plan reads on the page: as the price list
// calls it, else as its tool reports it.
func planOf(s *CostSubscription) string {
	switch {
	case s.PlanName != nil:
		return *s.PlanName
	case s.Plan != nil:
		return *s.Plan
	}
	return ""
}
