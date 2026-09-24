package view

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// teamAccount is one team account while the device docs are merged.
type teamAccount struct {
	provider string
	ta       TeamAccount
	// wins are the newest reading of each window, by name, and the device
	// and provider each came from.
	wins   map[string]*winReading
	order  []string
	qLink  *Link // the link on the device that sent the newest reading
	qAt    time.Time
	local  *Link // this device's own link, which wins
	planAt time.Time
	// start is when the main window began, when it is known and has not
	// reset; main is its name.
	start time.Time
	main  string
}

type winReading struct {
	w    snapshot.Window
	at   time.Time
	dev  string
	from string
}

// devAccount is one account on one device doc, or the part of a Hermes
// account that went through one login.
type devAccount struct {
	provider, label string
	usage           Usage
	days            []int64
	recent          []snapshot.Recent
	// link is the account a Hermes account bills through on that device.
	link *Link
}

// login is what a Hermes account on one device spent through one login, in
// input plus output tokens.
type login struct {
	link   Link
	tokens int64
}

// device is one device doc while the team is merged.
type device struct {
	dev         *TeamDevice
	name        string // label (OS user), as account lists show it
	collectedAt time.Time
	shift       int
	accts       []devAccount
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
	var devs []*device
	aliases := map[string]snapshot.Alias{}
	aliasBy := map[string]string{}
	for _, d := range docs {
		label := open(d.DeviceLabel)
		dev := &TeamDevice{
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
			Silent:           now.Sub(d.CollectedAt) > SilentAfter,
		}
		for _, s := range d.Sources {
			dev.Sources = append(dev.Sources, Source{Provider: s.Provider, Status: s.Status, Error: strPtr(open(s.Error))})
		}
		dev.Error = deviceError(*dev)
		dv := &device{dev: dev, name: label + " (" + dev.OSUser + ")", collectedAt: d.CollectedAt, shift: dayShift(d.CollectedAt, now)}
		devs = append(devs, dv)
		for _, a := range d.Aliases {
			if a.Name != "" {
				// A name the alias command would refuse is not one.
				if a.Name = open(a.Name); snapshot.CheckAlias(a.Name) != nil {
					continue
				}
			}
			k := aliasKey(a.Provider, open(a.Label))
			// The newest wins, and of two set at once, the one from the
			// smaller device id, as the alias command picks.
			if cur, ok := aliases[k]; !ok || a.At.After(cur.At) || (a.At.Equal(cur.At) && d.Device < aliasBy[k]) {
				aliases[k], aliasBy[k] = a, d.Device
			}
		}

		labels := make([]string, len(d.Accounts))
		for i, a := range d.Accounts {
			labels[i] = open(a.Label)
		}
		// through is, by Hermes account, what it spent through each login.
		through := map[string][]login{}
		for i, a := range d.Accounts {
			for _, u := range a.Linked {
				k := state.Key(u.Provider, open(u.Label))
				through[k] = append(through[k], login{Link{a.Provider, labels[i]}, logs.InOut(u.Tokens)})
			}
		}
		for i, a := range d.Accounts {
			if byProv[a.Provider] == nil {
				byProv[a.Provider] = map[string]*teamAccount{}
			}
			l := labels[i]
			x := byProv[a.Provider][l]
			if x == nil {
				x = &teamAccount{
					provider: a.Provider,
					ta: TeamAccount{
						Label: l, Name: l, Subscription: subscription(a.Provider),
						Devices: []string{}, State: StateUnknown, PerDevice: []DeviceUsage{}, LinkedUsage: []LinkedUsage{},
					},
					wins: map[string]*winReading{},
				}
				byProv[a.Provider][l] = x
			}
			usage := usageOf(a.Days, dv.shift)
			dev.Usage = dev.Usage.add(usage)
			x.ta.Devices = append(x.ta.Devices, dv.name)
			x.ta.Sessions += a.Sessions
			x.ta.Tokens = x.ta.Tokens.Add(a.Tokens)
			x.ta.Usage = x.ta.Usage.add(usage)
			if dev.This && a.Current {
				x.ta.Current = true
			}
			x.ta.PerDevice = append(x.ta.PerDevice, DeviceUsage{
				Device: dv.name, DeviceID: d.Device, Current: a.Current,
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
						x.wins[w.Name] = &winReading{w: w, at: *a.QuotaAt, dev: dv.name, from: a.QuotaFrom}
					}
				}
				if len(a.Windows) > 0 && a.QuotaAt.After(x.qAt) {
					x.qAt, x.qLink = *a.QuotaAt, link
				}
			}
			for _, u := range a.Linked {
				addLinked(linked, a.Provider, l, u.Provider, open(u.Label), dv.name, u)
			}
			da := devAccount{provider: a.Provider, label: l, usage: usage, days: a.Days, recent: a.Recent, link: link}
			dv.accts = append(dv.accts, da.split(through[state.Key(a.Provider, l)], dv.shift)...)
		}
	}

	t.Latest = strPtr(latestVersion(in.State.Update.Latest, devs))
	for _, dv := range devs {
		d := dv.dev
		d.Old = t.Latest != nil && selfupdate.Newer(*t.Latest, d.CollectorVersion)
	}

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
				x.ta.Quota = x.quota(p, now)
				if mw := mainWindow(x.ta.Quota.Windows); mw != nil && !mw.Reset && !mw.Unread {
					if s, ok := x.wins[mw.Name].w.Start(); ok {
						x.start, x.main = s, mw.Name
					}
				}
			}
			sort.Strings(x.ta.Devices)
			sortPerDevice(x.ta.PerDevice)
			x.ta.LinkedUsage = linkedList(linked[state.Key(p, x.ta.Label)])
		}
		names(p, m, aliases)
		users(p, m, devs)
		for _, x := range m {
			tp.Accounts = append(tp.Accounts, x.ta)
		}
		sortTeamAccounts(tp.Accounts)
		t.Providers = append(t.Providers, tp)
	}
	t.Matrix = matrix(t.Providers, byProv, devs)
	for _, dv := range devs {
		t.Devices = append(t.Devices, *dv.dev)
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
	return t
}

