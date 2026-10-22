package cost

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// How a subscription is billed.
const (
	Monthly = "monthly"
	Yearly  = "yearly"
)

// The questions a person may be asked about a subscription.
const (
	AskRenewal   = "renewal"
	AskPrice     = "price"
	AskStillPaid = "still_paid"
)

// StoreVersion is the version of cost.json this build writes.
const StoreVersion = 1

// storeFile is the cost store in the state folder.
const storeFile = "cost.json"

// How many plans and list price changes a subscription keeps.
const (
	maxPlans        = 12
	maxPriceChanges = 12
)

// Kept is how long an ended subscription stays, shown as ended, before it is
// dropped. A paid plan read after that is a new subscription.
const Kept = 30 * 24 * time.Hour

// shownKept is how long an event or question shown is remembered: longer
// than an event is reported.
const shownKept = 2 * EventsKept

// droppedKept is how long a dropped subscription is remembered: longer than
// any device of the team keeps the subscription, and its answers, after it
// ended.
const droppedKept = 90 * 24 * time.Hour

// Store is cost.json: what this device knows of the cost of each
// subscription it or its team has seen. A person's command and a run both
// change it, each under the config lock for a moment, never under the run
// lock.
type Store struct {
	V int `json:"v"`
	// Since is when this device began keeping costs. The subscriptions its
	// first look found are no news.
	Since time.Time `json:"since,omitzero"`
	// NeverAsk turns this device's questions off.
	NeverAsk bool `json:"never_ask,omitempty"`
	// Subs are by Key.
	Subs map[string]*Sub `json:"subscriptions,omitempty"`
	// Dropped is, by Key, when each subscription dropped after Kept had
	// ended, for as long as the team may still say it was cancelled then.
	Dropped map[string]time.Time `json:"dropped,omitempty"`
	// Shown is, by event or question ID, when this device first showed it
	// to a person, so that a view or a notification shows each once. An
	// ID is kept for as long as it may be reported.
	Shown map[string]time.Time `json:"shown,omitempty"`
	// Newer says a newer ai-usage saved the file: Edit refuses to write it.
	Newer bool `json:"-"`
}

// Key keys a subscription: one person on a provider, however the devices
// spell the label.
func Key(provider, label string) string { return state.Key(provider, snapshot.LabelKey(label)) }

// Sub is one subscription as this device knows it. It stays after every
// device logged out of it and its account was pruned, so that a paid
// subscription does not leave the cost unnoticed, until a person cancels it
// or a reading says it is free, and Kept passed.
type Sub struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	// Answer is what a person typed on this device, and Heard what each
	// other device of the team said, by device id, as its last snapshot
	// that had it did.
	Answer Answer            `json:"answer,omitzero"`
	Heard  map[string]Answer `json:"heard,omitempty"`
	// Plans are the plans read of it by this device and the team, oldest
	// first: each with when it was first read.
	Plans []PlanAt `json:"plans,omitempty"`
	// FirstSeen is when this device first found the subscription, LastSeen
	// the newest snapshot of a device that had it logged in, and
	// PlanConfirmedAt the newest reading of its quota, which proves the
	// paid plan was live then.
	FirstSeen       time.Time `json:"first_seen,omitzero"`
	LastSeen        time.Time `json:"last_seen,omitzero"`
	PlanConfirmedAt time.Time `json:"plan_confirmed_at,omitzero"`
	// ListPlan and ListUSD are the plan and its monthly list price as the
	// list in use found them the last time: a later list's other price
	// for the same plan is a price change.
	ListPlan     string        `json:"list_plan,omitempty"`
	ListUSD      *float64      `json:"list_usd,omitempty"`
	PriceChanges []PriceChange `json:"price_changes,omitempty"`
	// Skipped is, by question, the plan it was skipped for: a skipped
	// question is not asked again until the plan changes.
	Skipped map[string]string `json:"skipped,omitempty"`
	// Asked is, by question, when it was last asked.
	Asked map[string]time.Time `json:"asked,omitempty"`
	// Reopened is when the subscription this one follows ended, when it is
	// one read again after that one was dropped: a cancel from then or
	// before is that one's.
	Reopened time.Time `json:"reopened,omitzero"`
}

