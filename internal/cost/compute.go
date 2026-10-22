package cost

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"time"
)

// Used is counted over the last UsedDays UTC days, today's among them, in
// Slots weeks of SlotDays.
const (
	Slots    = 4
	SlotDays = 7
	UsedDays = Slots * SlotDays
)

// How long facts and events last.
const (
	// ConfirmAge is how long a reading of the quota, or a person's word,
	// vouches that a subscription is still paid.
	ConfirmAge = 35 * 24 * time.Hour
	// GoneAfter is how long no device may have a subscription logged in
	// before nobody can say it is still paid.
	GoneAfter = 14 * 24 * time.Hour
	// EventsKept is how far back the events go.
	EventsKept = 30 * 24 * time.Hour
	// ChargesSoon is how long before a confirmed charge it is news.
	ChargesSoon = 3 * 24 * time.Hour
)

// A subscription's state.
const (
	StateActive      = "active"
	StateIdle        = "idle"
	StateUnconfirmed = "unconfirmed"
	StateEnded       = "ended"
)

// Where a subscription's price comes from: the list, a person, the list
// beside a typed price for another plan, or nowhere.
const (
	PriceList    = "list"
	PriceTyped   = "typed"
	PriceStale   = "stale"
	PriceUnknown = "unknown"
)

// The events of a subscription.
const (
	EventNew          = "new"
	EventPlanChanged  = "plan_changed"
	EventPriceChanged = "price_changed"
	EventEnded        = "ended"
	EventChargesSoon  = "charges_soon"
	EventUnconfirmed  = "unconfirmed"
)

// dayNumber is the number of t's UTC day since the Unix epoch.
func dayNumber(t time.Time) int64 {
	s := t.Unix()
	if s < 0 {
		return (s - 86399) / 86400
	}
	return s / 86400
}

// SlotOf is the week of the last UsedDays UTC days, up to now's, that t
// falls in: 0 the oldest, Slots-1 the one that ends today. It is -1 when t
// is outside them.
func SlotOf(t, now time.Time) int {
	d := dayNumber(now) - dayNumber(t)
	if d < 0 || d >= UsedDays {
		return -1
	}
	return Slots - 1 - int(d/SlotDays)
}

// SlotEnd is when week i of SlotOf ends: the end of its last UTC day, or now
// for the week that ends today.
func SlotEnd(i int, now time.Time) time.Time {
	last := dayNumber(now) - int64((Slots-1-i)*SlotDays)
	end := time.Unix((last+1)*86400, 0).UTC()
	if end.After(now) {
		return now
	}
	return end
}

// SlotTokens is a device's series of tokens by UTC day, index 0 the day of
// collected, by week as of now.
func SlotTokens(days []int64, collected, now time.Time) [Slots]int64 {
	var out [Slots]int64
	first := dayNumber(collected)
	for i, n := range days {
		if s := SlotOf(time.Unix((first-int64(i))*86400, 0), now); s >= 0 {
			out[s] += n
		}
	}
	return out
}

// Cycle is one ended period of a subscription's main quota window: when it
// reset, and its fullest reading.
type Cycle struct {
	ResetsAt time.Time
	Percent  float64
}

// Slot is one week of the subscription's Used.
type Slot struct {
	// Known says how full it got is known: from the fullest of the
	// periods that reset in the week, or 0 when none did and the team
	// spent nothing on it while it was watched. A week with tokens and no
	// period is a missed reading, and one from before anybody watched the
	// subscription was not seen.
	Known   bool    `json:"known"`
	Percent float64 `json:"percent"`
	// Tokens are the team's weighted tokens on it in the week.
	Tokens int64 `json:"tokens"`
	// Plan is the plan it had at the week's end, and USD its price a month
	// then, nil when unknown.
	Plan string   `json:"plan"`
	USD  *float64 `json:"usd"`
}

