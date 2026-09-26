package view

import (
	"cmp"
	"math"
	"slices"
	"sort"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// login is what a Hermes account on one device spent through one login, in
// input plus output tokens.
type login struct {
	link   Link
	tokens int64
}

// split divides a Hermes account on one device among the logins it spent
// through, in proportion to what the device counted through each. The
// snapshot does not split the account's days, or its tokens since a window
// began, by login, so each login gets that share of them, and so does what
// the device counted through no login, as from before it recorded logins.
// An account that spent through no login stays whole, on its link.
func (a devAccount) split(logins []login, shift int) []devAccount {
	var parts []devAccount
	var weights []int64
	for _, l := range logins {
		if l.tokens > 0 {
			parts = append(parts, devAccount{provider: a.provider, label: a.label, last: a.last, link: &l.link})
			weights = append(weights, l.tokens)
		}
	}
	if subscription(a.provider) || len(parts) == 0 {
		return []devAccount{a}
	}
	for _, n := range a.days {
		for i, v := range shares(n, weights) {
			parts[i].days = append(parts[i].days, v)
		}
	}
	for _, r := range a.recent {
		for i, v := range shares(r.Tokens, weights) {
			parts[i].recent = append(parts[i].recent, snapshot.Recent{Window: r.Window, Start: r.Start, Tokens: v})
		}
	}
	for i := range parts {
		parts[i].usage = usageOf(parts[i].days, shift)
	}
	return parts
}

// shares divides n in proportion to weights, in whole tokens that add up to
// n.
func shares(n int64, weights []int64) []int64 {
	var sum, upTo, prev int64
	for _, w := range weights {
		sum += w
	}
	out := make([]int64, len(weights))
	for i, w := range weights {
		upTo += w
		next := int64(math.Round(float64(n) * float64(upTo) / float64(sum)))
		out[i], prev = next-prev, next
	}
	return out
}

// wireLink is the link of the i-th account of a device doc, from the wire
// alone: a borrowed reading belongs to the one account of the provider it
// came from with the same reading. With none, or several, the label is empty.
func wireLink(accts []snapshot.Account, labels []string, i int) *Link {
	a := accts[i]
	if a.QuotaFrom == "" {
		return nil
	}
	link := &Link{Provider: a.QuotaFrom}
	matches := 0
	for j, b := range accts {
		if b.Provider == a.QuotaFrom && sameReading(a, b) {
			link.Label = labels[j]
			matches++
		}
	}
	if matches != 1 {
		link.Label = ""
	}
	return link
}

func sameReading(a, b snapshot.Account) bool {
	if a.QuotaAt == nil || b.QuotaAt == nil || !a.QuotaAt.Equal(*b.QuotaAt) {
		return false
	}
	return slices.EqualFunc(a.Windows, b.Windows, func(w, v snapshot.Window) bool {
		return w.Name == v.Name && w.Percent == v.Percent && w.Minutes == v.Minutes && sameTime(w.ResetsAt, v.ResetsAt)
	})
}

// addLinked counts what account (provider, label) on one device spent
// through the account (to, toLabel).
func addLinked(linked map[string]map[string]*LinkedUsage, to, toLabel, provider, label, device string, a snapshot.Linked) {
	target := state.Key(to, toLabel)
	if linked[target] == nil {
		linked[target] = map[string]*LinkedUsage{}
	}
	k := state.Key(provider, label)
	u := linked[target][k]
	if u == nil {
		u = &LinkedUsage{Provider: provider, Label: label, Devices: []string{}}
		linked[target][k] = u
	}
	u.Devices = append(u.Devices, device)
	u.Sessions += a.Sessions
	u.Tokens = u.Tokens.Add(a.Tokens)
}

func linkedList(m map[string]*LinkedUsage) []LinkedUsage {
	out := []LinkedUsage{}
	for _, u := range m {
		sort.Strings(u.Devices)
		out = append(out, *u)
	}
	slices.SortFunc(out, func(a, b LinkedUsage) int {
		return cmp.Or(cmp.Compare(b.Tokens.Total(), a.Tokens.Total()), cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Label, b.Label))
	})
	return out
}
