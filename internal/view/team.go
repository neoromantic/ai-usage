package view

import (
	"cmp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
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
	// last is the account's newest activity on that device.
	last *time.Time
	// link is the account a Hermes account bills through on that device.
	link *Link
}

// device is one device doc while the team is merged.
type device struct {
	dev         *TeamDevice
	name        string // label (OS user), as account lists show it
	collectedAt time.Time
	shift       int
	accts       []devAccount
}

// merger merges the device docs into the team.
type merger struct {
	in  Input
	now time.Time
	// local holds this device's links, by account.
	local  map[string]*Link
	byProv map[string]map[string]*teamAccount
	// linked holds, by the account billed, what linked accounts spent.
	linked map[string]map[string]*LinkedUsage
	devs   []*device
	// aliases holds the newest alias of each account, and aliasBy the
	// device it came from.
	aliases map[string]snapshot.Alias
	aliasBy map[string]string
	behind  map[string]collect.Behind
}

func buildTeam(in Input, totals []collect.AccountTotals, now time.Time) Team {
	t := Team{Devices: []TeamDevice{}, Providers: []TeamProvider{}}
	m := &merger{
		in: in, now: now, local: map[string]*Link{},
		byProv: map[string]map[string]*teamAccount{}, linked: map[string]map[string]*LinkedUsage{},
		aliases: map[string]snapshot.Alias{}, aliasBy: map[string]string{},
	}
	docs := []snapshot.Doc{in.Doc}
	if in.Team.Team == in.Key.Fingerprint() {
		t.PulledAt = timePtr(in.Team.PulledAt)
		m.behind = in.Team.Behind
		for _, d := range in.Team.Docs {
			if d.Device != in.Doc.Device {
				docs = append(docs, d)
			}
		}
	}
	// This device knows its links even when the linked account has no
	// reading to match on the wire.
	for _, a := range totals {
		m.local[state.Key(a.Provider, a.Label)] = linkView(a.Link)
	}
	for _, d := range docs {
		m.addDoc(d)
	}
	m.hermesActivity()
	t.Latest = strPtr(latestVersion(in.State.Update.Latest, m.devs))
	m.markOld(t.Latest)
	for _, p := range snapshot.Providers {
		if tp, ok := m.provider(p); ok {
			t.Providers = append(t.Providers, tp)
		}
	}
	t.Matrix = matrix(t.Providers, m.byProv, m.devs)
	for _, dv := range m.devs {
		t.Devices = append(t.Devices, *dv.dev)
	}
	slices.SortFunc(t.Devices, func(a, b TeamDevice) int {
		if a.This != b.This {
			if a.This {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.Label, b.Label), cmp.Compare(a.Device, b.Device))
	})
	return t
}

// raw is a sealed string as it was sealed, line breaks and all.
func (m *merger) raw(s string) string {
	v, err := m.in.Key.Open(s)
	if err != nil {
		return "(unreadable)"
	}
	return v
}

func (m *merger) open(s string) string { return snapshot.Printable(m.raw(s)) }

// addDoc merges one device doc: the device, its aliases and its accounts.
func (m *merger) addDoc(d snapshot.Doc) {
	dev := m.teamDevice(d)
	dv := &device{dev: dev, name: dev.Label + " (" + dev.OSUser + ")", collectedAt: d.CollectedAt, shift: dayShift(d.CollectedAt, m.now)}
	m.devs = append(m.devs, dv)
	m.addAliases(d)
	m.addAccounts(dv, d)
}