// UsedSlots is how full the subscription got in each week as of now, from
// the periods of its main window that ended, each capped at 100%, and the
// team's weighted tokens on it by week. watched is since when the team
// would have seen a period end: a week that began before it, with no
// period and no tokens, is not known to be unused.
func UsedSlots(cycles []Cycle, tokens [Slots]int64, watched, now time.Time) [Slots]Slot {
	var out [Slots]Slot
	for i := range out {
		out[i].Tokens = tokens[i]
		out[i].Known = tokens[i] <= 0 && !SlotEnd(i-1, now).Before(watched)
	}
	for _, c := range cycles {
		s := SlotOf(c.ResetsAt, now)
		if s < 0 || c.ResetsAt.After(now) {
			continue
		}
		out[s].Percent = max(out[s].Percent, min(max(c.Percent, 0), 100))
		out[s].Known = true
	}
	return out
}

// Priced is a plan's price a month and where it comes from. StaleFor is the
// plan a typed price that is not used was typed for.
type Priced struct {
	USD      *float64
	Source   string
	StaleFor string
}

// Price is the price a month of a provider's plan, as people answered a
// and the list says: the typed price when it was typed for this plan, else
// the list price at the answered cadence, else unknown. A free plan costs
// nothing.
func Price(list Prices, provider, plan string, a Answer) Priced {
	yearly := a.Cadence == Yearly
	if a.Price != nil && (a.PriceFor == "" || list.Same(provider, a.PriceFor, plan)) {
		usd := *a.Price
		if yearly {
			usd /= 12
		}
		return Priced{USD: &usd, Source: PriceTyped}
	}
	p := Priced{Source: PriceUnknown}
	if a.Price != nil {
		p.Source, p.StaleFor = PriceStale, a.PriceFor
	}
	switch pl, ok := list.Find(provider, plan); {
	case Free(plan):
		p.USD = new(float64)
	case ok:
		usd := pl.Monthly(a.Cadence)
		p.USD = &usd
	default:
		return p
	}
	if p.Source == PriceUnknown {
		p.Source = PriceList
	}
	return p
}

// Cadences are how a provider's plan may be billed, when the list says it
// can be billed yearly too, else none: a question asks the cadence only of
// such a plan.
func Cadences(list Prices, provider, plan string) []string {
	if pl, ok := list.Find(provider, plan); ok && pl.AnnualUSD > 0 {
		return []string{Monthly, Yearly}
	}
	return nil
}

// Consumer is one consumer of a subscription, user@device, and its weighted
// tokens on it in each week of SlotOf.
type Consumer struct {
	Name  string
	Slots [Slots]int64
}

// Spend is a consumer's part of a subscription's Used: Share of its weighted
// tokens over the UsedDays, out of one, and USD, that part of the used
// dollars, nil when the price is unknown.
type Spend struct {
	Name   string
	USD    *float64
	Share  float64
	Tokens int64
}

// Input is what the numbers of one subscription are worked out from, as of
// Now: what the store and the team know of it, and what the report found.
type Input struct {
	Now  time.Time
	Loc  *time.Location
	List Prices
	// Sub is the store's subscription, nil when the store does not have it
	// yet: then Plan, read now, is its only plan.
	Sub      *Sub
	Provider string
	Label    string
	Plan     string
	// Answer is what the team said of it, merged.
	Answer Answer
	// Cycles are the ended periods of its main window: the weekly one, else
	// the longest it has of a day or more.
	Cycles    []Cycle
	Consumers []Consumer
	// IdleSince is when it was last used, when no device of the team used
	// it for long enough to call it idle.
	IdleSince *time.Time
	// Billing is how its tool says it is billed, and SubscribedAt when the
	// tool says it was subscribed to, as this device read them.
	Billing      string
	SubscribedAt time.Time
	// QuotaAt is the team's newest reading of its quota, for a
	// subscription the store does not have yet.
	QuotaAt time.Time
}

