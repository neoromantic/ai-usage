package view

import (
	"cmp"
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// colKey is a matrix column's key: a subscription's provider and label, or
// a provider whose tokens have no quota.
type colKey struct {
	provider, label string
	noQuota         bool
}

// matrix is every device against every subscription, and the tokens with
// no quota, one column per provider.
func matrix(providers []TeamProvider, byProv map[string]map[string]*teamAccount, devs []*device) Matrix {
	cols, index := matrixColumns(providers, byProv, devs)
	mx := Matrix{Columns: cols, Rows: []Row{}}
	for _, dv := range devs {
		row := Row{Device: dv.dev.Label, DeviceID: dv.dev.Device, Cells: make([]Cell, len(mx.Columns))}
		for _, a := range dv.accts {
			x := billsTo(a, byProv)
			k := colKey{a.provider, "", true}
			if x != nil {
				k = colKey{x.provider, x.ta.Label, false}
			}
			i := index[k]
			c := &row.Cells[i]
			c.Usage = c.Usage.add(a.usage)
			if x != nil && x.main != "" {
				c.WindowTokens += a.sinceStart(x.main, x.start, dv.collectedAt)
			}
			row.Usage = row.Usage.add(a.usage)
		}
		for i, c := range row.Cells {
			mx.Columns[i].Usage = mx.Columns[i].Usage.add(c.Usage)
			mx.Columns[i].WindowTokens += c.WindowTokens
		}
		mx.Rows = append(mx.Rows, row)
	}
	var all Usage
	for _, row := range mx.Rows {
		all = all.add(row.Usage)
	}
	for r := range mx.Rows {
		row := &mx.Rows[r]
		for i := range row.Cells {
			row.Cells[i].Share = shareOf(row.Cells[i].Usage, mx.Columns[i].Usage)
		}
		row.Share = shareOf(row.Usage, all)
	}
	slices.SortStableFunc(mx.Rows, func(a, b Row) int {
		return cmp.Or(cmp.Compare(b.Usage.Week, a.Usage.Week), cmp.Compare(a.Device, b.Device), cmp.Compare(a.DeviceID, b.DeviceID))
	})
	return mx
}

// matrixColumns is the matrix's columns, the subscriptions in the order of
// providers, and each column's index by its key.
func matrixColumns(providers []TeamProvider, byProv map[string]map[string]*teamAccount, devs []*device) ([]Column, map[colKey]int) {
	cols := []Column{}
	index := map[colKey]int{}
	for _, tp := range providers {
		for _, a := range tp.Accounts {
			if !a.Subscription {
				continue
			}
			c := Column{Provider: tp.Provider, Label: a.Label, Name: a.Name, State: a.State}
			if mw := knownMain(a.Quota); mw != nil {
				p := mw.Percent
				c.Percent = &p
			}
			index[colKey{tp.Provider, a.Label, false}] = len(cols)
			cols = append(cols, c)
		}
	}
	// Tokens with no quota get a column per provider after the rest.
	noQuota := map[string]bool{}
	for _, dv := range devs {
		for _, a := range dv.accts {
			if billsTo(a, byProv) == nil {
				noQuota[a.provider] = true
			}
		}
	}
	for _, p := range snapshot.Providers {
		if noQuota[p] {
			index[colKey{p, "", true}] = len(cols)
			cols = append(cols, Column{Provider: p, Name: p, NoQuota: true, State: StateUnknown})
		}
	}
	return cols, index
}

// shareOf is part's share of whole in each period.
func shareOf(part, whole Usage) Share {
	of := func(p Period) *float64 {
		total := p.Of(whole)
		if total == 0 {
			return nil
		}
		s := float64(p.Of(part)) / float64(total) * 100
		return &s
	}
	return Share{Today: of(Today), Week: of(Week), Month: of(Month), Quarter: of(Quarter)}
}

// billsTo is the subscription account a device account's tokens count
// against: its own, or for Hermes the login it spends through, when that
// is an account of the team. It is nil for tokens with no quota.
func billsTo(a devAccount, byProv map[string]map[string]*teamAccount) *teamAccount {
	if subscription(a.provider) {
		return byProv[a.provider][a.label]
	}
	if a.link == nil || a.link.Label == "" {
		return nil
	}
	return byProv[a.link.Provider][a.link.Label]
}

// users counts the devices with tokens on each account of a provider since
// its main window began, what linked accounts spent through it included,
// or in the last 7 days without a main window. The busiest is the one with
// the most.
func users(provider string, m map[string]*teamAccount, devs []*device) {
	for _, x := range m {
		var best int64
		for _, dv := range devs {
			var n int64
			for _, a := range dv.accts {
				own := a.provider == provider && a.label == x.ta.Label
				through := !subscription(a.provider) && a.link != nil && a.link.Provider == provider && a.link.Label == x.ta.Label
				if !own && !through {
					continue
				}
				if x.main != "" {
					n += a.sinceStart(x.main, x.start, dv.collectedAt)
				} else {
					n += a.usage.Week
				}
			}
			if n <= 0 {
				continue
			}
			x.ta.Users++
			if n > best {
				best = n
				x.ta.Busiest = strPtr(dv.dev.Label)
			}
		}
	}
}

// sinceStart is a device account's tokens since start, the beginning of
// the window named window. The device counted them itself when its reading
// of the window began then; otherwise they are its days from start's day
// on, which may count part of that day too many.
func (a devAccount) sinceStart(window string, start time.Time, collectedAt time.Time) int64 {
	if collectedAt.Before(start) {
		return 0
	}
	for _, r := range a.recent {
		if r.Window == window && r.Start.Sub(start).Abs() <= time.Hour {
			return r.Tokens
		}
	}
	n := int(collectedAt.Unix()/86400 - start.Unix()/86400)
	var sum int64
	for i := 0; i <= n && i < len(a.days); i++ {
		sum += a.days[i]
	}
	return sum
}
