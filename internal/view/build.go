package view

import (
	"cmp"
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/meter"
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
	r.Collector.Health = health(r.Collector, now)

	loc := cmp.Or(in.Loc, time.Local)
	totals := collect.Totals(st, in.Folders)
	covered := collect.Coverage(st)
	for _, p := range snapshot.Providers {
		src := st.Sources[p]
		pv := Provider{Provider: p, Status: cmp.Or(src.Status, "skipped"), Error: strPtr(src.Error), Homes: src.Homes,
			CoveredSince: timePtr(covered[p]), Accounts: []Account{}}
		if pv.Homes == nil {
			pv.Homes = []string{}
		}
		for _, a := range totals {
			if a.Provider == p {
				pv.Accounts = append(pv.Accounts, accountView(st, p, pv.Homes, a, paceOf(in, p, a.Label), now, loc))
			}
		}
		r.Providers = append(r.Providers, pv)
	}
	for _, pr := range collect.Projects(st, in.Folders) {
		r.Projects = append(r.Projects, projectView(pr, now, loc))
	}
	sortProjects(r.Projects, Week)
	r.Team = buildTeam(in, totals, now)
	listDefaults(&r)
	for i := range r.Providers {
		for j := range r.Providers[i].Accounts {
			a := &r.Providers[i].Accounts[j]
			a.Name = teamName(r.Team, r.Providers[i].Provider, a.Label)
		}
	}
	stopRunOuts(&r, now)
	r.Attention = attention(r.Team, r.Collector, now)
	describe(&r, loc)
	return r
}

// Fill gives a report saved by an earlier release of this schema the fields
// added to it since, as Build makes them: the collector's health, the
// windows that limit each account, the working folders of each project,
// one each, since such a release made a project of each, the team's
// devices that do not update themselves, the forecasts whose run-out time
// passed with no spend since, and the words of each ATTENTION entry and
// each device's note, with clocks in this device's time zone. A report that
// has them keeps them.
//
// A report saved before burn has its input plus output as burn, the team's
// marked estimated and this device's titled in tokens in+out, and its
// sessions of 90 days as the sessions active in every period, as that
// release showed them.
func Fill(r *Report) {
	if r.Collector.Health == nil {
		r.Collector.Health = health(r.Collector, r.GeneratedAt)
	}
	for i := range r.Team.Devices {
		d := &r.Team.Devices[i]
		d.NotUpdating = d.NotUpdating || notUpdating(*d)
	}
	fillFolders(r.Projects)
	old := savedBeforeBurn(r)
	for i := range r.Providers {
		for j := range r.Providers[i].Accounts {
			a := &r.Providers[i].Accounts[j]
			fillLimits(a.Quota)
			fillFolders(a.Projects)
			if old && fillBurn(&a.Burn, a.Usage) {
				r.inOut = true
				if len(a.BurnDays) == 0 {
					a.BurnDays = a.Days
				}
			}
		}
	}
	for i := range r.Team.Providers {
		for j := range r.Team.Providers[i].Accounts {
			fillLimits(r.Team.Providers[i].Accounts[j].Quota)
		}
	}
	if old {
		fillOldBurn(r)
	}
	stopRunOuts(r, r.GeneratedAt)
	listDefaults(r)
	describe(r, nil)
}

// describe gives each ATTENTION entry its subject and text, and each team
// device its note, as the console words them in full, with clocks in loc,
// nil for local time, where they have none.
func describe(r *Report, loc *time.Location) {
	// So wide that no form is cut, and every device of an OLD entry named.
	const full = 1 << 16
	p := newPage(r, Options{Loc: loc})
	for i := range r.Attention {
		if a := &r.Attention[i]; a.Text == "" {
			a.Subject, a.Text = p.subject(*a).text(), p.attentionText(*a, full)[0].text()
		}
	}
	for i := range r.Team.Devices {
		if d := &r.Team.Devices[i]; d.Note == nil {
			d.Note = strPtr(p.deviceNote(*d, -1).text())
		}
	}
}

// savedBeforeBurn says report r was saved by a release before
// collect.ExtSince, which has no burn: by its collector's version, or, from
// a development build, by its having no burn anywhere. A later release can
// leave an account with usage and no burn, as its usage counts UTC days and
// its burn local ones: that one is not filled.
func savedBeforeBurn(r *Report) bool {
	if v := r.Collector.Version; selfupdate.Newer(v, "v0.0.0") {
		return selfupdate.Newer(collect.ExtSince, v)
	}
	for _, p := range r.Providers {
		for _, a := range p.Accounts {
			if a.Burn != (Usage{}) {
				return false
			}
		}
	}
	for _, d := range r.Team.Devices {
		if d.Burn != (Usage{}) {
			return false
		}
	}
	return true
}