// Result is the numbers of one subscription.
type Result struct {
	Provider string
	Label    string
	Plan     string
	PlanName string
	Price    Priced
	// Cadence is Monthly or Yearly.
	Cadence string
	// NextCharge is the next confirmed charge, midnight of its day in Loc;
	// nil when nobody confirmed a charge day. SuggestedDay is the day of
	// the month its tool says it was subscribed on, offered when asking,
	// 0 when none.
	NextCharge   *time.Time
	SuggestedDay int
	// Slots are the weeks of Used. UsedPct is the mean of the known ones;
	// UsedUSD the mean of their price times how full they got, nil when
	// the price is unknown. With no week known, UsedUSD is an estimate:
	// the whole price when the team spent tokens on it, else nothing.
	// UnusedUSD is the price less UsedUSD.
	Slots         [Slots]Slot
	UsedPct       *float64
	UsedUSD       *float64
	UsedEstimated bool
	UnusedUSD     *float64
	State         string
	// EndedAt is when it ended, UnconfirmedSince since when nobody can say
	// it is still paid, and IdleSince when it was last used, each when its
	// state says so.
	EndedAt          *time.Time
	UnconfirmedSince *time.Time
	IdleSince        *time.Time
	PlanConfirmedAt  time.Time
	FirstSeen        time.Time
	LastSeen         time.Time
	// BilledThrough is the store that bills it, when not the provider:
	// "App Store" or "Google Play". Its price there may differ.
	BilledThrough string
	// Consumers split UsedUSD by weighted tokens, the most first; Tokens
	// is their sum, and USDPerMWeighted the price a month for a million
	// of them.
	Consumers       []Spend
	Tokens          int64
	USDPerMWeighted *float64
	Plans           []PlanAt
}

// Compute works out the numbers of one subscription.
func Compute(in Input) Result {
	now, loc := in.Now, cmp.Or(in.Loc, time.Local)
	sub := in.sub()
	plan := cmp.Or(sub.Plan(), in.Plan)
	// One that ended on a free plan is shown, and its end told, with the
	// plan it was paid for.
	if Free(plan) {
		plan = cmp.Or(sub.lastPaid(), plan)
	}
	r := Result{
		Provider: in.Provider, Label: in.Label, Plan: plan, PlanName: in.List.Name(in.Provider, plan),
		Price: Price(in.List, in.Provider, plan, in.Answer), Cadence: cmp.Or(in.Answer.Cadence, Monthly),
		PlanConfirmedAt: sub.PlanConfirmedAt, FirstSeen: sub.FirstSeen, LastSeen: sub.LastSeen,
		BilledThrough: BilledThrough(in.Billing), Plans: slices.Clone(sub.Plans),
	}
	var team [Slots]int64
	for _, c := range in.Consumers {
		for i, n := range c.Slots {
			team[i] += n
		}
	}
	watched := sub.FirstSeen
	for _, c := range in.Cycles {
		if c.ResetsAt.Before(watched) {
			watched = c.ResetsAt
		}
	}
	r.Slots = UsedSlots(in.Cycles, team, watched, now)
	var pct, usd float64
	var known, priced int
	for i := range r.Slots {
		s := &r.Slots[i]
		s.Plan = sub.PlanAt(SlotEnd(i, now))
		s.USD = Price(in.List, in.Provider, s.Plan, in.Answer).USD
		r.Tokens += s.Tokens
		if !s.Known {
			continue
		}
		known++
		pct += s.Percent
		if s.USD != nil {
			priced++
			usd += *s.USD * s.Percent / 100
		}
	}
	price := r.Price.USD
	switch {
	case known > 0:
		r.UsedPct = ptr(pct / float64(known))
		if priced > 0 && price != nil {
			r.UsedUSD = ptr(usd / float64(priced))
		}
	case price != nil && r.Tokens > 0:
		r.UsedUSD, r.UsedEstimated = ptr(*price), true
	case price != nil:
		r.UsedUSD, r.UsedEstimated = ptr(0.0), true
	}
	if price != nil && r.UsedUSD != nil {
		r.UnusedUSD = ptr(max(*price-*r.UsedUSD, 0))
	}
	for _, c := range in.Consumers {
		var n int64
		for _, t := range c.Slots {
			n += t
		}
		if n <= 0 {
			continue
		}
		sp := Spend{Name: c.Name, Tokens: n, Share: float64(n) / float64(r.Tokens)}
		if r.UsedUSD != nil {
			sp.USD = ptr(*r.UsedUSD * sp.Share)
		}
		r.Consumers = append(r.Consumers, sp)
	}
	slices.SortStableFunc(r.Consumers, func(a, b Spend) int { return cmp.Or(cmp.Compare(b.Tokens, a.Tokens), cmp.Compare(a.Name, b.Name)) })
	if price != nil && r.Tokens > 0 {
		r.USDPerMWeighted = ptr(*price / (float64(r.Tokens) / 1e6))
	}

	r.State = StateActive
	switch ended, ok := sub.endedAt(in.Answer); {
	case ok:
		r.State, r.EndedAt = StateEnded, &ended
	default:
		if since, ok := sub.unconfirmedSince(in.Answer, now); ok {
			r.State, r.UnconfirmedSince = StateUnconfirmed, &since
		} else if in.IdleSince != nil {
			r.State, r.IdleSince = StateIdle, in.IdleSince
		}
	}
	if r.State != StateEnded && in.Answer.Renews != "" {
		if next, ok := NextCharge(in.Answer.Renews, r.Cadence, now, loc); ok {
			r.NextCharge = &next
		}
	}
	if r.NextCharge == nil && !in.SubscribedAt.IsZero() {
		r.SuggestedDay = in.SubscribedAt.In(loc).Day()
	}
	return r
}

