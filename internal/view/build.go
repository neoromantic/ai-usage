package view

import (
	"cmp"
	"sort"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

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
			Schedule: Schedule{Registered: st.Schedule.Registered, Foreground: st.Schedule.Registered && st.Schedule.Foreground, Error: strPtr(st.Schedule.Error)},
			Update: Update{
				CheckedAt: timePtr(st.Update.CheckedAt),
				Latest:    strPtr(st.Update.Latest),
				Staged:    strPtr(staged(st.Update.Installed, in.Version)),
				Error:     strPtr(st.Update.Error),
			},
		},
		Attention: []Attention{},
		Providers: []Provider{},
		Projects:  []Project{},
		Team:      Team{Devices: []TeamDevice{}, Providers: []TeamProvider{}, Matrix: Matrix{Columns: []Column{}, Rows: []Row{}}},
	}

	totals := collect.Totals(st)
	for _, p := range snapshot.Providers {
		src := st.Sources[p]
		pv := Provider{Provider: p, Status: cmp.Or(src.Status, "skipped"), Error: strPtr(src.Error), Homes: src.Homes, Accounts: []Account{}}
		if pv.Homes == nil {
			pv.Homes = []string{}
		}
		for _, a := range totals {
			if a.Provider != p {
				continue
			}
			days := collect.DaysOf(a.Hours, now)
			acct := Account{
				Label:        a.Label,
				Name:         a.Label,
				Current:      a.Current,
				Home:         currentHome(st, p, pv.Homes, a.Label),
				Plan:         strPtr(a.Plan),
				State:        StateUnknown,
				Link:         linkView(a.Link),
				Sessions:     a.Sessions,
				Tokens:       a.Tokens,
				Usage:        usageOf(days, 0),
				Days:         orEmpty(days),
				LinkedUsage:  []LinkedUsage{},
				LastActiveAt: timePtr(a.LastActive),
				Projects:     []Project{},
			}
			if a.Quota != nil {
				acct.Quota = quotaView(a.Quota.At, a.Quota.Source, "", now)
				acct.Quota.Windows, acct.State = readQuota(withUnread(p, readings(a.Quota.Windows, a.Quota.At)), now)
				acct.Quota.Stale = anyStale(acct.Quota.Windows)
				acct.Quota.From = a.QuotaFrom
			}
			if a.LinkedSessions > 0 {
				// Only Hermes bills through another harness's login.
				acct.LinkedUsage = append(acct.LinkedUsage, LinkedUsage{Provider: "hermes", Sessions: a.LinkedSessions, Tokens: a.Linked})
			}
			for _, pr := range a.Projects {
				acct.Projects = append(acct.Projects, projectView(pr, now))
			}
			pv.Accounts = append(pv.Accounts, acct)
		}
		r.Providers = append(r.Providers, pv)
	}
	for _, pr := range collect.Projects(st) {
		r.Projects = append(r.Projects, projectView(pr, now))
	}
	sortProjects(r.Projects, Week)
	r.Team = buildTeam(in, totals, now)
	for i := range r.Providers {
		for j := range r.Providers[i].Accounts {
			a := &r.Providers[i].Accounts[j]
			a.Name = teamName(r.Team, r.Providers[i].Provider, a.Label)
		}
	}
	r.Attention = attention(r.Team, r.Collector, now)
	return r
}

// teamName is an account's short name in the team, or its default name
// when the team view does not hold it.
func teamName(t Team, provider, label string) string {
	for _, p := range t.Providers {
		if p.Provider != provider {
			continue
		}
		for _, a := range p.Accounts {
			if a.Label == label {
				return a.Name
			}
		}
	}
	return ShortName(label)
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

// quotaView is a quota's reading, without its windows.
func quotaView(at time.Time, source, device string, now time.Time) *Quota {
	return &Quota{
		ObservedAt: at.UTC(),
		AgeSeconds: int64(now.Sub(at).Seconds()),
		Source:     source,
		Device:     device,
		Windows:    []Window{},
	}
}

func projectView(p collect.ProjectTotals, now time.Time) Project {
	return Project{
		Path:         p.Path,
		Sessions:     p.Sessions,
		Tokens:       p.Tokens,
		Usage:        usageOf(collect.DaysOf(p.Hours, now), 0),
		Providers:    p.Providers,
		LastActiveAt: timePtr(p.LastActive),
	}
}

// sortProjects puts the most tokens in the period first, then the most in
// 90 days, then by path.
func sortProjects(ps []Project, p Period) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if p.Of(a.Usage) != p.Of(b.Usage) {
			return p.Of(a.Usage) > p.Of(b.Usage)
		}
		if a.Usage.Quarter != b.Usage.Quarter {
			return a.Usage.Quarter > b.Usage.Quarter
		}
		return a.Path < b.Path
	})
}

// usageOf sums a device's days into the report's periods. Days[0] is the
// device's UTC day at collection, shift days before the report's.
func usageOf(days []int64, shift int) Usage {
	sum := func(n int) int64 {
		var t int64
		for i := 0; i < n-shift && i < len(days); i++ {
			t += days[i]
		}
		return t
	}
	return Usage{Today: sum(1), Week: sum(7), Month: sum(30), Quarter: sum(90)}
}

// olderUsage sums an account from a collector older than v0.2.0 into the
// report's periods. Its snapshot has no days, only inOut, its input plus
// output over the device's 90 days, which are the report's 90 days as
// nearly as that collector counts them: they end when the device last
// collected, and hold a session whole while it was active in them. A
// period that began after last, the account's newest activity, is 0; any
// other period shorter than 90 days is not known.
func olderUsage(inOut int64, last *time.Time, now time.Time) Usage {
	var u Usage
	today := time.Unix(now.Unix()/86400*86400, 0).UTC()
	for _, p := range Periods {
		switch {
		case last != nil && last.Before(today.AddDate(0, 0, 1-p.days())):
		case p == Quarter:
			u.Quarter = inOut
		default:
			u.Unknown |= p.bit()
		}
	}
	return u
}

// dayShift is how many UTC days the report's day is past collectedAt's.
func dayShift(collectedAt, now time.Time) int {
	return max(int(now.Unix()/86400-collectedAt.Unix()/86400), 0)
}

func (u Usage) add(v Usage) Usage {
	return Usage{Today: u.Today + v.Today, Week: u.Week + v.Week, Month: u.Month + v.Month, Quarter: u.Quarter + v.Quarter,
		Unknown: u.Unknown | v.Unknown}
}

func orEmpty(days []int64) []int64 {
	if days == nil {
		return []int64{}
	}
	return days
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