// subscription says a provider's accounts have a quota of their own. Hermes
// is a harness: it spends through other logins, or through keys with none.
func subscription(provider string) bool { return provider != "hermes" }

// deviceError is what fails on a device now: a source's error, named after
// its provider, else the last run's error when no success followed it.
func deviceError(d TeamDevice) *string {
	for _, s := range d.Sources {
		if s.Status != "error" && s.Status != "partial" {
			continue
		}
		if s.Error != nil {
			return strPtr(sourceError(s.Provider, *s.Error))
		}
		return strPtr(s.Provider + ": " + s.Status)
	}
	if d.LastError != nil && (d.LastSuccessAt == nil || d.CollectedAt.After(*d.LastSuccessAt)) {
		return d.LastError
	}
	return nil
}

// sourceError names the provider once before a source's error. A harness's
// own messages already start with its name, as "codex: not logged in" does.
func sourceError(p, msg string) string {
	if strings.HasPrefix(msg, p+":") || strings.HasPrefix(msg, p+" ") {
		return msg
	}
	return p + ": " + msg
}

// latestVersion is the newest release this device's update check or any
// device knows.
func latestVersion(checked string, devs []*device) string {
	best := ""
	consider := func(v string) {
		if !selfupdate.Dev(v) && (best == "" || selfupdate.Newer(v, best)) {
			best = v
		}
	}
	consider(checked)
	for _, d := range devs {
		consider(d.dev.CollectorVersion)
	}
	return best
}