// sub is the store's subscription, or one made of what the report read
// now when the store does not have it yet.
func (in Input) sub() *Sub {
	if in.Sub != nil {
		return in.Sub
	}
	s := &Sub{Provider: in.Provider, Label: in.Label, FirstSeen: in.Now, PlanConfirmedAt: in.QuotaAt}
	if in.Plan != "" {
		s.Plans = []PlanAt{{At: in.Now, Plan: in.Plan}}
	}
	return s
}

func ptr[T any](v T) *T { return &v }

// lastPaid is the newest plan of the subscription that is not free, or ""
// when none is.
func (sub *Sub) lastPaid() string {
	for i := len(sub.Plans) - 1; i >= 0; i-- {
		if !Free(sub.Plans[i].Plan) {
			return sub.Plans[i].Plan
		}
	}
	return ""
}

// endedAt is when the subscription ended, as the merged answer a says: when
// a person cancelled it, or when a reading first found the free plan it is
// on now, whichever came first.
func (sub *Sub) endedAt(a Answer) (time.Time, bool) {
	var at time.Time
	if a.Cancelled() && a.CancelledAt.After(sub.Reopened) {
		at = a.CancelledAt
	}
	if n := len(sub.Plans); n > 0 && Free(sub.Plans[n-1].Plan) && (at.IsZero() || sub.Plans[n-1].At.Before(at)) {
		at = sub.Plans[n-1].At
	}
	return at, !at.IsZero()
}

// unconfirmedSince is since when nobody can say the subscription is still
// paid, as of now: its plan was last confirmed by a reading of its quota,
// or, with none, it was first found, ConfirmAge ago, or no device has had
// it logged in for GoneAfter; and no person said it was still paid in the
// last ConfirmAge.
func (sub *Sub) unconfirmedSince(a Answer, now time.Time) (time.Time, bool) {
	// Found with no reading of either, it is as old as when it was found.
	confirmed, seen := sub.PlanConfirmedAt, sub.LastSeen
	if confirmed.IsZero() {
		confirmed = sub.FirstSeen
	}
	if seen.IsZero() {
		seen = sub.FirstSeen
	}
	since := confirmed.Add(ConfirmAge)
	if gone := seen.Add(GoneAfter); gone.Before(since) {
		since = gone
	}
	if !a.ConfirmedAt.IsZero() {
		if said := a.ConfirmedAt.Add(ConfirmAge); said.After(since) {
			since = said
		}
	}
	return since, !now.Before(since)
}

// BilledThrough is the store that bills a subscription, as Claude Code's
// billing type names it, or "" when the provider does.
func BilledThrough(billing string) string {
	switch billing {
	case "apple_subscription":
		return "App Store"
	case "google_play_subscription":
		return "Google Play"
	}
	return ""
}

// NextCharge is the first charge on or after now's day in loc of a
// subscription that charges on day anchor, YYYY-MM-DD, at cadence: the same
// day each month, the last day of a shorter month, or the same date each
// year. A day still to come is the next charge itself.
func NextCharge(anchor, cadence string, now time.Time, loc *time.Location) (time.Time, bool) {
	a, err := parseDay(anchor)
	if err != nil {
		return time.Time{}, false
	}
	y, m, d := now.In(loc).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	step := 1
	if cadence == Yearly {
		step = 12
	}
	months := 0
	if since := (y-a.Year())*12 + int(m) - int(a.Month()); since > 0 {
		months = since / step * step
	}
	for ; ; months += step {
		if at := monthDay(a.Year(), a.Month()+time.Month(months), a.Day(), loc); !at.Before(today) {
			return at, true
		}
	}
}

