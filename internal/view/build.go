package view

import (
	"cmp"
	"slices"
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
		Providers: []Provider{},
		Projects:  []Project{},
	}

	totals := collect.Totals(st)
	for _, p := range snapshot.Providers {
		src := st.Sources[p]
		pv := Provider{Provider: p, Status: cmp.Or(src.Status, "skipped"), Error: strPtr(src.Error), Homes: src.Homes, Accounts: []Account{}}
		if pv.Homes == nil {
			pv.Homes = []string{}
		}
		for _, a := range totals {
			if a.Provider == p {
				pv.Accounts = append(pv.Accounts, accountView(st, p, pv.Homes, a, now))
			}
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

// accountView is one of this device's accounts of provider, whose homes are
// homes, without its name, which comes from the team.
func accountView(st *state.State, provider string, homes []string, a collect.AccountTotals, now time.Time) Account {
	days := collect.DaysOf(a.Hours, now)
	acct := Account{
		Label:        a.Label,
		Current:      a.Current,
		Home:         currentHome(st, provider, homes, a.Label),
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
		acct.Quota, acct.State = quotaOf(Quota{Source: a.Quota.Source, From: a.QuotaFrom}, provider, readings(a.Quota.Windows, a.Quota.At), a.Quota.At, now)
	}
	if a.LinkedSessions > 0 {
		// Only Hermes bills through another harness's login.
		acct.LinkedUsage = append(acct.LinkedUsage, LinkedUsage{Provider: "hermes", Sessions: a.LinkedSessions, Tokens: a.Linked})
	}
	for _, pr := range a.Projects {
		acct.Projects = append(acct.Projects, projectView(pr, now))
	}
	return acct
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

// quotaOf is q, a reading of provider's account whose newest window was read
// at at, with its age and its windows rs as they read at now, and the
// account's state.
func quotaOf(q Quota, provider string, rs []reading, at, now time.Time) (*Quota, string) {
	q.ObservedAt = at.UTC()
	q.AgeSeconds = int64(now.Sub(at).Seconds())
	var state string
	q.Windows, state = readQuota(withUnread(provider, rs), now)
	q.Stale = slices.ContainsFunc(q.Windows, func(w Window) bool { return w.Stale })
	return &q, state
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
	slices.SortStableFunc(ps, func(a, b Project) int {
		return cmp.Or(cmp.Compare(p.Of(b.Usage), p.Of(a.Usage)), cmp.Compare(b.Usage.Quarter, a.Usage.Quarter), cmp.Compare(a.Path, b.Path))
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

// dayShift is how many UTC days the report's day is past collectedAt's.
func dayShift(collectedAt, now time.Time) int {
	return max(int(now.Unix()/86400-collectedAt.Unix()/86400), 0)
}

func (u Usage) add(v Usage) Usage {
	return Usage{Today: u.Today + v.Today, Week: u.Week + v.Week, Month: u.Month + v.Month, Quarter: u.Quarter + v.Quarter}
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