// teamDevice is a device doc's device, before its usage and release.
func (m *merger) teamDevice(d snapshot.Doc) *TeamDevice {
	label := m.open(d.DeviceLabel)
	this := d.Device == m.in.Doc.Device
	lastErr, updateErr := m.lastErrors(d, this)
	dev := &TeamDevice{
		Device:           d.Device,
		Label:            label,
		OSUser:           m.open(d.OSUser),
		This:             this,
		CollectorVersion: d.CollectorVersion,
		CollectedAt:      d.CollectedAt,
		AgeSeconds:       int64(m.now.Sub(d.CollectedAt).Seconds()),
		LastSuccessAt:    timePtr(d.LastSuccessAt),
		LastError:        lastErr,
		UpdateError:      updateErr,
		Sources:          []Source{},
		Silent:           m.now.Sub(d.CollectedAt) > collect.SilentAfter,
	}
	for _, s := range d.Sources {
		dev.Sources = append(dev.Sources, Source{Provider: s.Provider, Status: s.Status, Error: strPtr(m.open(s.Error))})
	}
	dev.Error = deviceError(*dev)
	return dev
}

// lastErrors are a device doc's last error and update error, printable.
func (m *merger) lastErrors(d snapshot.Doc, this bool) (lastErr, updateErr *string) {
	last, update := snapshot.SplitLastError(m.raw(d.LastError))
	// This device's own update shows in the header and ATTENTION.
	if !this {
		updateErr = strPtr(snapshot.Printable(update))
	}
	return strPtr(snapshot.Printable(last)), updateErr
}

// account is the team account of provider and label, made when it is new.
func (m *merger) account(provider, label string) *teamAccount {
	if m.byProv[provider] == nil {
		m.byProv[provider] = map[string]*teamAccount{}
	}
	x := m.byProv[provider][label]
	if x == nil {
		x = &teamAccount{
			provider: provider,
			ta: TeamAccount{
				Label: label, Subscription: subscription(provider),
				Devices: []string{}, State: StateUnknown, PerDevice: []DeviceUsage{},
			},
			wins: map[string]*winReading{},
		}
		m.byProv[provider][label] = x
	}
	return x
}

// addAccounts adds a device doc's accounts to the team accounts and to the
// device dv.
func (m *merger) addAccounts(dv *device, d snapshot.Doc) {
	dev := dv.dev
	labels := make([]string, len(d.Accounts))
	for i, a := range d.Accounts {
		labels[i] = m.open(a.Label)
	}
	// through is, by Hermes account, what it spent through each login.
	through := map[string][]login{}
	for i, a := range d.Accounts {
		for _, u := range a.Linked {
			k := state.Key(u.Provider, m.open(u.Label))
			through[k] = append(through[k], login{Link{a.Provider, labels[i]}, u.Tokens.InOut()})
		}
	}
	for i, a := range d.Accounts {
		l := labels[i]
		x := m.account(a.Provider, l)
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
		x.active(a.LastActiveAt)
		if a.Plan != "" && (x.ta.Plan == nil || d.CollectedAt.After(x.planAt)) {
			x.ta.Plan, x.planAt = strPtr(a.Plan), d.CollectedAt
		}
		link := wireLink(d.Accounts, labels, i)
		if dev.This {
			link = m.local[state.Key(a.Provider, l)]
			x.local = link
		}
		x.addReading(a, dv.name, link)
		for _, u := range a.Linked {
			addLinked(m.linked, a.Provider, l, u.Provider, m.open(u.Label), dv.name, u)
		}
		da := devAccount{provider: a.Provider, label: l, usage: usage, days: a.Days, recent: a.Recent,
			last: a.LastActiveAt, link: link}
		dv.accts = append(dv.accts, da.split(through[state.Key(a.Provider, l)], dv.shift)...)
	}
}

// addReading merges a's reading, sent by device dev, into the newest
// reading of each window, and keeps link when a's reading is the newest.
func (x *teamAccount) addReading(a snapshot.Account, dev string, link *Link) {
	if a.QuotaAt == nil {
		return
	}
	for _, w := range a.Windows {
		cur := x.wins[w.Name]
		if cur == nil {
			x.order = append(x.order, w.Name)
		}
		if cur == nil || a.QuotaAt.After(cur.at) {
			x.wins[w.Name] = &winReading{w: w, at: *a.QuotaAt, dev: dev, from: a.QuotaFrom}
		}
	}
	if len(a.Windows) > 0 && a.QuotaAt.After(x.qAt) {
		x.qAt, x.qLink = *a.QuotaAt, link
	}
}