// monthDay is day d of month m of year y in loc, or the month's last day
// when it is shorter. m may run past December.
func monthDay(y int, m time.Month, d int, loc *time.Location) time.Time {
	first := time.Date(y, m, 1, 0, 0, 0, 0, loc)
	last := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(d, last)-1)
}

// RenewsOn is the date of the next charge of a subscription that charges
// on day of the month day, as of now in loc, YYYY-MM-DD: this month's when
// it is still to come, else next month's, the last day of a shorter month.
func RenewsOn(day int, now time.Time, loc *time.Location) string {
	y, m, d := now.In(loc).Date()
	at := monthDay(y, m, day, loc)
	if at.Day() < d {
		at = monthDay(y, m+1, day, loc)
	}
	return at.Format(time.DateOnly)
}

// Anniversary is the next date, YYYY-MM-DD, as of now in loc, on which a
// subscription that began at began and is billed yearly charges: today's
// when it is today.
func Anniversary(began, now time.Time, loc *time.Location) string {
	next, _ := NextCharge(began.In(loc).Format(time.DateOnly), Yearly, now, loc)
	return next.Format(time.DateOnly)
}

// DayAnchor is a charge day, YYYY-MM-DD, that charges monthly on day of the
// month, as of now in loc: in this month, or the last one before it that
// has the day, so that a shorter month's last day does not stand for it
// in the months after.
func DayAnchor(day int, now time.Time, loc *time.Location) string {
	y, m, _ := now.In(loc).Date()
	for {
		if at := monthDay(y, m, day, loc); at.Day() == day {
			return at.Format(time.DateOnly)
		}
		m--
	}
}

// Event is something that happened to a subscription that a person may want
// to hear of once: ID stays the same for as long as it is reported.
type Event struct {
	ID       string
	Kind     string
	At       time.Time
	Provider string
	Label    string
	// From and To are the plans of a change, To the plan of a new
	// subscription; FromUSD and ToUSD their prices a month, of a price
	// change the list's.
	From, To       string
	FromUSD, ToUSD *float64
}

// DeltaUSD is how much a month the event changed the cost by, when known.
func (e Event) DeltaUSD() *float64 {
	switch {
	case e.ToUSD == nil:
		return nil
	case e.FromUSD == nil && e.Kind == EventNew:
		return ptr(*e.ToUSD)
	case e.FromUSD == nil:
		return nil
	}
	return ptr(*e.ToUSD - *e.FromUSD)
}

// Events are what happened to the subscription of in, whose numbers are r,
// over the last EventsKept, oldest first. since is when the store began:
// a subscription found then is no news.
func Events(in Input, r Result, since time.Time) []Event {
	sub := in.sub()
	var out []Event
	add := func(kind string, at time.Time, from, to string, fromUSD, toUSD *float64) {
		e := Event{Kind: kind, At: at, Provider: in.Provider, Label: in.Label, From: from, To: to, FromUSD: fromUSD, ToUSD: toUSD}
		e.ID = eventID(e)
		out = append(out, e)
	}
	priced := func(plan string) *float64 { return Price(in.List, in.Provider, plan, in.Answer).USD }
	if in.Sub != nil && sub.FirstSeen.After(since) && len(sub.Plans) > 0 {
		first := sub.Plans[0].Plan
		add(EventNew, sub.FirstSeen, "", first, nil, priced(first))
	}
	for i := 1; i < len(sub.Plans); i++ {
		from, to := sub.Plans[i-1].Plan, sub.Plans[i].Plan
		if Free(to) {
			continue
		}
		add(EventPlanChanged, sub.Plans[i].At, from, to, priced(from), priced(to))
	}
	for _, c := range sub.PriceChanges {
		add(EventPriceChanged, c.At, c.Plan, c.Plan, ptr(c.From), ptr(c.To))
	}
	if r.EndedAt != nil {
		add(EventEnded, *r.EndedAt, r.Plan, "", r.Price.USD, nil)
	}
	if r.UnconfirmedSince != nil {
		add(EventUnconfirmed, *r.UnconfirmedSince, "", r.Plan, nil, r.Price.USD)
	}
	if r.NextCharge != nil {
		if at := r.NextCharge.AddDate(0, 0, -int(ChargesSoon/(24*time.Hour))); !in.Now.Before(at) {
			add(EventChargesSoon, at, "", r.Plan, nil, r.Price.USD)
		}
	}
	out = slices.DeleteFunc(out, func(e Event) bool { return e.At.After(in.Now) || in.Now.Sub(e.At) > EventsKept })
	slices.SortStableFunc(out, func(a, b Event) int { return a.At.Compare(b.At) })
	return out
}

