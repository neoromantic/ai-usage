package view

import (
	"cmp"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/meter"
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
	// holders are the devices whose snapshots have the account.
	holders []*device
	// cycles are the periods of its windows each snapshot carries.
	cycles []snapshot.Cycle
}

type winReading struct {
	w     snapshot.Window
	at    time.Time
	dev   string
	devID string
	from  string
}

// devAccount is one account on one device doc, or the part of a Hermes
// account that went through one login. Its recent windows have their tokens
// and their burn.
type devAccount struct {
	provider, label string
	usage           Usage
	days            []int64
	// hours are its input plus output tokens by UTC hour, and burnHours its
	// weighted tokens, as the device sent them.
	hours, burnHours []int64
	recent           []snapshot.Recent
	// burn is its weighted tokens, by the UTC days burnDays, estimated from
	// its input and output where estimated says so.
	burn      Usage
	burnDays  []int64
	estimated bool
	// last is the account's newest activity on that device.
	last *time.Time
	// link is the account a Hermes account bills through on that device.
	link *Link
}

// device is one device doc while the team is merged.
type device struct {
	dev         *TeamDevice
	name        string // as the report calls it, DeviceName
	collectedAt time.Time
	shift       int
	accts       []devAccount
	// noQuota is why a release before collect.ExtSince read no Claude
	// quota, as merger.noReading says it.
	noQuota string
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
	// localToday says the team's today counts from midnight, where
	// midnight is this device's, as Team.TodayLocal has it.
	localToday bool
	midnight   time.Time
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
	t.TodayLocal = localToday(docs[1:], in.State.Relay, now)
	m.localToday, m.midnight = t.TodayLocal, localMidnight(now, m.loc())
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
	m.replaced()
	for _, dv := range m.devs {
		dv.dev.Gap = gapOf(m.in.Team.Seen[dv.dev.Device], m.in.Team.Reads, dv.collectedAt, now, dv.dev.This)
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
	m.estimate(&t)
	m.cyclesGate(&t)
	return t
}

// localToday says the team's today can count from local midnight: none of
// the other devices, whose docs are docs, runs a release before
// collect.HoursSince, and the relay has not refused the hours not long
// ago, so each device sends its hours, or will at its next run. Otherwise
// today is the UTC day, as before.
func localToday(docs []snapshot.Doc, r state.Relay, now time.Time) bool {
	if now.Before(collect.NoHoursUntil(r)) {
		return false
	}
	return !slices.ContainsFunc(docs, func(d snapshot.Doc) bool { return collect.BeforeHours(d.CollectorVersion) })
}

// periods sums an account's days on the device dv into the report's
// periods. While the team's today counts from local midnight, it is the
// hours since then, where dv sent its hours, and its UTC day otherwise.
func (m *merger) periods(days, hours []int64, dv *device) Usage {
	u := usageOf(days, dv.shift)
	if m.localToday && len(hours) > 0 {
		u.Today = hoursToday(hours, dv.collectedAt, m.now, m.midnight)
	}
	return u
}

// raw is a sealed string as it was sealed, line breaks and all.
func (m *merger) raw(s string) string {
	v, err := m.in.Key.Open(s)
	if err != nil {
		return unreadable
	}
	return v
}

func (m *merger) open(s string) string { return snapshot.Printable(m.raw(s)) }

// addDoc merges one device doc: the device, its aliases and its accounts.
func (m *merger) addDoc(d snapshot.Doc) {
	dev, noQuota := m.teamDevice(d)
	dv := &device{dev: dev, name: dev.Name, collectedAt: d.CollectedAt, shift: dayShift(d.CollectedAt, m.now), noQuota: noQuota}
	m.devs = append(m.devs, dv)
	m.addAliases(d)
	m.addAccounts(dv, d)
}

// teamDevice is a device doc's device, before its usage and release, and,
// on a release before collect.ExtSince, why its Claude source read no quota,
// as a subscription's reason for having no reading says it.
func (m *merger) teamDevice(d snapshot.Doc) (dev *TeamDevice, noQuota string) {
	label, user := m.open(d.DeviceLabel), m.open(d.OSUser)
	this := d.Device == m.in.Doc.Device
	lastErr, updateErr := m.lastErrors(d, this)
	dev = &TeamDevice{
		Device:           d.Device,
		Label:            label,
		OSUser:           user,
		Name:             DeviceName(label, user),
		This:             this,
		CollectorVersion: d.CollectorVersion,
		CollectedAt:      d.CollectedAt,
		AgeSeconds:       int64(m.now.Sub(d.CollectedAt).Seconds()),
		LastSuccessAt:    timePtr(d.LastSuccessAt),
		LastError:        lastErr,
		UpdateError:      updateErr,
		LastErrorAt:      lastErrorAt(lastErr, d.LastErrorAt),
		Sources:          []Source{},
		Silent:           m.now.Sub(d.CollectedAt) > collect.SilentAfter,
		Limitations:      limitations(d.CollectorVersion),
	}
	for _, s := range d.Sources {
		src := Source{Provider: s.Provider, Status: s.Status, Error: strPtr(m.open(s.Error))}
		if s.Provider == "claude" && src.Error != nil && src.Status != "ok" {
			note, reason, rest := legacyNoQuota(d.CollectorVersion, dev.Name, *src.Error)
			switch {
			case note == "":
			case rest == "":
				src.Status, src.Error = "ok", strPtr(note)
			default:
				// It failed for something else too: that stays its error.
				src.Error = strPtr(rest)
			}
			noQuota = cmp.Or(noQuota, reason)
		}
		dev.Sources = append(dev.Sources, src)
	}
	dev.Error = deviceError(*dev)
	return dev, noQuota
}

// claudeTooOld is how a release before collect.ExtSince says the Claude
// Code it ran cannot cache the usage.
var claudeTooOld = regexp.MustCompile(`Claude Code (\S+) does not cache the usage; update it to (\S+) or later`)

// legacyNoQuota splits msg, a source's or a run's error on a device named
// label that runs version, a release before collect.ExtSince, into the
// problem of reading no Claude quota and the rest. Such a release made that
// problem an error, joined with "; " to any other; this one keeps the source
// ok for it, and shows it as the subscription's reason for having no
// reading. note is the problem as a source's error says it, action first,
// and reason as that reason says it, with the device; both are "" when msg
// holds no such problem, or version is no such release. rest is what else
// msg holds, joined again.
func legacyNoQuota(version, label, msg string) (note, reason, rest string) {
	if !collect.BeforeExt(version) {
		return "", "", msg
	}
	parts := strings.Split(msg, "; ")
	var others []string
	for i := 0; i < len(parts); i++ {
		// The message of a Claude Code too old spans two parts.
		if i+1 < len(parts) {
			if m := claudeTooOld.FindStringSubmatch(parts[i] + "; " + parts[i+1]); m != nil {
				if note == "" {
					note = "update Claude Code to " + m[2] + " or newer (it runs " + m[1] + ")"
					reason = label + " runs Claude Code " + m[1] + ": update Claude Code to " + m[2] + " or newer"
				}
				i++
				continue
			}
		}
		if j := strings.Index(parts[i], "claude /usage: nonessential traffic is off"); j >= 0 {
			if note == "" {
				note = parts[i][j:]
				reason = note + " on " + label
			}
			continue
		}
		others = append(others, parts[i])
	}
	if note == "" {
		return "", "", msg
	}
	return note, reason, strings.Join(others, "; ")
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

// lastErrorAt is when a device's last error, lastErr, happened, when its
// snapshot says.
func lastErrorAt(lastErr *string, at *time.Time) *time.Time {
	if lastErr == nil || at == nil {
		return nil
	}
	return timePtr(*at)
}

// errorAt is when what fails on a device happened: when its last run's
// error did, when that is its error and its collector says.
func errorAt(d TeamDevice) *time.Time {
	if d.Error != nil && d.LastError != nil && *d.Error == *d.LastError {
		return d.LastErrorAt
	}
	return nil
}

// account is the team account of provider and label, made when it is new.
// Devices that spell an email in other cases hold one account, which keeps
// the spelling it was first seen in.
func (m *merger) account(provider, label string) *teamAccount {
	if m.byProv[provider] == nil {
		m.byProv[provider] = map[string]*teamAccount{}
	}
	k := snapshot.LabelKey(label)
	x := m.byProv[provider][k]
	if x == nil {
		x = &teamAccount{
			provider: provider,
			ta: TeamAccount{
				Label: label, Subscription: subscription(provider),
				Devices: []string{}, State: StateUnknown, PerDevice: []DeviceUsage{},
			},
			wins: map[string]*winReading{},
		}
		m.byProv[provider][k] = x
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
			label := m.open(u.Label)
			k := state.Key(u.Provider, label)
			through[k] = append(through[k], login{Link{a.Provider, labels[i]}, u.Tokens.InOut(), meter.TokensBurn(u.Tokens, u.Provider, label)})
		}
	}
	for i, a := range d.Accounts {
		l := labels[i]
		x := m.account(a.Provider, l)
		usage := m.periods(a.Days, a.Hours, dv)
		burnDays, recent, estimated := m.accountBurn(d, a, l)
		burn := m.periods(burnDays, a.BurnHours, dv)
		dev.Usage = dev.Usage.add(usage)
		dev.Burn = dev.Burn.add(burn)
		dev.BurnEstimated = dev.BurnEstimated || estimated
		x.ta.Devices = append(x.ta.Devices, dv.name)
		x.holders = append(x.holders, dv)
		x.ta.Sessions += a.Sessions
		x.ta.Tokens = x.ta.Tokens.Add(a.Tokens)
		x.ta.Usage = x.ta.Usage.add(usage)
		x.ta.Burn = x.ta.Burn.add(burn)
		x.ta.BurnEstimated = x.ta.BurnEstimated || estimated
		if dev.This && a.Current {
			x.ta.Current = true
		}
		x.ta.PerDevice = append(x.ta.PerDevice, DeviceUsage{
			Device: dv.name, DeviceID: d.Device, Current: a.Current,
			Sessions: a.Sessions, Tokens: a.Tokens, Usage: usage, Burn: burn, LastActiveAt: timeOf(a.LastActiveAt),
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
		x.addReading(a, dv, link)
		x.cycles = append(x.cycles, a.Cycles...)
		for _, u := range a.Linked {
			addLinked(m.linked, a.Provider, l, u.Provider, m.open(u.Label), dv.name, u)
		}
		da := devAccount{provider: a.Provider, label: l, usage: usage, days: a.Days, hours: a.Hours, recent: recent,
			burn: burn, burnDays: burnDays, burnHours: a.BurnHours, estimated: estimated, last: a.LastActiveAt, link: link}
		dv.accts = append(dv.accts, da.split(through[state.Key(a.Provider, l)], func(days, hours []int64) Usage { return m.periods(days, hours, dv) })...)
	}
}

// addReading merges a's reading, sent by device dv, into the newest
// reading of each window, and keeps link when a's reading is the newest.
func (x *teamAccount) addReading(a snapshot.Account, dv *device, link *Link) {
	if a.QuotaAt == nil {
		return
	}
	for _, w := range a.Windows {
		cur := x.wins[w.Name]
		if cur == nil {
			x.order = append(x.order, w.Name)
		}
		if cur == nil || a.QuotaAt.After(cur.at) {
			x.wins[w.Name] = &winReading{w: w, at: *a.QuotaAt, dev: dv.name, devID: dv.dev.Device, from: a.QuotaFrom}
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
			x.ta.Quota = x.quota(p, paceOf(m.in, p, x.ta.Label), m.now)
			if mw := knownMain(x.ta.Quota); mw != nil {
				if s, ok := x.wins[mw.Name].w.Start(); ok {
					x.start, x.main = s, mw.Name
				}
			}
		}
		sort.Strings(x.ta.Devices)
		sortPerDevice(x.ta.PerDevice)
		x.ta.LinkedUsage = linkedList(m.linked[linkKey(p, x.ta.Label)])
		if x.ta.Subscription && knownMain(x.ta.Quota) == nil {
			x.ta.NoReading = m.noReading(x)
		}
		x.ta.Outside = outside(m.in.Team.History[collect.HistoryKey(p, x.ta.Label)], m.now)
		x.ta.Utilization = []Utilization{}
		if x.ta.Subscription {
			x.ta.Utilization = utilization(x.cycles, x.ta.Quota, m.now)
		}
	}
	names(p, accts, m.aliases)
	users(accts, m.byProv, m.devs)
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
		// A release before collect.ExtSince failed its run for no Claude
		// quota too: that is no error now.
		if note, _, rest := legacyNoQuota(d.CollectorVersion, d.Name, *d.LastError); note != "" {
			if rest == "" {
				return nil
			}
			return strPtr(rest)
		}
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
// in the order the windows first came, at the pace its readings pts show.
func (x *teamAccount) quota(provider string, pts []pacePoint, now time.Time) *Quota {
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
	q, x.ta.State = quotaOf(Quota{Device: newest.dev, DeviceID: newest.devID, From: newest.from}, provider, rs, pts, newest.at, now)
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

// sortPerDevice puts the devices that used the account in byBurn's order
// for 7d.
func sortPerDevice(ds []DeviceUsage) {
	by := byBurn(Week, func(d DeviceUsage) Usage { return d.Burn }, func(d DeviceUsage) string { return d.Device })
	slices.SortStableFunc(ds, func(a, b DeviceUsage) int { return cmp.Or(by(a, b), cmp.Compare(a.DeviceID, b.DeviceID)) })
}

// What a device's release cannot do, in TeamDevice.Limitations.
const (
	// LimitNoClaudeQuota is a release before v0.2.3, which reads no Claude
	// quota.
	LimitNoClaudeQuota = "no_claude_quota"
	// LimitNoUpdateErrors is a release before v0.2.5, which does not say
	// why its update failed.
	LimitNoUpdateErrors = "no_update_errors"
)

// limitations are what a collector of version cannot do. A development
// build, whose version is not a release, can do all of it.
func limitations(version string) []string {
	out := []string{}
	if selfupdate.Newer("v0.2.3", version) {
		out = append(out, LimitNoClaudeQuota)
	}
	if selfupdate.Newer("v0.2.5", version) {
		out = append(out, LimitNoUpdateErrors)
	}
	return out
}

// replaced marks each silent device that another reporting device took the
// place of: one with the same name, as labels compare, and OS user, that reported
// after it. A reinstall gives a device a new id, and the old one's record
// stays on the relay until it expires. Of several, the newest is named.
func (m *merger) replaced() {
	for _, old := range m.devs {
		o := old.dev
		if !o.Silent || o.Label == unreadable {
			continue
		}
		var by *TeamDevice
		for _, nw := range m.devs {
			n := nw.dev
			if n.Silent || n.Device == o.Device || n.OSUser != o.OSUser || snapshot.LabelKey(n.Label) != snapshot.LabelKey(o.Label) {
				continue
			}
			if n.CollectedAt.After(o.CollectedAt) && (by == nil || n.CollectedAt.After(by.CollectedAt)) {
				by = n
			}
		}
		if by != nil {
			o.ReplacedBy = strPtr(by.Label)
		}
	}
}

// gapKept is how far back a device's gaps count, as far as the team cache
// keeps when devices reported, and gapMin the shortest gap that counts: more
// than the runs of 15 minutes and the reads of 20 leave between reports.
const (
	gapKept = 48 * time.Hour
	gapMin  = time.Hour
)

// gapOf is the longest time within gapKept of now that a device did not
// report, over the times the team's reads found it reporting, seen, and its
// snapshot collected at collected, until now, when that is over gapMin.
// With no times seen there is nothing to go by, and it is nil. Only what
// this device's reads, at reads, saw of a gap counts: a read between two
// reports found the device still at the first, while a gap with no read in
// it, as when this laptop slept, may have had reports this device never
// read. This device itself, own, knows its gaps whole.
func gapOf(seen, reads []time.Time, collected, now time.Time, own bool) *Gap {
	cut := now.Add(-gapKept)
	var ts []time.Time
	for _, t := range append(slices.Clone(seen), collected) {
		if !t.Before(cut) && !t.After(now) {
			ts = append(ts, t)
		}
	}
	if len(seen) == 0 || len(ts) == 0 {
		return nil
	}
	slices.SortFunc(ts, time.Time.Compare)
	var best time.Duration
	var at time.Time
	for i, t := range ts {
		next := now
		if i+1 < len(ts) {
			next = ts[i+1]
		}
		if !own {
			// Up to the last read that still found the device at t.
			until := t
			for _, r := range reads {
				if r.After(until) && (r.Before(next) || i+1 == len(ts) && !r.After(next)) {
					until = r
				}
			}
			next = until
		}
		if d := next.Sub(t); d > best {
			best, at = d, t
		}
	}
	if best <= gapMin {
		return nil
	}
	return &Gap{Seconds: int64(best.Seconds()), At: at.UTC()}
}

// noReading is why subscription x has no reading of its main window, with
// what to do, when that is known: the devices that hold a Claude account all
// run a release that reads no Claude quota, or one of them could not read
// it, as its source says without failing. Of several, this device, then
// the one first by name, is named. The unknown account has the reason
// unknownReason gives, or none.
func (m *merger) noReading(x *teamAccount) *string {
	hs := slices.Clone(x.holders)
	slices.SortStableFunc(hs, func(a, b *device) int {
		if a.dev.This != b.dev.This {
			if a.dev.This {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.dev.Name, b.dev.Name)
	})
	hs = slices.CompactFunc(hs, func(a, b *device) bool { return a == b })
	if len(hs) == 0 {
		return nil
	}
	if x.ta.Label == collect.UnknownAccount {
		return unknownReason(x.provider, hs)
	}
	if x.provider == "claude" && !slices.ContainsFunc(hs, func(dv *device) bool {
		return !slices.Contains(dv.dev.Limitations, LimitNoClaudeQuota)
	}) {
		d := hs[0].dev
		return strPtr(d.Name + " runs " + d.CollectorVersion + ": reinstall with install.sh")
	}
	for _, dv := range hs {
		if x.provider == "claude" && dv.noQuota != "" {
			return strPtr(dv.noQuota)
		}
		for _, s := range dv.dev.Sources {
			if s.Provider == x.provider && s.Status == "ok" && s.Error != nil {
				msg := strings.TrimPrefix(*s.Error, s.Provider+": ")
				return strPtr(msg + " on " + dv.dev.Name)
			}
		}
	}
	return nil
}

// unknownReason is why tokens of harness p went to no login, and where,
// with what to do, as the first of the devices hs to say so does: its
// source failed for a home whose logs have tokens, as the harness answered
// that nobody is logged in to it. Without that it is nil: a device that
// logged in since says nothing of the tokens it had before.
func unknownReason(p string, hs []*device) *string {
	for _, dv := range hs {
		for _, s := range dv.dev.Sources {
			if s.Provider != p || s.Error == nil || s.Status != "error" && s.Status != "partial" {
				continue
			}
			// A device with several homes names the home first.
			for _, part := range strings.Split(*s.Error, "; ") {
				home, ok := strings.CutSuffix(part, p+": not logged in")
				if !ok {
					continue
				}
				where := dv.dev.Name
				if home = strings.TrimSuffix(home, ": "); home != "" {
					where += " (" + home + ")"
				}
				return strPtr("not logged in on " + where + ": " + loginAction(p))
			}
		}
	}
	return nil
}

// loginAction is how one logs in to harness p.
func loginAction(p string) string {
	switch p {
	case "claude":
		return "run claude and log in"
	case "codex":
		return "run codex login"
	}
	return "log in to " + p
}

// Outside is found over outsideSpan of an account's readings: a window that
// rose outsideRise points or more while the team burned under a hundredth of
// what a point of it usually takes.
const (
	outsideSpan = 24 * time.Hour
	outsideRise = 5
)

// outside is the window of an account whose readings, ps, the team's reads
// found filling while the team burned next to nothing on it: over the last
// outsideSpan of the window's current period, it rose outsideRise points or
// more while the team's burn on it grew by under a hundredth of the median
// burn a point took between two readings where both grew. With no such two
// readings, the team's burn must not have grown at all. Of several windows,
// the one that rose most.
func outside(ps []collect.Point, now time.Time) *Outside {
	byWindow := map[string][]collect.Point{}
	var order []string
	for _, p := range ps {
		if byWindow[p.Window] == nil {
			order = append(order, p.Window)
		}
		byWindow[p.Window] = append(byWindow[p.Window], p)
	}
	var best *Outside
	for _, name := range order {
		w := slices.Clone(byWindow[name])
		slices.SortStableFunc(w, func(a, b collect.Point) int { return a.At.Compare(b.At) })
		last := w[len(w)-1]
		// The points of the window's current period, over the span.
		var cur []collect.Point
		for _, p := range w {
			if samePeriod(p, last) && !p.At.Before(now.Add(-outsideSpan)) {
				cur = append(cur, p)
			}
		}
		if len(cur) < 2 {
			continue
		}
		rise := last.Percent - cur[0].Percent
		if rise < outsideRise {
			continue
		}
		// What the team burned is what it added between readings: a total
		// that fell, as when a device that counted from its days starts
		// counting from its own reading, takes nothing back.
		var burned int64
		for i := 1; i < len(cur); i++ {
			burned += max(cur[i].TeamBurn-cur[i-1].TeamBurn, 0)
		}
		var perPoint []float64
		for i := 1; i < len(w); i++ {
			dp, db := w[i].Percent-w[i-1].Percent, w[i].TeamBurn-w[i-1].TeamBurn
			if samePeriod(w[i], w[i-1]) && dp > 0 && db > 0 {
				perPoint = append(perPoint, float64(db)/dp)
			}
		}
		switch {
		case len(perPoint) == 0 && burned > 0:
			continue
		case len(perPoint) > 0 && float64(burned)*100 >= median(perPoint):
			continue
		}
		if best == nil || rise > best.Points {
			best = &Outside{Points: rise, Since: cur[0].At.UTC()}
		}
	}
	return best
}

// samePeriod says two readings of a window are of one period of it: they
// reset at the same time, give or take a minute.
func samePeriod(a, b collect.Point) bool {
	if a.ResetsAt == nil || b.ResetsAt == nil {
		return a.ResetsAt == nil && b.ResetsAt == nil
	}
	return a.ResetsAt.Sub(*b.ResetsAt).Abs() <= time.Minute
}

// median is the middle of vs, or the mean of the two in the middle.
func median(vs []float64) float64 {
	s := slices.Clone(vs)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
