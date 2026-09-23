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
	Label           string          `json:"label"`
	Current         bool            `json:"current"`
	Plan            *string         `json:"plan"`
	HeadlinePercent *float64        `json:"headline_percent"`
	Level           string          `json:"level"`
	Quota           *Quota          `json:"quota"`
	Sessions        int             `json:"sessions"`
	Tokens          snapshot.Tokens `json:"tokens"`
	LastActiveAt    *time.Time      `json:"last_active_at"`
	Projects        []Project       `json:"projects"`
}

type Quota struct {
	ObservedAt time.Time `json:"observed_at"`
	AgeSeconds int64     `json:"age_seconds"`
	Stale      bool      `json:"stale"`
	Source     string    `json:"source,omitempty"`
	Device     string    `json:"device,omitempty"`
	Windows    []Window  `json:"windows"`
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
	HeadlinePercent *float64        `json:"headline_percent"`
	Level           string          `json:"level"`
	Quota           *Quota          `json:"quota"`
	Sessions        int             `json:"sessions"`
	Tokens          snapshot.Tokens `json:"tokens"`
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
				Plan:         strPtr(a.Plan),
				Level:        "unknown",
				Sessions:     a.Sessions,
				Tokens:       a.Tokens,
				LastActiveAt: timePtr(a.LastActive),
				Projects:     []Project{},
			}
			if a.Quota != nil {
				acct.Quota = quotaView(a.Quota.At, a.Quota.Source, "", a.Quota.Windows, now, in.Samples, p, a.Label)
				acct.HeadlinePercent, acct.Level = headline(a.Quota.Windows, now)
			}
			for _, pr := range a.Projects {
				acct.Projects = append(acct.Projects, Project(pr))
			}
			pv.Accounts = append(pv.Accounts, acct)
		}
		r.Providers = append(r.Providers, pv)
	}
	r.Team = buildTeam(in, now)
	return r
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

func buildTeam(in Input, now time.Time) Team {
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

	type acc struct {
		ta    TeamAccount
		qAt   time.Time
		qWins []snapshot.Window
		qDev  string
	}
	byProv := map[string]map[string]*acc{}
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
		for _, a := range d.Accounts {
			if byProv[a.Provider] == nil {
				byProv[a.Provider] = map[string]*acc{}
			}
			l := open(a.Label)
			x := byProv[a.Provider][l]
			if x == nil {
				x = &acc{ta: TeamAccount{Label: l, Devices: []string{}, Level: "unknown"}}
				byProv[a.Provider][l] = x
			}
			x.ta.Devices = append(x.ta.Devices, devName)
			x.ta.Sessions += a.Sessions
			x.ta.Tokens = x.ta.Tokens.Add(a.Tokens)
			if a.QuotaAt != nil && len(a.Windows) > 0 && a.QuotaAt.After(x.qAt) {
				x.qAt, x.qWins, x.qDev = *a.QuotaAt, a.Windows, devName
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
			if x.qWins != nil {
				samples := in.Samples
				x.ta.Quota = quotaView(x.qAt, "", x.qDev, x.qWins, now, samples, p, x.ta.Label)
				x.ta.HeadlinePercent, x.ta.Level = headline(x.qWins, now)
			}
			sort.Strings(x.ta.Devices)
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

// staged is the installed release while it still waits for the next run.
// The state keeps the tag after that run starts, and it is not news then.
func staged(installed, running string) string {
	if !selfupdate.Newer(installed, running) {
		return ""
	}
	return installed
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