// fillOldBurn gives a report saved before burn its input plus output as
// burn, as Fill says.
func fillOldBurn(r *Report) {
	var projects []*Project
	for i := range r.Projects {
		projects = append(projects, &r.Projects[i])
	}
	for i := range r.Providers {
		for j := range r.Providers[i].Accounts {
			for k := range r.Providers[i].Accounts[j].Projects {
				projects = append(projects, &r.Providers[i].Accounts[j].Projects[k])
			}
		}
	}
	for _, p := range projects {
		r.inOut = fillBurn(&p.Burn, p.Usage) || r.inOut
		if p.Active == (Counts{}) && p.Sessions > 0 {
			p.Active = Counts{Today: p.Sessions, Week: p.Sessions, Month: p.Sessions, Quarter: p.Sessions}
		}
	}
	t := &r.Team
	est := func(filled bool, mark *bool) {
		if filled {
			*mark, t.Estimated = true, true
		}
	}
	for i := range t.Devices {
		d := &t.Devices[i]
		est(fillBurn(&d.Burn, d.Usage), &d.BurnEstimated)
	}
	for i := range t.Providers {
		for j := range t.Providers[i].Accounts {
			a := &t.Providers[i].Accounts[j]
			est(fillBurn(&a.Burn, a.Usage), &a.BurnEstimated)
			for k := range a.PerDevice {
				fillBurn(&a.PerDevice[k].Burn, a.PerDevice[k].Usage)
			}
		}
	}
	for i := range t.Matrix.Columns {
		c := &t.Matrix.Columns[i]
		if fillBurn(&c.Burn, c.Usage) && c.WindowBurn == 0 {
			c.WindowBurn = c.WindowTokens
		}
	}
	for i := range t.Matrix.Rows {
		row := &t.Matrix.Rows[i]
		if fillBurn(&row.Burn, row.Usage) {
			row.BurnShare = row.Share
			est(true, &row.BurnEstimated)
		}
		for k := range row.Cells {
			c := &row.Cells[k]
			if fillBurn(&c.Burn, c.Usage) {
				c.BurnShare = c.Share
				if c.WindowBurn == 0 {
					c.WindowBurn = c.WindowTokens
				}
			}
		}
	}
	if t.Estimated && t.EstimatedReason == nil {
		t.EstimatedReason = strPtr("saved by a release before v0.4.0: tokens are in+out only")
	}
}

// fillBurn sets burn to usage where burn is nothing and usage is not, and
// says whether it did.
func fillBurn(burn *Usage, usage Usage) bool {
	if *burn != (Usage{}) || usage == (Usage{}) {
		return false
	}
	*burn = usage
	return true
}

// listDefaults gives every list of r that is missing an empty one, so it is
// an array in JSON, never null.
func listDefaults(r *Report) {
	for i := range r.Providers {
		for j := range r.Providers[i].Accounts {
			a := &r.Providers[i].Accounts[j]
			a.BurnDays = orEmpty(a.BurnDays)
		}
	}
	if r.Team.HeldBackBy == nil {
		r.Team.HeldBackBy = []string{}
	}
	if r.Team.CyclesHeldBackBy == nil {
		r.Team.CyclesHeldBackBy = []string{}
	}
	for i := range r.Team.Providers {
		for j := range r.Team.Providers[i].Accounts {
			a := &r.Team.Providers[i].Accounts[j]
			if a.Utilization == nil {
				a.Utilization = []Utilization{}
			}
		}
	}
	for i := range r.Team.Devices {
		if d := &r.Team.Devices[i]; d.Limitations == nil {
			d.Limitations = []string{}
		}
	}
}

// fillFolders gives each project saved without its working folders the one
// it was.
func fillFolders(ps []Project) {
	for i := range ps {
		ps[i].Folders = max(ps[i].Folders, 1)
	}
}