// quota is the team account's reading: the newest reading of each window,
// in the order the windows first came.
func (x *teamAccount) quota(provider string, now time.Time) *Quota {
	var rs []reading
	newest := x.wins[x.order[0]]
	for _, name := range x.order {
		wr := x.wins[name]
		rs = append(rs, reading{Window: wr.w, At: wr.at})
		if wr.at.After(newest.at) {
			newest = wr
		}
	}
	q := quotaView(newest.at, "", newest.dev, now)
	q.From = newest.from
	q.Windows, x.ta.State = readQuota(withUnread(provider, rs), now)
	q.Stale = anyStale(q.Windows)
	return q
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
		if r.Window == window && absDuration(r.Start.Sub(start)) <= time.Hour {
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

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
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
			parts = append(parts, devAccount{provider: a.provider, label: a.label, link: &l.link})
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

// names gives each account of a provider its short name: the alias the
// team gave it, else the part of an email before the @, else the first 8
// characters of an id. When two accounts go by the same name, in any case,
// as the alias command compares names, both go by the full label. A device
// can give an account a name before it reads another device's account that
// goes by it.
func names(provider string, m map[string]*teamAccount, aliases map[string]snapshot.Alias) {
	for _, x := range m {
		x.ta.Name = ShortName(x.ta.Label)
		if a, ok := aliases[aliasKey(provider, x.ta.Label)]; ok && a.Name != "" {
			x.ta.Name, x.ta.Alias = a.Name, strPtr(a.Name)
		}
	}
	// A full label can be another account's name too, so this goes on until
	// no name is taken twice. A full label stays, so it ends.
	for {
		count := map[string]int{}
		for _, x := range m {
			count[strings.ToLower(x.ta.Name)]++
		}
		same := false
		for _, x := range m {
			if count[strings.ToLower(x.ta.Name)] > 1 && x.ta.Name != x.ta.Label {
				x.ta.Name, same = x.ta.Label, true
			}
		}
		if !same {
			return
		}
	}
}

// aliasKey keys an alias by provider and label, with an email in any case.
func aliasKey(provider, label string) string {
	if strings.Contains(label, "@") {
		label = strings.ToLower(label)
	}
	return state.Key(provider, label)
}

// ShortName is a label's short name when the team gave it none, before
// collisions: the part of an email before the @, the first 8 characters of
// an id, else the whole label.
func ShortName(label string) string {
	if i := strings.Index(label, "@"); i > 0 {
		return label[:i]
	}
	if idLike(label) {
		return label[:8]
	}
	return label
}

// idLike is a label that is an opaque id: a UUID, or a long run of letters
// and digits with digits in it.
func idLike(s string) bool {
	if uuidRe.MatchString(s) {
		return true
	}
	if len(s) < 16 {
		return false
	}
	digits := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_':
		default:
			return false
		}
	}
	return digits
}

// matrix is every device against every subscription, and the tokens with
// no quota, one column per provider.
func matrix(providers []TeamProvider, byProv map[string]map[string]*teamAccount, devs []*device) Matrix {
	mx := Matrix{Columns: []Column{}, Rows: []Row{}}
	type colKey struct {
		provider, label string
		noQuota         bool
	}
	index := map[colKey]int{}
	for _, tp := range providers {
		for _, a := range tp.Accounts {
			if !a.Subscription {
				continue
			}
			c := Column{Provider: tp.Provider, Label: a.Label, Name: a.Name, State: a.State}
			if a.Quota != nil {
				if mw := mainWindow(a.Quota.Windows); mw != nil && !mw.Reset && !mw.Unread {
					p := mw.Percent
					c.Percent = &p
				}
			}
			index[colKey{tp.Provider, a.Label, false}] = len(mx.Columns)
			mx.Columns = append(mx.Columns, c)
		}
	}
	// Tokens with no quota get a column per provider after the rest.
	for _, dv := range devs {
		for _, a := range dv.accts {
			if billsTo(a, byProv) != nil {
				continue
			}
			k := colKey{a.provider, "", true}
			if _, ok := index[k]; !ok {
				index[k] = -1
			}
		}
	}
	for _, p := range collect.Providers {
		k := colKey{p, "", true}
		if _, ok := index[k]; ok {
			index[k] = len(mx.Columns)
			mx.Columns = append(mx.Columns, Column{Provider: p, Name: p, NoQuota: true, State: StateUnknown})
		}
	}

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
	for _, row := range mx.Rows {
		for i := range row.Cells {
			col := mx.Columns[i]
			if col.Percent == nil || col.WindowTokens <= 0 {
				continue
			}
			s := float64(row.Cells[i].WindowTokens) / float64(col.WindowTokens) * *col.Percent
			row.Cells[i].Share = &s
		}
	}
	sort.SliceStable(mx.Rows, func(i, j int) bool {
		a, b := mx.Rows[i], mx.Rows[j]
		if a.Usage.Week != b.Usage.Week {
			return a.Usage.Week > b.Usage.Week
		}
		if a.Device != b.Device {
			return a.Device < b.Device
		}
		return a.DeviceID < b.DeviceID
	})
	return mx
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
	if m == nil || m.Reset || m.Unread {
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
