package view

import (
	"cmp"
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// colKey is a matrix column's key: a subscription's provider and label, or
// a provider whose tokens have no quota.
type colKey struct {
	provider, label string
	noQuota         bool
}

// matrix is every device against every subscription, and the tokens with
// no quota, one column per provider, in byBurn's order for 7d: the rows,
// the providers' groups, and the columns in each.
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
			c.Burn = c.Burn.add(a.burn)
			if x != nil && x.main != "" {
				tokens, burn := a.sinceStart(x.main, x.start, dv.collectedAt)
				c.WindowTokens += tokens
				c.WindowBurn += burn
			}
			row.Usage = row.Usage.add(a.usage)
			row.Burn = row.Burn.add(a.burn)
			row.BurnEstimated = row.BurnEstimated || a.estimated
		}
		for i, c := range row.Cells {
			col := &mx.Columns[i]
			col.Usage, col.Burn = col.Usage.add(c.Usage), col.Burn.add(c.Burn)
			col.WindowTokens += c.WindowTokens
			col.WindowBurn += c.WindowBurn
		}
		mx.Rows = append(mx.Rows, row)
	}
	var all, allBurn Usage
	for _, row := range mx.Rows {
		all, allBurn = all.add(row.Usage), allBurn.add(row.Burn)
	}
	for r := range mx.Rows {
		row := &mx.Rows[r]
		for i := range row.Cells {
			c := &row.Cells[i]
			c.Share, c.BurnShare = shareOf(c.Usage, mx.Columns[i].Usage), shareOf(c.Burn, mx.Columns[i].Burn)
		}
		row.Share, row.BurnShare = shareOf(row.Usage, all), shareOf(row.Burn, allBurn)
	}
	order := columnOrder(mx.Columns, Week)
	mx.Columns = reorder(mx.Columns, order)
	for r := range mx.Rows {
		mx.Rows[r].Cells = reorder(mx.Rows[r].Cells, order)
	}
	sortRows(mx.Rows, Week)
	return mx
}

// sortRows puts the matrix's rows in byBurn's order for the period. The
// status view shows its rows in the same order.
func sortRows(rows []Row, per Period) {
	by := byBurn(per, func(r Row) Usage { return r.Burn }, func(r Row) string { return r.Device })
	slices.SortStableFunc(rows, func(a, b Row) int { return cmp.Or(by(a, b), cmp.Compare(a.DeviceID, b.DeviceID)) })
}

// columnOrder is the order of the matrix's columns for the period: the
// providers' groups of subscriptions in byBurn's order of what each group
// burned, each group's subscriptions in byBurn's order, then the columns
// with no quota, in byBurn's order too. They are indexes into cols.
func columnOrder(cols []Column, per Period) []int {
	group := map[string]Usage{}
	for _, c := range cols {
		if !c.NoQuota {
			group[c.Provider] = group[c.Provider].add(c.Burn)
		}
	}
	byGroup := byBurn(per, func(p string) Usage { return group[p] }, func(p string) string { return p })
	byColumn := byBurn(per, func(i int) Usage { return cols[i].Burn }, func(i int) string { return cols[i].Name })
	order := make([]int, len(cols))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(i, j int) int {
		a, b := cols[i], cols[j]
		switch {
		case a.NoQuota != b.NoQuota:
			if a.NoQuota {
				return 1
			}
			return -1
		case !a.NoQuota && a.Provider != b.Provider:
			return byGroup(a.Provider, b.Provider)
		}
		return byColumn(i, j)
	})
	return order
}

// reorder is xs in order, a list of its indexes.
func reorder[T any](xs []T, order []int) []T {
	out := make([]T, 0, len(xs))
	for _, i := range order {
		out = append(out, xs[i])
	}
	return out
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
			if x := byProv[tp.Provider][snapshot.LabelKey(a.Label)]; x != nil && x.main != "" {
				c.WindowStart = timePtr(x.start)
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
		return byProv[a.provider][snapshot.LabelKey(a.label)]
	}
	if a.link == nil || a.link.Label == "" {
		return nil
	}
	return byProv[a.link.Provider][snapshot.LabelKey(a.link.Label)]
}

// users counts the devices that burned on each account since its main
// window began, what linked accounts spent through it included, or in the
// last 7 days without a main window. The busiest is the one that burned the
// most.
func users(m map[string]*teamAccount, byProv map[string]map[string]*teamAccount, devs []*device) {
	for _, x := range m {
		var best int64
		for _, dv := range devs {
			var n int64
			for _, a := range dv.accts {
				own := a.provider == x.provider && snapshot.LabelKey(a.label) == snapshot.LabelKey(x.ta.Label)
				if !own && billsTo(a, byProv) != x {
					continue
				}
				if x.main != "" {
					_, burn := a.sinceStart(x.main, x.start, dv.collectedAt)
					n += burn
				} else {
					n += a.burn.Week
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

// sinceStart is a device account's tokens and burn since start, the
// beginning of the window named window. The device counted them itself when
// its reading of the window began then; otherwise they are its days from
// start's day on, which may count part of that day too many.
func (a devAccount) sinceStart(window string, start time.Time, collectedAt time.Time) (tokens, burn int64) {
	if collectedAt.Before(start) {
		return 0, 0
	}
	for _, r := range a.recent {
		if r.Window == window && r.Start.Sub(start).Abs() <= time.Hour {
			return r.Tokens, r.Burn
		}
	}
	return collect.DaysSince(a.days, start, collectedAt), collect.DaysSince(a.burnDays, start, collectedAt)
}