// PlanAt is a plan, and when it was first read.
type PlanAt struct {
	At   time.Time `json:"at"`
	Plan string    `json:"plan"`
}

// PriceChange is a change of a plan's list price between two lists.
type PriceChange struct {
	At   time.Time `json:"at"`
	Plan string    `json:"plan"`
	From float64   `json:"from"`
	To   float64   `json:"to"`
}

// Answer is what people said of a subscription. Each answer has the time it
// was given, and a cleared one keeps its time with no value, so that the
// clearing outranks an older answer of another device.
type Answer struct {
	// Price is what each charge costs, in USD: a year's when Cadence is
	// Yearly. PriceFor is the plan it was typed for: on another plan it is
	// stale and not used.
	Price    *float64  `json:"price,omitempty"`
	PriceFor string    `json:"price_for,omitempty"`
	PriceAt  time.Time `json:"price_at,omitzero"`
	// Cadence is Monthly or Yearly.
	Cadence   string    `json:"cadence,omitempty"`
	CadenceAt time.Time `json:"cadence_at,omitzero"`
	// Renews is a day the subscription charges, YYYY-MM-DD.
	Renews   string    `json:"renews,omitempty"`
	RenewsAt time.Time `json:"renews_at,omitzero"`
	// CancelledAt is when a person said it was cancelled, and ConfirmedAt
	// when one said it was still paid. The newer one stands.
	CancelledAt time.Time `json:"cancelled_at,omitzero"`
	ConfirmedAt time.Time `json:"confirmed_at,omitzero"`
}

// IsZero says nobody said anything.
func (a Answer) IsZero() bool {
	return a.PriceAt.IsZero() && a.CadenceAt.IsZero() && a.RenewsAt.IsZero() && a.CancelledAt.IsZero() && a.ConfirmedAt.IsZero()
}

// Cancelled says a person said the subscription was cancelled, and nobody
// said it was still paid since.
func (a Answer) Cancelled() bool {
	return !a.CancelledAt.IsZero() && a.CancelledAt.After(a.ConfirmedAt)
}

// At is when the newest of the answers was given.
func (a Answer) At() time.Time {
	t := a.PriceAt
	for _, u := range []time.Time{a.CadenceAt, a.RenewsAt, a.CancelledAt, a.ConfirmedAt} {
		if u.After(t) {
			t = u
		}
	}
	return t
}

// SetPrice sets the price of each charge, typed for plan, at now; nil
// clears it.
func (a *Answer) SetPrice(price *float64, plan string, now time.Time) error {
	if price != nil && (*price < 0 || *price > MaxUSD || *price != *price) {
		return fmt.Errorf("a price is from 0 to %d", MaxUSD)
	}
	a.Price, a.PriceFor, a.PriceAt = price, "", second(now)
	if price != nil {
		a.PriceFor = snapshot.PlainLabel(plan)
	}
	return nil
}

// SetCadence sets how the subscription is billed, Monthly or Yearly, at
// now; "" clears it.
func (a *Answer) SetCadence(cadence string, now time.Time) error {
	if cadence != "" && cadence != Monthly && cadence != Yearly {
		return errors.New("a subscription is billed monthly or yearly")
	}
	a.Cadence, a.CadenceAt = cadence, second(now)
	return nil
}

// SetRenews sets a day the subscription charges, YYYY-MM-DD, at now; ""
// clears it.
func (a *Answer) SetRenews(day string, now time.Time) error {
	if day != "" {
		if _, err := parseDay(day); err != nil {
			return errors.New("a charge day is YYYY-MM-DD")
		}
	}
	a.Renews, a.RenewsAt = day, second(now)
	return nil
}

// Confirm says the subscription is still paid, at now.
func (a *Answer) Confirm(now time.Time) { a.ConfirmedAt = second(now) }

// Cancel says the subscription was cancelled, at now.
func (a *Answer) Cancel(now time.Time) { a.CancelledAt = second(now) }

