package collect

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/cost"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

// pricesTimeout bounds the ask for the relay's price list.
const pricesTimeout = 5 * time.Second

// withCosts gives doc, fitted without them, the costs people typed on this
// device, as many as fit.
func withCosts(doc *snapshot.Doc, store *cost.Store, key *team.Key) {
	doc.Costs = costs(store, func(s string) string { return key.Seal(clip(s, maxSealedPlain)) }, key.Seal)
	fitCosts(doc)
}

// costs are the answers people typed on this device, newest first, as many
// as a snapshot holds, with each label sealed by seal and each answer's
// data by sealData, which must not cut it.
func costs(store *cost.Store, seal, sealData func(string) string) []snapshot.Cost {
	if store == nil {
		return nil
	}
	type entry struct {
		c    snapshot.Cost
		data string
	}
	var out []entry
	for _, sub := range store.Subs {
		if sub.Label == "" || !snapshot.KnownProvider(sub.Provider) || sub.Provider == "hermes" {
			continue
		}
		data, at, ok := cost.EncodeAnswer(sub.Answer)
		if !ok {
			continue
		}
		out = append(out, entry{snapshot.Cost{Provider: sub.Provider, Label: sub.Label, At: at}, data})
	}
	slices.SortFunc(out, func(a, b entry) int {
		return cmp.Or(b.c.At.Compare(a.c.At), cmp.Compare(a.c.Provider, b.c.Provider), cmp.Compare(a.c.Label, b.c.Label))
	})
	if len(out) > snapshot.MaxCosts {
		out = out[:snapshot.MaxCosts]
	}
	cs := make([]snapshot.Cost, 0, len(out))
	for _, e := range out {
		e.c.Label, e.c.Data = seal(e.c.Label), sealData(e.data)
		cs = append(cs, e.c)
	}
	if len(cs) == 0 {
		return nil
	}
	return cs
}

// fitCosts fits the document, already fitted without its costs, under the
// relay's size limit with them: the projects go first, as no view reads
// another device's, then the oldest costs. No account goes for them.
func fitCosts(doc *snapshot.Doc) {
	for len(doc.Costs) > 0 {
		b, err := json.Marshal(doc)
		if err != nil || len(b) <= snapshot.MaxBytes {
			return
		}
		if i := longest(doc.Accounts, func(a snapshot.Account) int { return len(a.Projects) }, 0); i >= 0 {
			ps := doc.Accounts[i].Projects
			doc.Accounts[i].Projects = ps[:len(ps)-1]
			continue
		}
		if doc.Costs = doc.Costs[:len(doc.Costs)-1]; len(doc.Costs) == 0 {
			doc.Costs = nil
		}
	}
}

// loadCosts is the cost store, or nil when it cannot be read: a run then
// sends no costs, and noteCosts says why.
func loadCosts(dir state.Dir) *cost.Store {
	s, err := cost.LoadStore(dir)
	if err != nil {
		return nil
	}
	return s
}

// fetchPrices asks the relay for its price list, at most once every
// cost.FetchEvery, and keeps what it answers. A failure says nothing: the
// built-in list serves, and the ask is made again the next day. A run
// stopped while it asks leaves the ask to the next one.
func fetchPrices(ctx context.Context, o Options, now time.Time) {
	if o.Relay == nil || !cost.FetchDue(o.Dir, now) {
		return
	}
	askCtx, cancel := context.WithTimeout(ctx, pricesTimeout)
	body, err := o.Relay.Prices(askCtx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	_ = cost.SaveFetched(o.Dir, body, err, now)
}

// noteCosts records in the cost store what this run found of the team's
// subscriptions: this device's accounts, the accounts of each other
// device's snapshot in the team read, and what each other device's people
// said of them, with the price list in use.
func noteCosts(o Options, st *state.State, key *team.Key, cache TeamCache, self string, now time.Time) error {
	var seen []cost.Sighting
	for _, a := range st.Accounts {
		if a.Label == UnknownAccount {
			continue
		}
		g := cost.Sighting{Provider: a.Provider, Label: a.Label, Plan: a.Plan, At: a.LastSeenAt, Current: IsCurrent(st, a.Provider, a.Label)}
		if a.Quota != nil {
			g.QuotaAt = a.Quota.At
		}
		seen = append(seen, g)
	}
	var docs []snapshot.Doc
	if cache.Team == key.Fingerprint() {
		for _, d := range cache.Docs {
			if d.Device == self {
				continue
			}
			docs = append(docs, d)
			for _, a := range d.Accounts {
				label, err := key.Open(a.Label)
				if err != nil || label == "" || label == UnknownAccount {
					continue
				}
				g := cost.Sighting{Provider: a.Provider, Label: label, Plan: a.Plan, At: d.CollectedAt, Current: a.Current}
				if a.QuotaAt != nil && a.QuotaFrom == "" {
					g.QuotaAt = *a.QuotaAt
				}
				seen = append(seen, g)
			}
		}
	}
	heard := cost.Heard(docs, key.Open)
	list, _ := cost.Current(o.Dir)
	_, err := cost.EditStore(o.Dir, func(s *cost.Store) error {
		s.Observe(self, seen, heard, list, now)
		return nil
	})
	if errors.Is(err, cost.ErrNewer) {
		return err
	}
	if err != nil {
		return errors.New("cost.json: " + err.Error())
	}
	return nil
}
