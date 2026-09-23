// Package view turns the collector's state into the two views: versioned JSON
// for an agent and console text for a person.
package view

import (
	"sort"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

// SchemaVersion is the agent JSON contract. A field may change meaning only
// with a new version.
const SchemaVersion = 2

// Thresholds follow PitStop's menu.
const (
	WarnPercent     = 75
	CriticalPercent = 90
	// StaleAfter marks a quota reading as old in both views.
	StaleAfter = 6 * time.Hour
)

type Report struct {
	SchemaVersion int        `json:"schema_version"`
	GeneratedAt   time.Time  `json:"generated_at"`
	Collector     Collector  `json:"collector"`
	Providers     []Provider `json:"providers"`
	Team          Team       `json:"team"`
}

type Collector struct {
	Version       string     `json:"version"`
	Device        string     `json:"device"`
	DeviceLabel   string     `json:"device_label"`
	OSUser        string     `json:"os_user"`
	Team          string     `json:"team"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     *string    `json:"last_error"`
	LastErrorAt   *time.Time `json:"last_error_at"`
	Relay         Relay      `json:"relay"`
	Schedule      Schedule   `json:"schedule"`
	Update        Update     `json:"update"`
}

type Relay struct {
	URL        *string    `json:"url"`
	LastPushAt *time.Time `json:"last_push_at"`
	LastPullAt *time.Time `json:"last_pull_at"`
	Pending    bool       `json:"pending"`
	LastError  *string    `json:"last_error"`
}

type Schedule struct {
	Registered bool    `json:"registered"`
	Error      *string `json:"error"`
}

type Update struct {
	CheckedAt *time.Time `json:"checked_at"`
	Latest    *string    `json:"latest"`
	Staged    *string    `json:"staged"`
	Error     *string    `json:"error"`
}

type Provider struct {
	Provider string    `json:"provider"`
	Status   string    `json:"status"`
	Error    *string   `json:"error"`
	Homes    []string  `json:"homes"`
	Accounts []Account `json:"accounts"`
}

type Account struct {
	Label   string `json:"label"`
	Current bool   `json:"current"`
	// Home is the harness home the account is logged in to now.
	Home            string          `json:"home,omitempty"`
	Plan            *string         `json:"plan"`
	HeadlinePercent *float64        `json:"headline_percent"`
	Level           string          `json:"level"`
	Quota           *Quota          `json:"quota"`
	Link            *Link           `json:"link"`
	Sessions        int             `json:"sessions"`
	Tokens          snapshot.Tokens `json:"tokens"`
	LinkedUsage     []LinkedUsage   `json:"linked_usage"`
	LastActiveAt    *time.Time      `json:"last_active_at"`
	Projects        []Project       `json:"projects"`
}

// Link names the account of another provider an account is assumed to bill
// through. On a team account the label is empty when the reading it shares
// matched no account, or several, on the device that sent it.
type Link struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

// LinkedUsage is what an account of another provider spent through this one.
// Those tokens are that account's, never added to this one's. A team entry
// also names that account and the devices it ran on.
type LinkedUsage struct {
	Provider string          `json:"provider"`
	Label    string          `json:"label,omitempty"`
	Devices  []string        `json:"devices,omitempty"`
	Sessions int             `json:"sessions"`
	Tokens   snapshot.Tokens `json:"tokens"`
}

type Quota struct {
	ObservedAt time.Time `json:"observed_at"`
	AgeSeconds int64     `json:"age_seconds"`
	Stale      bool      `json:"stale"`
	Source     string    `json:"source,omitempty"`
	// From names the provider whose linked account took the reading.
	From    string   `json:"from,omitempty"`
	Device  string   `json:"device,omitempty"`
	Windows []Window `json:"windows"`
}

type Window struct {
	Name     string     `json:"name"`
	Percent  float64    `json:"percent"`
	Level    string     `json:"level"`
	ResetsAt *time.Time `json:"resets_at"`
	Minutes  int        `json:"minutes,omitempty"`
	Pace     *Pace      `json:"pace"`
}

type Project struct {
	Path     string          `json:"path"`
	Sessions int             `json:"sessions"`
	Tokens   snapshot.Tokens `json:"tokens"`
}

type Team struct {
	PulledAt  *time.Time     `json:"pulled_at"`
	Devices   []TeamDevice   `json:"devices"`
	Providers []TeamProvider `json:"providers"`
}

type TeamDevice struct {
	Device           string     `json:"device"`
	Label            string     `json:"label"`
	OSUser           string     `json:"os_user"`
	This             bool       `json:"this_device"`
	CollectorVersion string     `json:"collector_version"`
	CollectedAt      time.Time  `json:"collected_at"`
	AgeSeconds       int64      `json:"age_seconds"`
	LastSuccessAt    *time.Time `json:"last_success_at"`
	LastError        *string    `json:"last_error"`
	Sources          []Source   `json:"sources"`
}

type Source struct {
	Provider string  `json:"provider"`
	Status   string  `json:"status"`
	Error    *string `json:"error"`
}

type TeamProvider struct {
	Provider string        `json:"provider"`
	Accounts []TeamAccount `json:"accounts"`
}

// TeamAccount adds tokens across devices. The quota is the newest reading any
// device has for the account; percentages are never added.
type TeamAccount struct {
	Label           string          `json:"label"`
	Devices         []string        `json:"devices"`
	Plan            *string         `json:"plan"`
	HeadlinePercent *float64        `json:"headline_percent"`
	Level           string          `json:"level"`
	Quota           *Quota          `json:"quota"`
	Link            *Link           `json:"link"`
	Sessions        int             `json:"sessions"`
	Tokens          snapshot.Tokens `json:"tokens"`
	PerDevice       []DeviceUsage   `json:"per_device"`
	LinkedUsage     []LinkedUsage   `json:"linked_usage"`
}

// DeviceUsage is one device's share of a team account.
type DeviceUsage struct {
	Device       string          `json:"device"`
	DeviceID     string          `json:"device_id"`
	Current      bool            `json:"current"`
	Sessions     int             `json:"sessions"`
	Tokens       snapshot.Tokens `json:"tokens"`
	LastActiveAt *time.Time      `json:"last_active_at"`
}

// Input is everything a report is built from.
type Input struct {
	Version  string
	RelayURL string
	Config   state.Config
	State    *state.State
	Key      *team.Key
	Doc      snapshot.Doc
	Team     collect.TeamCache
	Samples  []state.Sample
	Hostname string
	OSUser   string
	Now      time.Time
}

// Build assembles the report.
func Build(in Input) Report {
	now := in.Now.UTC()
	st := in.State
	r := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   now,
		Collector: Collector{
			Version:       in.Version,
			Device:        in.Config.Device,
			DeviceLabel:   in.Hostname,
			OSUser:        in.OSUser,
			Team:          in.Key.Fingerprint(),
			LastRunAt:     timePtr(st.LastRunAt),
			LastSuccessAt: timePtr(st.LastSuccessAt),
			LastError:     strPtr(st.LastError),
			LastErrorAt:   timePtr(st.LastErrorAt),
			Relay: Relay{
				URL:        strPtr(in.RelayURL),
				LastPushAt: timePtr(st.Relay.LastPushAt),
				LastPullAt: timePtr(st.Relay.LastPullAt),
				Pending:    st.Relay.Pending,
				LastError:  strPtr(st.Relay.LastError),
			},
			Schedule: Schedule{Registered: st.Schedule.Registered, Error: strPtr(st.Schedule.Error)},
			Update: Update{
				CheckedAt: timePtr(st.Update.CheckedAt),
				Latest:    strPtr(st.Update.Latest),
				Staged:    strPtr(staged(st.Update.Installed, in.Version)),
				Error:     strPtr(st.Update.Error),
			},
		},
		Providers: []Provider{},
		Team:      Team{Devices: []TeamDevice{}, Providers: []TeamProvider{}},
	}

	totals := collect.Totals(st)
	for _, p := range collect.Providers {
		src := st.Sources[p]
		pv := Provider{Provider: p, Status: orDefault(src.Status, "skipped"), Error: strPtr(src.Error), Homes: src.Homes, Accounts: []Account{}}
		if pv.Homes == nil {
			pv.Homes = []string{}
		}
		for _, a := range totals {
			if a.Provider != p {
				continue
			}
			acct := Account{
				Label:        a.Label,
				Current:      a.Current,
				Home:         currentHome(st, p, pv.Homes, a.Label),
				Plan:         strPtr(a.Plan),
				Level:        "unknown",
				Link:         linkView(a.Link),
				Sessions:     a.Sessions,
				Tokens:       a.Tokens,
				LinkedUsage:  []LinkedUsage{},
				LastActiveAt: timePtr(a.LastActive),
				Projects:     []Project{},
			}
			if a.Quota != nil {
				// A borrowed reading paces like the account that took it.
				from, label := p, a.Label
				if a.QuotaFrom != "" && a.Link != nil {
					from, label = a.Link.Provider, a.Link.Label
				}
				acct.Quota = quotaView(a.Quota.At, a.Quota.Source, "", a.Quota.Windows, now, in.Samples, from, label)
				acct.Quota.From = a.QuotaFrom
				acct.HeadlinePercent, acct.Level = headline(a.Quota.Windows, now)
			}
			if a.LinkedSessions > 0 {
				// Only Hermes bills through another harness's login.
				acct.LinkedUsage = append(acct.LinkedUsage, LinkedUsage{Provider: "hermes", Sessions: a.LinkedSessions, Tokens: a.Linked})
			}
			for _, pr := range a.Projects {
				acct.Projects = append(acct.Projects, Project(pr))
			}
			pv.Accounts = append(pv.Accounts, acct)
		}
		r.Providers = append(r.Providers, pv)
	}
	r.Team = buildTeam(in, totals, now)
	return r
}

// currentHome is the first of the provider's homes whose login is label.
func currentHome(st *state.State, provider string, homes []string, label string) string {
	for _, h := range homes {
		if st.Current[state.Key(provider, h)] == label {
			return h
		}
	}
	return ""
}

func linkView(l *state.Link) *Link {
	if l == nil {
		return nil
	}
	return &Link{Provider: l.Provider, Label: l.Label}
}

func quotaView(at time.Time, source, device string, ws []snapshot.Window, now time.Time, samples []state.Sample, provider, label string) *Quota {
	q := &Quota{
		ObservedAt: at,
		AgeSeconds: int64(now.Sub(at).Seconds()),
		Stale:      now.Sub(at) > StaleAfter,
		Source:     source,
		Device:     device,
		Windows:    []Window{},
	}
	for _, w := range ws {
		win := Window{Name: w.Name, Percent: w.Percent, Level: level(w.Percent), ResetsAt: w.ResetsAt, Minutes: w.Minutes}
		if hasReset(w, now) {
			// The percent is from a period that has ended. How full the
			// new one is, nobody has read.
			win.Level = "unknown"
		} else if samples != nil {
			win.Pace = windowPace(samples, provider, label, w, at)
		}
		q.Windows = append(q.Windows, win)
	}
	return q
}

// hasReset reports whether a window's reset time has passed since it was read.
func hasReset(w snapshot.Window, now time.Time) bool {
	return w.ResetsAt != nil && !w.ResetsAt.After(now)
}

// headline is the fullest window that has not reset since the reading. With
// none left there is no headline, rather than a percent that no longer holds.
func headline(ws []snapshot.Window, now time.Time) (*float64, string) {
	var max *float64
	for _, w := range ws {
		if hasReset(w, now) {
			continue
		}
		if max == nil || w.Percent > *max {
			p := w.Percent
			max = &p
		}
	}
	if max == nil {
		return nil, "unknown"
	}
	return max, level(*max)
}

func level(p float64) string {
	switch {
	case p >= CriticalPercent:
		return "critical"
	case p >= WarnPercent:
		return "warning"
	default:
		return "ok"
	}
}

// teamAccount is one team account while the device docs are merged.
type teamAccount struct {
	ta     TeamAccount
	qAt    time.Time
	qWins  []snapshot.Window
	qDev   string
	qFrom  string
	qLink  *Link // the link on the device that sent the chosen reading
	local  *Link // this device's own link, which wins
	planAt time.Time
}

func buildTeam(in Input, totals []collect.AccountTotals, now time.Time) Team {
	t := Team{Devices: []TeamDevice{}, Providers: []TeamProvider{}}
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
		return v
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
		t.Devices = append(t.Devices, dev)

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
				x = &teamAccount{ta: TeamAccount{Label: l, Devices: []string{}, Level: "unknown", PerDevice: []DeviceUsage{}, LinkedUsage: []LinkedUsage{}}}
				byProv[a.Provider][l] = x
			}
			x.ta.Devices = append(x.ta.Devices, devName)
			x.ta.Sessions += a.Sessions
			x.ta.Tokens = x.ta.Tokens.Add(a.Tokens)
			x.ta.PerDevice = append(x.ta.PerDevice, DeviceUsage{
				Device: devName, DeviceID: d.Device, Current: a.Current,
				Sessions: a.Sessions, Tokens: a.Tokens, LastActiveAt: timeOf(a.LastActiveAt),
			})
			if a.Plan != "" && (x.ta.Plan == nil || d.CollectedAt.After(x.planAt)) {
				x.ta.Plan, x.planAt = strPtr(a.Plan), d.CollectedAt
			}
			link := wireLink(d.Accounts, labels, i)
			if dev.This {
				link = localLinks[state.Key(a.Provider, l)]
				x.local = link
			}
			if a.QuotaAt != nil && len(a.Windows) > 0 && a.QuotaAt.After(x.qAt) {
				x.qAt, x.qWins, x.qDev, x.qFrom, x.qLink = *a.QuotaAt, a.Windows, devName, a.QuotaFrom, link
			}
			for _, u := range a.Linked {
				addLinked(linked, a.Provider, l, u.Provider, open(u.Label), devName, u)
			}
		}
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
			if x.qWins != nil {
				from, label := p, x.ta.Label
				if x.qFrom != "" && x.ta.Link != nil && x.ta.Link.Label != "" {
					from, label = x.ta.Link.Provider, x.ta.Link.Label
				}
				x.ta.Quota = quotaView(x.qAt, "", x.qDev, x.qWins, now, in.Samples, from, label)
				x.ta.Quota.From = x.qFrom
				x.ta.HeadlinePercent, x.ta.Level = headline(x.qWins, now)
			}
			sort.Strings(x.ta.Devices)
			sortPerDevice(x.ta.PerDevice)
			x.ta.LinkedUsage = linkedList(linked[state.Key(p, x.ta.Label)])
			tp.Accounts = append(tp.Accounts, x.ta)
		}
		sort.Slice(tp.Accounts, func(i, j int) bool {
			a, b := tp.Accounts[i], tp.Accounts[j]
			if a.Tokens.Total() != b.Tokens.Total() {
				return a.Tokens.Total() > b.Tokens.Total()
			}
			return a.Label < b.Label
		})
		t.Providers = append(t.Providers, tp)
	}
	return t
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

// staged is the installed release while it still waits for the next run.
// The state keeps the tag after that run starts, and it is not news then.
func staged(installed, running string) string {
	if !selfupdate.Newer(installed, running) {
		return ""
	}
	return installed
}

func timeOf(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	return timePtr(*t)
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