// fillLimits marks the windows of a quota that limit the account, as
// readQuota does, unless its main window is marked already.
func fillLimits(q *Quota) {
	if q == nil {
		return
	}
	m := mainWindow(q.Windows)
	if m == nil || m.Limits {
		return
	}
	main := *m
	for i := range q.Windows {
		w := &q.Windows[i]
		w.Limits = w.Main || limitsMore(*w, main)
	}
}

// accountView is one of this device's accounts of provider, whose homes are
// homes, without its name, which comes from the team. Its windows go at the
// pace their readings pts show.
func accountView(st *state.State, provider string, homes []string, a collect.AccountTotals, pts []pacePoint, now time.Time, loc *time.Location) Account {
	days := collect.DaysOf(a.Hours.Series(meter.Use.InOut), now)
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
		Burn:         localUsage(a.Burn, now, loc),
		Fresh:        localUsage(a.Hours.Series(meter.Use.Fresh), now, loc),
		BurnDays:     localDays(a.Burn, now, loc),
		LinkedUsage:  []LinkedUsage{},
		LastActiveAt: timePtr(a.LastActive),
		Projects:     []Project{},
	}
	if a.Quota != nil {
		acct.Quota, acct.State = quotaOf(Quota{Source: a.Quota.Source, From: a.QuotaFrom}, provider, readings(a.Quota.Windows, a.Quota.At), pts, a.Quota.At, now)
	}
	if provider == "claude" && (a.Nulls > 0 || a.OverTracked > 0) {
		acct.TranscriptGaps = &TranscriptGaps{NullSnapshots: a.Nulls, OverTracked: a.OverTracked}
	}
	if a.LinkedSessions > 0 {
		// Only Hermes bills through another harness's login.
		acct.LinkedUsage = append(acct.LinkedUsage, LinkedUsage{Provider: "hermes", Sessions: a.LinkedSessions, Tokens: a.Linked})
	}
	for _, pr := range a.Projects {
		acct.Projects = append(acct.Projects, projectView(pr, now, loc))
	}
	sortProjects(acct.Projects, Week)
	return acct
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
// at at, with its age and its windows rs as they read at now, at the pace
// the account's readings pts show, and the account's state.
func quotaOf(q Quota, provider string, rs []reading, pts []pacePoint, at, now time.Time) (*Quota, string) {
	q.ObservedAt = at.UTC()
	q.AgeSeconds = int64(now.Sub(at).Seconds())
	var state string
	q.Windows, state = readQuota(withUnread(provider, rs), pts, now)
	q.Stale = slices.ContainsFunc(q.Windows, func(w Window) bool { return w.Stale })
	return &q, state
}

// paceOf is the readings of provider's account label, however a device
// spells it, that this device's samples and the team's reads hold: what the
// pace of its windows goes by, beside their newest reading.
func paceOf(in Input, provider, label string) []pacePoint {
	k := collect.HistoryKey(provider, label)
	var out []pacePoint
	for _, s := range in.Samples {
		for _, a := range s.Accounts {
			if a.QuotaAt == nil || a.Provider != provider || collect.HistoryKey(a.Provider, a.Label) != k {
				continue
			}
			for _, w := range a.Windows {
				out = append(out, pacePoint{At: *a.QuotaAt, Window: w.Name, ResetsAt: w.ResetsAt, Percent: w.Percent})
			}
		}
	}
	for _, p := range in.Team.History[k] {
		out = append(out, pacePoint{At: p.At, Window: p.Window, ResetsAt: p.ResetsAt, Percent: p.Percent})
	}
	return out
}

// projectView is a project's totals as the report shows them, in the local
// periods of loc as of now; its usage keeps UTC days.
func projectView(p collect.ProjectTotals, now time.Time, loc *time.Location) Project {
	return Project{
		Path:         p.Path,
		Folders:      p.Folders,
		Sessions:     p.Sessions,
		Tokens:       p.Tokens,
		Usage:        usageOf(collect.DaysOf(p.Hours.Series(meter.Use.InOut), now), 0),
		Burn:         localUsage(p.Burn, now, loc),
		Fresh:        localUsage(p.Hours.Series(meter.Use.Fresh), now, loc),
		Active:       activeCounts(p.Active, now, loc),
		Providers:    p.Providers,
		LastActiveAt: timePtr(p.LastActive),
	}
}

// sortProjects puts projects in byBurn's order for the period.
func sortProjects(ps []Project, p Period) {
	slices.SortStableFunc(ps, byBurn(p, func(x Project) Usage { return x.Burn }, func(x Project) string { return x.Path }))
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

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