// hermesActivity gives the logins Hermes spent through its newest activity.
// What Hermes spends through a login is that login's use, and its newest
// activity is the login's too. A device whose Hermes spent through several
// logins gives each one its newest activity, since its snapshot does not
// say which login that went through.
func (m *merger) hermesActivity() {
	for _, dv := range m.devs {
		for _, a := range dv.accts {
			if x := billsTo(a, m.byProv); x != nil && !subscription(a.provider) {
				x.active(a.last)
			}
		}
	}
}

// markOld marks the devices on a release older than latest and, when the
// team cache found a device behind on the release it runs, since when.
func (m *merger) markOld(latest *string) {
	for _, dv := range m.devs {
		d := dv.dev
		d.Old = latest != nil && selfupdate.Newer(*latest, d.CollectorVersion)
		if b, ok := m.behind[d.Device]; ok && d.Old && b.Version == d.CollectorVersion {
			d.BehindSince = timePtr(b.Since)
		}
		d.NotUpdating = notUpdating(*d)
	}
}

// provider is provider p's part of the team, and false when no device has
// an account of it.
func (m *merger) provider(p string) (TeamProvider, bool) {
	accts := m.byProv[p]
	if len(accts) == 0 {
		return TeamProvider{}, false
	}
	tp := TeamProvider{Provider: p}
	for _, x := range accts {
		x.ta.Link = cmp.Or(x.local, x.qLink)
		if len(x.wins) > 0 {
			x.ta.Quota = x.quota(p, m.now)
			if mw := knownMain(x.ta.Quota); mw != nil {
				if s, ok := x.wins[mw.Name].w.Start(); ok {
					x.start, x.main = s, mw.Name
				}
			}
		}
		sort.Strings(x.ta.Devices)
		sortPerDevice(x.ta.PerDevice)
		x.ta.LinkedUsage = linkedList(m.linked[state.Key(p, x.ta.Label)])
	}
	names(p, accts, m.aliases)
	users(p, accts, m.devs)
	for _, x := range accts {
		tp.Accounts = append(tp.Accounts, x.ta)
	}
	sortTeamAccounts(tp.Accounts)
	return tp, true
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
	versions := []string{checked}
	for _, d := range devs {
		versions = append(versions, d.dev.CollectorVersion)
	}
	return selfupdate.Newest(versions...)
}

// active makes at the team account's newest activity when it is newer.
func (x *teamAccount) active(at *time.Time) {
	if at != nil && (x.ta.LastActiveAt == nil || at.After(*x.ta.LastActiveAt)) {
		x.ta.LastActiveAt = timeOf(at)
	}
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
	var q *Quota
	q, x.ta.State = quotaOf(Quota{Device: newest.dev, From: newest.from}, provider, rs, newest.at, now)
	return q
}

// sortTeamAccounts puts the worst state first, ties to the one with less
// left, then by label.
func sortTeamAccounts(as []TeamAccount) {
	slices.SortStableFunc(as, func(a, b TeamAccount) int {
		return cmp.Or(cmp.Compare(stateRank(a.State), stateRank(b.State)), cmp.Compare(left(a.Quota), left(b.Quota)), cmp.Compare(a.Label, b.Label))
	})
}

// left is what is left of a quota's main window, in percent; 101 with none,
// so an account with no reading sorts after one with a reading.
func left(q *Quota) float64 {
	m := knownMain(q)
	if m == nil {
		return 101
	}
	return max(100-m.Percent, 0)
}

// sortPerDevice puts the device that used the account most first.
func sortPerDevice(ds []DeviceUsage) {
	slices.SortFunc(ds, func(a, b DeviceUsage) int {
		return cmp.Or(cmp.Compare(b.Tokens.Total(), a.Tokens.Total()), cmp.Compare(a.Device, b.Device), cmp.Compare(a.DeviceID, b.DeviceID))
	})
}
