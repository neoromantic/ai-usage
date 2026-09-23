package view

import (
	"sort"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// teamAccount is one team account while the device docs are merged.
type teamAccount struct {
	ta TeamAccount
	// wins are the newest reading of each window, by name, and the device
	// and provider each came from.
	wins   map[string]*winReading
	order  []string
	qLink  *Link // the link on the device that sent the newest reading
	qAt    time.Time
	local  *Link // this device's own link, which wins
	planAt time.Time
}

type winReading struct {
	w    snapshot.Window
	at   time.Time
	dev  string
	from string
}

func buildTeam(in Input, totals []collect.AccountTotals, now time.Time) Team {
	t := Team{Devices: []TeamDevice{}, Providers: []TeamProvider{}, Matrix: Matrix{Columns: []Column{}, Rows: []Row{}}}
	docs := []snapshot.Doc{in.Doc}
	if in.Team.Team == in.Key.Fingerprint() {
		t.PulledAt = timePtr(in.Team.PulledAt)
		for _, d := range in.Team.Docs {
			if d.Device != in.Doc.Device {
				docs = append(docs, d)
			}
		}
	}
	open := func(s string) string {
		v, err := in.Key.Open(s)
		if err != nil {
			return "(unreadable)"
		}
		return snapshot.Printable(v)
	}
	// This device knows its links even when the linked account has no
	// reading to match on the wire.
	localLinks := map[string]*Link{}
	for _, a := range totals {
		localLinks[state.Key(a.Provider, a.Label)] = linkView(a.Link)
	}

	byProv := map[string]map[string]*teamAccount{}
	// linked holds, by the account billed, what linked accounts spent.
	linked := map[string]map[string]*LinkedUsage{}
	for _, d := range docs {
		label := open(d.DeviceLabel)
		dev := TeamDevice{
			Device:           d.Device,
			Label:            label,
			OSUser:           open(d.OSUser),
			This:             d.Device == in.Doc.Device,
			CollectorVersion: d.CollectorVersion,
			CollectedAt:      d.CollectedAt,
			AgeSeconds:       int64(now.Sub(d.CollectedAt).Seconds()),
			LastSuccessAt:    timePtr(d.LastSuccessAt),
			LastError:        strPtr(open(d.LastError)),
			Sources:          []Source{},
		}
		for _, s := range d.Sources {
			dev.Sources = append(dev.Sources, Source{Provider: s.Provider, Status: s.Status, Error: strPtr(open(s.Error))})
		}
		shift := dayShift(d.CollectedAt, now)

		devName := label + " (" + dev.OSUser + ")"
		labels := make([]string, len(d.Accounts))
		for i, a := range d.Accounts {
			labels[i] = open(a.Label)
		}
		for i, a := range d.Accounts {
			if byProv[a.Provider] == nil {
				byProv[a.Provider] = map[string]*teamAccount{}
			}
			l := labels[i]
			x := byProv[a.Provider][l]
			if x == nil {
				x = &teamAccount{
					ta: TeamAccount{
						Label: l, Name: l, Subscription: a.Provider != "hermes",
						Devices: []string{}, State: StateUnknown, PerDevice: []DeviceUsage{}, LinkedUsage: []LinkedUsage{},
					},
					wins: map[string]*winReading{},
				}
				byProv[a.Provider][l] = x
			}
			usage := usageOf(a.Days, shift)
			dev.Usage = dev.Usage.add(usage)
			x.ta.Devices = append(x.ta.Devices, devName)
			x.ta.Sessions += a.Sessions
			x.ta.Tokens = x.ta.Tokens.Add(a.Tokens)
			x.ta.Usage = x.ta.Usage.add(usage)
			if dev.This && a.Current {
				x.ta.Current = true
			}
			x.ta.PerDevice = append(x.ta.PerDevice, DeviceUsage{
				Device: devName, DeviceID: d.Device, Current: a.Current,
				Sessions: a.Sessions, Tokens: a.Tokens, Usage: usage, LastActiveAt: timeOf(a.LastActiveAt),
			})
			if a.LastActiveAt != nil && (x.ta.LastActiveAt == nil || a.LastActiveAt.After(*x.ta.LastActiveAt)) {
				x.ta.LastActiveAt = timeOf(a.LastActiveAt)
			}
			if a.Plan != "" && (x.ta.Plan == nil || d.CollectedAt.After(x.planAt)) {
				x.ta.Plan, x.planAt = strPtr(a.Plan), d.CollectedAt
			}
			link := wireLink(d.Accounts, labels, i)
			if dev.This {
				link = localLinks[state.Key(a.Provider, l)]
				x.local = link
			}
			if a.QuotaAt != nil {
				for _, w := range a.Windows {
					cur := x.wins[w.Name]
					if cur == nil {
						x.order = append(x.order, w.Name)
					}
					if cur == nil || a.QuotaAt.After(cur.at) {
						x.wins[w.Name] = &winReading{w: w, at: *a.QuotaAt, dev: devName, from: a.QuotaFrom}
					}
				}
				if len(a.Windows) > 0 && a.QuotaAt.After(x.qAt) {
					x.qAt, x.qLink = *a.QuotaAt, link
				}
			}
			for _, u := range a.Linked {
				addLinked(linked, a.Provider, l, u.Provider, open(u.Label), devName, u)
			}
		}
		t.Devices = append(t.Devices, dev)
	}
	sort.Slice(t.Devices, func(i, j int) bool {
		a, b := t.Devices[i], t.Devices[j]
		if a.This != b.This {
			return a.This
		}
		if a.Label != b.Label {
			return a.Label < b.Label
		}
		return a.Device < b.Device
	})
	for _, p := range collect.Providers {
		m := byProv[p]
		if len(m) == 0 {
			continue
		}
		tp := TeamProvider{Provider: p}
		for _, x := range m {
			x.ta.Link = x.local
			if x.ta.Link == nil {
				x.ta.Link = x.qLink
			}
			if len(x.wins) > 0 {
				x.ta.Quota = x.quota(now)
			}
			sort.Strings(x.ta.Devices)
			sortPerDevice(x.ta.PerDevice)
			x.ta.LinkedUsage = linkedList(linked[state.Key(p, x.ta.Label)])
			tp.Accounts = append(tp.Accounts, x.ta)
		}
		sortTeamAccounts(tp.Accounts)
		t.Providers = append(t.Providers, tp)
	}
	return t
}

// quota is the team account's reading: the newest reading of each window,
// in the order the windows first came.
func (x *teamAccount) quota(now time.Time) *Quota {
	var rs []reading
	newest := x.wins[x.order[0]]
	for _, name := range x.order {
		wr := x.wins[name]
		rs = append(rs, reading{wr.w, wr.at})
		if wr.at.After(newest.at) {
			newest = wr
		}
	}
	q := quotaView(newest.at, "", newest.dev, now)
	q.From = newest.from
	q.Windows, x.ta.State = readQuota(rs, now)
	return q
}

// sortTeamAccounts puts the worst state first, ties to the one with less
// left, then by label.
func sortTeamAccounts(as []TeamAccount) {
	sort.SliceStable(as, func(i, j int) bool {
		a, b := as[i], as[j]
		if ra, rb := stateRank(a.State), stateRank(b.State); ra != rb {
			return ra < rb
		}
		if la, lb := left(a.Quota), left(b.Quota); la != lb {
			return la < lb
		}
		return a.Label < b.Label
	})
}

// left is what is left of a quota's main window, in percent; 101 with none,
// so an account with no reading sorts after one with a reading.
func left(q *Quota) float64 {
	if q == nil {
		return 101
	}
	m := mainWindow(q.Windows)
	if m == nil || m.Reset {
		return 101
	}
	return max(100-m.Percent, 0)
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
	if a.QuotaAt == nil || b.QuotaAt == nil || !a.QuotaAt.Equal(*b.QuotaAt) || len(a.Windows) != len(b.Windows) {
		return false
	}
	for i, w := range a.Windows {
		v := b.Windows[i]
		if w.Name != v.Name || w.Percent != v.Percent || w.Minutes != v.Minutes || !sameTime(w.ResetsAt, v.ResetsAt) {
			return false
		}
	}
	return true
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
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
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Tokens.Total() != b.Tokens.Total() {
			return a.Tokens.Total() > b.Tokens.Total()
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Label < b.Label
	})
	return out
}

// sortPerDevice puts the device that used the account most first.
func sortPerDevice(ds []DeviceUsage) {
	sort.Slice(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.Tokens.Total() != b.Tokens.Total() {
			return a.Tokens.Total() > b.Tokens.Total()
		}
		if a.Device != b.Device {
			return a.Device < b.Device
		}
		return a.DeviceID < b.DeviceID
	})
}