// eventID keys an event by its subscription, kind, and time, so that the
// same event has the same ID in every report: not by its plans, which a
// later reading of the tier spells anew.
func eventID(e Event) string {
	h := sha256.Sum256([]byte(Key(e.Provider, e.Label) + "\x00" + e.Kind + "\x00" + strconv.FormatInt(e.At.Unix(), 10)))
	return e.Kind + "-" + hex.EncodeToString(h[:6])
}

// Totals are the team's subscriptions that have not ended, added up.
type Totals struct {
	// USD is the sum of the prices known, and WithoutPrice how many have
	// none.
	USD          float64
	WithoutPrice int
	// UsedUSD and UnusedUSD add up the subscriptions with a known price
	// and Used; UsedPct is UsedUSD over their prices. UsedEstimated says
	// an estimate is among them.
	UsedUSD       float64
	UnusedUSD     float64
	UsedPct       *float64
	UsedEstimated bool
}

// Sum adds up rs.
func Sum(rs []Result) Totals {
	var t Totals
	var paid float64
	for _, r := range rs {
		if r.State == StateEnded {
			continue
		}
		if r.Price.USD == nil {
			t.WithoutPrice++
			continue
		}
		t.USD += *r.Price.USD
		if r.UsedUSD == nil {
			continue
		}
		paid += *r.Price.USD
		t.UsedUSD += *r.UsedUSD
		t.UnusedUSD += *r.UnusedUSD
		t.UsedEstimated = t.UsedEstimated || r.UsedEstimated
	}
	if paid > 0 {
		t.UsedPct = ptr(t.UsedUSD / paid * 100)
	}
	return t
}

// ByConsumer is what each consumer used of the subscriptions rs that have
// not ended, in dollars, the most first.
func ByConsumer(rs []Result) []Spend {
	at := map[string]int{}
	var out []Spend
	for _, r := range rs {
		if r.State == StateEnded {
			continue
		}
		for _, c := range r.Consumers {
			i, ok := at[c.Name]
			if !ok {
				i = len(out)
				at[c.Name] = i
				out = append(out, Spend{Name: c.Name})
			}
			out[i].Tokens += c.Tokens
			if c.USD != nil {
				out[i].USD = ptr(*c.USD + *cmp.Or(out[i].USD, new(float64)))
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Spend) int {
		au, bu := *cmp.Or(a.USD, new(float64)), *cmp.Or(b.USD, new(float64))
		return cmp.Or(cmp.Compare(bu, au), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// Pending are the questions to ask a person on this device about the
// subscription whose numbers are r: the day it charges, and its cadence,
// and its price when the list has none, of one logged in here; and whether
// it is still paid, when nobody can say, of any. A charge day or price
// skipped for its plan is not asked again until the plan changes, whether
// it is still paid is never asked again once skipped, and none is asked of
// an ended subscription or on a device whose questions are off.
func Pending(r Result, sub *Sub, here, neverAsk bool) []string {
	if neverAsk || r.State == StateEnded {
		return nil
	}
	skipped := func(kind string) bool {
		if sub == nil {
			return false
		}
		plan, ok := sub.Skipped[kind]
		return ok && (kind == AskStillPaid || plan == r.Plan || Refines(plan, r.Plan))
	}
	var out []string
	if here && r.NextCharge == nil && !skipped(AskRenewal) {
		out = append(out, AskRenewal)
	}
	if here && r.Price.USD == nil && !skipped(AskPrice) {
		out = append(out, AskPrice)
	}
	if r.State == StateUnconfirmed && !skipped(AskStillPaid) {
		out = append(out, AskStillPaid)
	}
	return out
}