// Reset clears the typed price, cadence, and charge day, at now. Whether it
// was cancelled or confirmed stays: confirming undoes a cancel.
func (a *Answer) Reset(now time.Time) {
	_ = a.SetPrice(nil, "", now)
	_ = a.SetCadence("", now)
	_ = a.SetRenews("", now)
}

// second is t in UTC to the second, as a snapshot carries it, so that an
// answer read back from the team is the same as this device's.
func second(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// LoadStore reads cost.json. A missing file is an empty store.
func LoadStore(dir state.Dir) (*Store, error) {
	s := &Store{V: StoreVersion}
	b, err := os.ReadFile(dir.Path(storeFile))
	if errors.Is(err, os.ErrNotExist) {
		return s.fill(), nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, damaged{fmt.Errorf("%s: %w", storeFile, err), b}
	}
	s.Newer = s.V > StoreVersion
	return s.fill(), nil
}

func (s *Store) fill() *Store {
	if s.Subs == nil {
		s.Subs = map[string]*Sub{}
	}
	for k, sub := range s.Subs {
		// A file edited by hand keys each subscription by its Key again.
		if sub == nil || k != Key(sub.Provider, sub.Label) {
			delete(s.Subs, k)
		}
		if sub != nil && s.Subs[Key(sub.Provider, sub.Label)] == nil {
			s.Subs[Key(sub.Provider, sub.Label)] = sub
		}
	}
	return s
}

// damaged is LoadStore's error for a cost.json that does not parse, with
// its bytes.
type damaged struct {
	error
	b []byte
}

// ErrNewer is Edit refusing to write over a cost.json a newer ai-usage saved.
var ErrNewer = errors.New("cost.json was saved by a newer ai-usage; update ai-usage")

// EditStore changes cost.json under the config lock, held only while the
// file is read and written, and returns the store as it saved it. A file
// that does not parse is kept as cost.json.bad, and the edit starts from an
// empty store, so that it does not stop every later run and command.
func EditStore(dir state.Dir, edit func(*Store) error) (*Store, error) {
	var s *Store
	err := dir.ConfigLocked(storeFile, func() error {
		var err error
		s, err = LoadStore(dir)
		var bad damaged
		if errors.As(err, &bad) {
			if err := fsutil.WriteFile(dir.Path(storeFile+".bad"), bad.b, 0o600); err != nil {
				return err
			}
			s, err = (&Store{V: StoreVersion}).fill(), nil
		}
		if err != nil {
			return err
		}
		if s.Newer {
			return ErrNewer
		}
		if err := edit(s); err != nil {
			return err
		}
		s.V = StoreVersion
		b, err := json.MarshalIndent(s, "", " ")
		if err != nil {
			return err
		}
		return fsutil.WriteFile(dir.Path(storeFile), append(b, '\n'), 0o600)
	})
	return s, err
}

// Sub is the subscription of provider and label, made when it is not there.
func (s *Store) Sub(provider, label string, now time.Time) *Sub {
	k := Key(provider, label)
	sub := s.Subs[k]
	if sub == nil {
		sub = &Sub{Provider: provider, Label: label, FirstSeen: now.UTC()}
		s.Subs[k] = sub
	}
	return sub
}

// Merged is what the team said of the subscription, this device's answers
// as device self's among them: each answer the newest any device gave.
func (sub *Sub) Merged(self string) Answer {
	voices := maps.Clone(sub.Heard)
	if voices == nil {
		voices = map[string]Answer{}
	}
	voices[self] = sub.Answer
	return Merge(voices)
}

// Plan is the subscription's newest plan, or "" when none was read.
func (sub *Sub) Plan() string {
	if len(sub.Plans) == 0 {
		return ""
	}
	return sub.Plans[len(sub.Plans)-1].Plan
}

// PlanAt is the plan the subscription had at t: the newest one read by
// then, else the first one read.
func (sub *Sub) PlanAt(t time.Time) string {
	plan := ""
	for i, p := range sub.Plans {
		if i == 0 || !p.At.After(t) {
			plan = p.Plan
		}
	}
	return plan
}

// Paid says the subscription had a plan that is not free.
func (sub *Sub) Paid() bool {
	return slices.ContainsFunc(sub.Plans, func(p PlanAt) bool { return !Free(p.Plan) })
}

// Ask records that question kind was asked at now.
func (sub *Sub) Ask(kind string, now time.Time) {
	if sub.Asked == nil {
		sub.Asked = map[string]time.Time{}
	}
	sub.Asked[kind] = now.UTC()
}

// Show records that the event or question id was shown to a person at now,
// and says whether it was the first time.
func (s *Store) Show(id string, now time.Time) bool {
	if _, ok := s.Shown[id]; ok {
		return false
	}
	if s.Shown == nil {
		s.Shown = map[string]time.Time{}
	}
	s.Shown[id] = now.UTC()
	return true
}

// Skip records that question kind was skipped for the subscription's plan.
func (sub *Sub) Skip(kind string) {
	if sub.Skipped == nil {
		sub.Skipped = map[string]string{}
	}
	sub.Skipped[kind] = sub.Plan()
}

// Sighting is a subscription as one device's snapshot, or this device's
// run, found it.
type Sighting struct {
	Provider string
	Label    string
	// Plan is the plan the device read, "" when it read none.
	Plan string
	// At is when the device read it: its snapshot's collected_at.
	At time.Time
	// Current says the device had it logged in then.
	Current bool
	// QuotaAt is when the device last read its quota, zero when never.
	QuotaAt time.Time
}

// CurrentPlan is the plan of the sightings ss of one subscription and when
// it was read: the one read with the newest quota reading, which proves it
// live, else the newest, said with its tier when another sighting has it.
// A device whose tool keeps an old plan while nobody uses it there does not
// take the plan back.
func CurrentPlan(ss []Sighting) (string, time.Time) {
	var best *Sighting
	for i := range ss {
		s := &ss[i]
		if Norm(s.Plan) == "" || Norm(s.Plan) == "unknown" {
			continue
		}
		if best == nil || cmp.Or(s.QuotaAt.Compare(best.QuotaAt), s.At.Compare(best.At)) > 0 {
			best = s
		}
	}
	if best == nil {
		return "", time.Time{}
	}
	plan := best.Plan
	for _, s := range ss {
		if Refines(plan, s.Plan) {
			plan = s.Plan
		}
	}
	return plan, best.At
}

// Observe records what a run found: the sightings of every subscription on
// this device and in the team's snapshots, what each other device said of
// them, by Key and device id, and the price list in use. self is this
// device's id. A subscription first read on a free plan, or with neither a
// plan nor a quota reading, as an API key, is no subscription to pay for.
// Ended subscriptions are dropped after Kept.
func (s *Store) Observe(self string, seen []Sighting, heard map[string]map[string]Answer, list Prices, now time.Time) {
	now = now.UTC()
	if s.Since.IsZero() {
		s.Since = now
	}
	groups := map[string][]Sighting{}
	for _, g := range seen {
		if costProvider(g.Provider) && g.Label != "" {
			k := Key(g.Provider, g.Label)
			groups[k] = append(groups[k], g)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(groups)) {
		ss := groups[k]
		plan, planAt := CurrentPlan(ss)
		sub := s.Subs[k]
		if sub == nil {
			quota := slices.ContainsFunc(ss, func(g Sighting) bool { return !g.QuotaAt.IsZero() })
			fresh := slices.ContainsFunc(ss, func(g Sighting) bool { return Fresh(g.Current, g.QuotaAt, s.Dropped[k], now) })
			if Free(plan) || plan == "" && !quota || !fresh {
				continue
			}
			sub = s.Sub(ss[0].Provider, ss[0].Label, now)
			sub.Reopened = s.Dropped[k]
			delete(s.Dropped, k)
		}
		for _, g := range ss {
			if g.Current && g.At.After(sub.LastSeen) {
				sub.LastSeen = g.At.UTC()
			}
			// A device that read the quota had the account logged in then.
			if g.QuotaAt.After(sub.LastSeen) && !g.QuotaAt.After(now) {
				sub.LastSeen = g.QuotaAt.UTC()
			}
			if g.QuotaAt.After(sub.PlanConfirmedAt) {
				sub.PlanConfirmedAt = g.QuotaAt.UTC()
			}
		}
		sub.notePlan(list, plan, planAt)
		sub.notePrice(list, now)
	}
	for k, devs := range heard {
		sub := s.Subs[k]
		if sub == nil {
			continue
		}
		for dev, a := range devs {
			if dev == self || a.IsZero() {
				continue
			}
			if sub.Heard == nil {
				sub.Heard = map[string]Answer{}
			}
			sub.Heard[dev] = a
		}
	}
	for k, sub := range s.Subs {
		if at, ok := sub.EndedAt(self); ok && now.Sub(at) > Kept {
			delete(s.Subs, k)
			if s.Dropped == nil {
				s.Dropped = map[string]time.Time{}
			}
			s.Dropped[k] = at
		}
	}
	for k, at := range s.Dropped {
		if now.Sub(at) > droppedKept {
			delete(s.Dropped, k)
		}
	}
	for id, at := range s.Shown {
		if now.Sub(at) > shownKept {
			delete(s.Shown, id)
		}
	}
}

// Fresh says a sighting of a subscription the store does not keep makes it
// one: a device has it logged in, or read its quota in the last ConfirmAge.
// A device keeps an account it logged out of for 90 days, and a teammate's
// snapshot carries it as long, so an old reading of a paid plan is no news
// of one. Of a subscription dropped after it ended at dropped, only a
// reading of its quota after the drop is: one from before may be of the
// paid time a cancelled subscription runs out.
func Fresh(current bool, quotaAt, dropped, now time.Time) bool {
	if !dropped.IsZero() {
		return quotaAt.After(dropped.Add(Kept)) && !quotaAt.After(now)
	}
	return current || !quotaAt.IsZero() && now.Sub(quotaAt) <= ConfirmAge
}

// notePlan records plan, read at at. The same plan spelled anew, renamed or
// with its tier, takes the new spelling; another plan read after the newest
// one is a change; one read before it is stale and left out.
func (sub *Sub) notePlan(list Prices, plan string, at time.Time) {
	if plan == "" {
		return
	}
	if n := len(sub.Plans); n > 0 {
		last := &sub.Plans[n-1]
		if list.Same(sub.Provider, last.Plan, plan) {
			last.Plan = Finer(last.Plan, plan)
			return
		}
		if !at.After(last.At) {
			return
		}
	}
	sub.Plans = append(sub.Plans, PlanAt{At: at.UTC(), Plan: plan})
	if len(sub.Plans) > maxPlans {
		sub.Plans = slices.Delete(sub.Plans, 0, len(sub.Plans)-maxPlans)
	}
}

// notePrice records the list price of the subscription's plan as list has
// it, and a change of it when the plan is the same as the last time.
func (sub *Sub) notePrice(list Prices, now time.Time) {
	plan := sub.Plan()
	var usd *float64
	if pl, ok := list.Find(sub.Provider, plan); ok {
		usd = &pl.USD
	}
	if usd != nil && sub.ListUSD != nil && *usd != *sub.ListUSD && list.Same(sub.Provider, sub.ListPlan, plan) {
		sub.PriceChanges = append(sub.PriceChanges, PriceChange{At: now, Plan: plan, From: *sub.ListUSD, To: *usd})
		if len(sub.PriceChanges) > maxPriceChanges {
			sub.PriceChanges = slices.Delete(sub.PriceChanges, 0, len(sub.PriceChanges)-maxPriceChanges)
		}
	}
	sub.ListPlan, sub.ListUSD = plan, usd
}

// EndedAt is when the subscription ended, as the team's answers merged with
// this device's, self, say: when a person cancelled it, or when a reading
// first found the free plan it is on now, whichever came first.
func (sub *Sub) EndedAt(self string) (time.Time, bool) { return sub.endedAt(sub.Merged(self)) }
