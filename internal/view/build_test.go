package view

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

func TestAccountOfThisDevice(t *testing.T) {
	st := emptyState()
	start := now.Add(-2 * 24 * time.Hour)
	addAccount(st, "claude", "ann@acme.io", true, &state.Quota{At: now, Source: "cache", Windows: []snapshot.Window{
		{Name: "5h", Percent: 10, Minutes: 300, ResetsAt: tp(now.Add(4 * time.Hour))},
		week7(50, start.Add(week)),
	}}, 0)
	st.Accounts[state.Key("claude", "ann@acme.io")].Plan = "max"
	spend(st, "claude", "ann@acme.io", "/work/web", 100, now.Add(-time.Hour), now.Add(-26*time.Hour), now.Add(-10*24*time.Hour), now.Add(-40*24*time.Hour))
	r := Build(newFixture(t, st).in)

	a := findAccount(t, r, "claude", "ann@acme.io")
	if a.Name != "ann" || a.State != StateOver || a.Plan == nil || *a.Plan != "max" || !a.Current {
		t.Fatalf("account = %+v", a)
	}
	if want := (Usage{Today: 100, Week: 200, Month: 300, Quarter: 400}); a.Usage != want {
		t.Fatalf("usage = %+v, want %+v", a.Usage, want)
	}
	if len(a.Days) != 41 || a.Days[0] != 100 || a.Days[1] != 100 || a.Days[40] != 100 {
		t.Fatalf("days = %v", a.Days)
	}
	ws := a.Quota.Windows
	if len(ws) != 2 || ws[0].Main || !ws[1].Main || ws[1].State != StateOver || ws[1].Forecast.Percent != 175 || ws[0].State != StateOK {
		t.Fatalf("windows = %+v", ws)
	}
	if len(r.Projects) != 1 || r.Projects[0].Path != "/work/web" || r.Projects[0].Usage.Week != 200 || !reflect.DeepEqual(r.Projects[0].Providers, []string{"claude"}) {
		t.Fatalf("projects = %+v", r.Projects)
	}
}

func TestProjectsSortByPeriod(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann", true, nil, 0)
	addAccount(st, "codex", "ann", true, nil, 0)
	spend(st, "claude", "ann", "/p/recent", 10, now.Add(-time.Hour))
	spend(st, "codex", "ann", "/p/recent", 30, now.Add(-2*time.Hour))
	spend(st, "claude", "ann", "/p/month", 100, now.Add(-20*24*time.Hour))
	spend(st, "codex", "ann", "/p/quarter", 1000, now.Add(-60*24*time.Hour))
	r := Build(newFixture(t, st).in)
	var got []string
	for _, p := range r.Projects {
		got = append(got, p.Path)
	}
	if want := []string{"/p/recent", "/p/quarter", "/p/month"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("projects by 7d = %v", got)
	}
	if p := r.Projects[0]; !reflect.DeepEqual(p.Providers, []string{"codex", "claude"}) || p.Sessions != 2 || p.Usage.Today != 40 {
		t.Fatalf("recent = %+v", p)
	}
	sortProjects(r.Projects, Month)
	if r.Projects[0].Path != "/p/month" {
		t.Fatalf("by 30d first = %s", r.Projects[0].Path)
	}
}

// keys lists a JSON object's field names, and those of every element of each
// array of objects, as dotted paths.
func keys(prefix string, v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			out[prefix+k] = true
			keys(prefix+k+".", child, out)
		}
	case []any:
		for _, e := range x {
			keys(prefix, e, out)
		}
	}
}

func TestJSONFieldNamesAreStable(t *testing.T) {
	start := now.Add(-5 * 24 * time.Hour)
	st := emptyState()
	st.LastError, st.LastErrorAt = "x", now
	st.Relay.LastError = "y"
	st.Update = state.Update{CheckedAt: now, Latest: "v9.0.0", Installed: "v9.0.0", Error: "z"}
	st.Schedule.Error = "w"
	addAccount(st, "claude", "ann", true, &state.Quota{At: now, Source: "cache", Windows: []snapshot.Window{
		{Name: "5h", Percent: 30, ResetsAt: tp(now.Add(4 * time.Hour)), Minutes: 300}, week7(90, start.Add(week)),
	}}, 10)
	st.Accounts[state.Key("claude", "ann")].Plan = "max"
	st.Sessions[state.Key("claude", "ann-s")].Nulls = 2
	addAccount(st, "codex", "bob", true, codexQuota(now.Add(-7*time.Hour), 80), 10)
	addHermes(st, "openai-codex", "codex", "bob", 5)
	f := newFixture(t, st)
	other := emptyState()
	other.Sources["codex"] = state.Source{Status: "error", Error: "e"}
	addAccount(other, "codex", "bob", true, codexQuota(now.Add(-7*time.Hour), 40), 10)
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: []snapshot.Doc{
		otherDoc(t, f.key, "d-other-device", "o", now.Add(-48*time.Hour), other),
	}}
	f.in.Config.Aliases = map[string]state.Alias{state.Key("codex", "bob"): {Name: "b", At: now}}
	f.seal(now)
	r := Build(f.in)
	if r.SchemaVersion != 4 {
		t.Fatalf("schema version %d", r.SchemaVersion)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	keys("", v, got)
	var list []string
	for k := range got {
		list = append(list, k)
	}
	sort.Strings(list)
	want := strings.Fields(jsonFields)
	if !reflect.DeepEqual(list, want) {
		gotSet, wantSet := map[string]bool{}, map[string]bool{}
		for _, k := range list {
			gotSet[k] = true
		}
		for _, k := range want {
			wantSet[k] = true
		}
		for _, k := range want {
			if !gotSet[k] {
				t.Errorf("missing field %s", k)
			}
		}
		for _, k := range list {
			if !wantSet[k] {
				t.Errorf("new field %s: add it here and to docs/json-schema.md; renaming or removing a field needs a new schema version", k)
			}
		}
	}

	// Empty lists are arrays and absent values are null, never missing.
	empty := Build(newFixture(t, emptyState()).in)
	b, _ = json.Marshal(empty)
	for _, want := range []string{`"attention":[]`, `"providers":[{`, `"accounts":[]`, `"homes":["/home/.claude"]`, `"last_error":null`, `"pulled_at":null`, `"projects":[]`, `"columns":[]`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("empty report lacks %s: %s", want, b)
		}
	}
}

// jsonFields are every field of the report, as dotted paths.
const jsonFields = `
attention attention.account attention.at attention.devices attention.kind attention.message attention.name
attention.percent attention.provider attention.reading_age_seconds attention.resets_at attention.window
collector collector.device collector.device_label
collector.health collector.health.at collector.health.item collector.health.release collector.health.state collector.health.status
collector.last_error collector.last_error_at
collector.last_run_at collector.last_success_at collector.os_user collector.relay
collector.relay.last_error collector.relay.last_pull_at collector.relay.last_push_at collector.relay.pending
collector.relay.url collector.schedule collector.schedule.error collector.schedule.foreground collector.schedule.registered
collector.team collector.update collector.update.checked_at collector.update.error
collector.update.latest collector.update.staged collector.version
generated_at
projects projects.active_sessions projects.active_sessions.30d projects.active_sessions.7d projects.active_sessions.90d projects.active_sessions.today
projects.burn projects.burn.30d projects.burn.7d projects.burn.90d projects.burn.today
projects.folders projects.fresh projects.fresh.30d projects.fresh.7d projects.fresh.90d projects.fresh.today
projects.last_active_at projects.path projects.providers projects.sessions projects.tokens
projects.tokens.cache_read projects.tokens.cache_write projects.tokens.input projects.tokens.output
projects.usage projects.usage.30d projects.usage.7d projects.usage.90d projects.usage.today
providers providers.accounts providers.accounts.burn providers.accounts.burn.30d providers.accounts.burn.7d providers.accounts.burn.90d providers.accounts.burn.today
providers.accounts.burn_days providers.accounts.current providers.accounts.days
providers.accounts.fresh providers.accounts.fresh.30d providers.accounts.fresh.7d providers.accounts.fresh.90d providers.accounts.fresh.today
providers.accounts.home providers.accounts.label providers.accounts.last_active_at
providers.accounts.link providers.accounts.link.label providers.accounts.link.provider
providers.accounts.linked_usage providers.accounts.linked_usage.provider providers.accounts.linked_usage.sessions
providers.accounts.linked_usage.tokens providers.accounts.linked_usage.tokens.cache_read providers.accounts.linked_usage.tokens.cache_write
providers.accounts.linked_usage.tokens.input providers.accounts.linked_usage.tokens.output providers.accounts.name providers.accounts.plan
providers.accounts.projects providers.accounts.projects.active_sessions providers.accounts.projects.active_sessions.30d providers.accounts.projects.active_sessions.7d providers.accounts.projects.active_sessions.90d providers.accounts.projects.active_sessions.today
providers.accounts.projects.burn providers.accounts.projects.burn.30d providers.accounts.projects.burn.7d providers.accounts.projects.burn.90d providers.accounts.projects.burn.today
providers.accounts.projects.folders providers.accounts.projects.fresh providers.accounts.projects.fresh.30d providers.accounts.projects.fresh.7d providers.accounts.projects.fresh.90d providers.accounts.projects.fresh.today
providers.accounts.projects.last_active_at providers.accounts.projects.path providers.accounts.projects.sessions
providers.accounts.projects.tokens providers.accounts.projects.tokens.cache_read providers.accounts.projects.tokens.cache_write
providers.accounts.projects.tokens.input providers.accounts.projects.tokens.output
providers.accounts.projects.usage providers.accounts.projects.usage.30d providers.accounts.projects.usage.7d
providers.accounts.projects.usage.90d providers.accounts.projects.usage.today
providers.accounts.quota providers.accounts.quota.age_seconds providers.accounts.quota.from providers.accounts.quota.observed_at
providers.accounts.quota.source providers.accounts.quota.stale providers.accounts.quota.windows
providers.accounts.quota.windows.forecast providers.accounts.quota.windows.forecast.basis providers.accounts.quota.windows.forecast.elapsed
providers.accounts.quota.windows.forecast.pace providers.accounts.quota.windows.forecast.percent providers.accounts.quota.windows.forecast.runs_out_at
providers.accounts.quota.windows.limits providers.accounts.quota.windows.main providers.accounts.quota.windows.minutes providers.accounts.quota.windows.name
providers.accounts.quota.windows.observed_at providers.accounts.quota.windows.percent providers.accounts.quota.windows.reset
providers.accounts.quota.windows.resets_at providers.accounts.quota.windows.stale providers.accounts.quota.windows.state
providers.accounts.sessions providers.accounts.state providers.accounts.tokens providers.accounts.tokens.cache_read
providers.accounts.tokens.cache_write providers.accounts.tokens.input providers.accounts.tokens.output
providers.accounts.transcript_gaps providers.accounts.transcript_gaps.null_snapshots providers.accounts.transcript_gaps.over_tracked
providers.accounts.usage providers.accounts.usage.30d providers.accounts.usage.7d providers.accounts.usage.90d providers.accounts.usage.today
providers.covered_since providers.error providers.homes providers.provider providers.status
schema_version
team team.devices team.devices.age_seconds team.devices.behind_since team.devices.burn team.devices.burn.30d team.devices.burn.7d team.devices.burn.90d team.devices.burn.today
team.devices.burn_estimated team.devices.collected_at team.devices.collector_version
team.devices.device team.devices.error team.devices.gap team.devices.label team.devices.last_error team.devices.last_error_at team.devices.last_success_at
team.devices.limitations team.devices.not_updating team.devices.old team.devices.os_user team.devices.replaced_by
team.devices.silent team.devices.sources team.devices.sources.error team.devices.sources.provider
team.devices.sources.status team.devices.this_device team.devices.update_error team.devices.usage team.devices.usage.30d team.devices.usage.7d
team.devices.usage.90d team.devices.usage.today
team.estimated team.estimated_reason team.held_back_by team.latest_version
team.matrix team.matrix.columns team.matrix.columns.burn team.matrix.columns.burn.30d team.matrix.columns.burn.7d team.matrix.columns.burn.90d team.matrix.columns.burn.today
team.matrix.columns.label team.matrix.columns.name team.matrix.columns.no_quota
team.matrix.columns.percent team.matrix.columns.provider team.matrix.columns.state team.matrix.columns.usage
team.matrix.columns.usage.30d team.matrix.columns.usage.7d team.matrix.columns.usage.90d team.matrix.columns.usage.today
team.matrix.columns.window_burn team.matrix.columns.window_tokens
team.matrix.rows team.matrix.rows.burn team.matrix.rows.burn.30d team.matrix.rows.burn.7d team.matrix.rows.burn.90d team.matrix.rows.burn.today
team.matrix.rows.burn_estimated team.matrix.rows.burn_share team.matrix.rows.burn_share.30d team.matrix.rows.burn_share.7d team.matrix.rows.burn_share.90d team.matrix.rows.burn_share.today
team.matrix.rows.cells team.matrix.rows.cells.burn team.matrix.rows.cells.burn.30d team.matrix.rows.cells.burn.7d team.matrix.rows.cells.burn.90d team.matrix.rows.cells.burn.today
team.matrix.rows.cells.burn_share team.matrix.rows.cells.burn_share.30d team.matrix.rows.cells.burn_share.7d team.matrix.rows.cells.burn_share.90d team.matrix.rows.cells.burn_share.today
team.matrix.rows.cells.share team.matrix.rows.cells.share.30d
team.matrix.rows.cells.share.7d team.matrix.rows.cells.share.90d team.matrix.rows.cells.share.today team.matrix.rows.cells.usage
team.matrix.rows.cells.usage.30d team.matrix.rows.cells.usage.7d team.matrix.rows.cells.usage.90d team.matrix.rows.cells.usage.today
team.matrix.rows.cells.window_burn team.matrix.rows.cells.window_tokens team.matrix.rows.device team.matrix.rows.device_id team.matrix.rows.usage
team.matrix.rows.usage.30d team.matrix.rows.usage.7d team.matrix.rows.usage.90d team.matrix.rows.usage.today
team.matrix.rows.share team.matrix.rows.share.30d team.matrix.rows.share.7d team.matrix.rows.share.90d team.matrix.rows.share.today
team.providers team.providers.accounts team.providers.accounts.alias team.providers.accounts.burn team.providers.accounts.burn.30d team.providers.accounts.burn.7d team.providers.accounts.burn.90d team.providers.accounts.burn.today
team.providers.accounts.burn_estimated team.providers.accounts.busiest team.providers.accounts.current
team.providers.accounts.devices team.providers.accounts.label team.providers.accounts.last_active_at
team.providers.accounts.link team.providers.accounts.link.label team.providers.accounts.link.provider
team.providers.accounts.linked_usage team.providers.accounts.linked_usage.devices team.providers.accounts.linked_usage.label
team.providers.accounts.linked_usage.provider team.providers.accounts.linked_usage.sessions team.providers.accounts.linked_usage.tokens
team.providers.accounts.linked_usage.tokens.cache_read team.providers.accounts.linked_usage.tokens.cache_write
team.providers.accounts.linked_usage.tokens.input team.providers.accounts.linked_usage.tokens.output
team.providers.accounts.name team.providers.accounts.no_reading team.providers.accounts.outside
team.providers.accounts.per_device team.providers.accounts.per_device.burn team.providers.accounts.per_device.burn.30d team.providers.accounts.per_device.burn.7d team.providers.accounts.per_device.burn.90d team.providers.accounts.per_device.burn.today
team.providers.accounts.per_device.current team.providers.accounts.per_device.device
team.providers.accounts.per_device.device_id team.providers.accounts.per_device.last_active_at team.providers.accounts.per_device.sessions
team.providers.accounts.per_device.tokens team.providers.accounts.per_device.tokens.cache_read team.providers.accounts.per_device.tokens.cache_write
team.providers.accounts.per_device.tokens.input team.providers.accounts.per_device.tokens.output
team.providers.accounts.per_device.usage team.providers.accounts.per_device.usage.30d team.providers.accounts.per_device.usage.7d
team.providers.accounts.per_device.usage.90d team.providers.accounts.per_device.usage.today
team.providers.accounts.plan team.providers.accounts.quota
team.providers.accounts.quota.age_seconds team.providers.accounts.quota.device team.providers.accounts.quota.from team.providers.accounts.quota.observed_at
team.providers.accounts.quota.stale team.providers.accounts.quota.windows
team.providers.accounts.quota.windows.forecast team.providers.accounts.quota.windows.forecast.basis team.providers.accounts.quota.windows.forecast.elapsed
team.providers.accounts.quota.windows.forecast.pace team.providers.accounts.quota.windows.forecast.percent team.providers.accounts.quota.windows.forecast.runs_out_at
team.providers.accounts.quota.windows.limits team.providers.accounts.quota.windows.main team.providers.accounts.quota.windows.minutes team.providers.accounts.quota.windows.name
team.providers.accounts.quota.windows.observed_at team.providers.accounts.quota.windows.percent team.providers.accounts.quota.windows.reset
team.providers.accounts.quota.windows.resets_at team.providers.accounts.quota.windows.stale team.providers.accounts.quota.windows.state
team.providers.accounts.sessions team.providers.accounts.state team.providers.accounts.subscription team.providers.accounts.tokens
team.providers.accounts.tokens.cache_read team.providers.accounts.tokens.cache_write team.providers.accounts.tokens.input
team.providers.accounts.tokens.output
team.providers.accounts.usage team.providers.accounts.usage.30d team.providers.accounts.usage.7d team.providers.accounts.usage.90d
team.providers.accounts.usage.today team.providers.accounts.users
team.providers.provider team.pulled_at
`

// hour is the ledger key of the hour that holds t.
func hour(t time.Time) int64 { return t.Unix() / 3600 }

// TestLocalToday: today is the hours since local midnight, an hour counting
// on the day its middle falls in, so a zone half an hour off UTC splits
// none, and hours after now count in no period.
func TestLocalToday(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 8, 31, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		zone     *time.Location
		in, out  time.Time // the first hour today, and the one before it
		dayStart time.Time
	}{
		// 12:00 UTC is 15:00 at UTC+3, whose day began at 21:00 UTC.
		{time.FixedZone("UTC+3", 3*3600), at(21, 0), at(20, 0), at(21, 0)},
		// And 17:30 at UTC+5:30, whose day began at 18:30 UTC: the hour
		// from 18:00 is half in each day, and counts in the new one.
		{time.FixedZone("UTC+5:30", 5*3600+1800), at(18, 0), at(17, 0), at(18, 30)},
	} {
		series := map[int64]int64{hour(c.in): 1, hour(c.in) + 1: 10, hour(c.out): 100, hour(now) + 1: 1000}
		u := localUsage(series, now, c.zone)
		if u.Today != 11 || u.Week != 111 {
			t.Errorf("%s: usage %+v, want today 11 and 7d 111", c.zone, u)
		}
		if got := localDays(series, now, c.zone); !reflect.DeepEqual(got, []int64{11, 100}) {
			t.Errorf("%s: days %v, want [11 100]", c.zone, got)
		}
		n := activeCounts([]time.Time{c.dayStart, c.dayStart.Add(-time.Minute), now.Add(time.Hour)}, now, c.zone)
		if n != (Counts{Today: 2, Week: 3, Month: 3, Quarter: 3}) {
			t.Errorf("%s: active %+v", c.zone, n)
		}
	}
}

// TestLocalRolling: 7d, 30d, and 90d are the last 168, 720, and 2160 hours,
// the current one included, whatever the day.
func TestLocalRolling(t *testing.T) {
	cur := hour(now)
	series := map[int64]int64{}
	for i, n := range []int64{167, 168, 719, 720, 2159, 2160} {
		series[cur-n] = 1 << i
	}
	u := localUsage(series, now, time.UTC)
	if u != (Usage{Today: 0, Week: 1, Month: 1 + 2 + 4, Quarter: 1 + 2 + 4 + 8 + 16}) {
		t.Fatalf("usage %+v", u)
	}
	n := activeCounts([]time.Time{logsHour(cur - 167), logsHour(cur - 168).Add(59 * time.Minute)}, now, time.UTC)
	if n != (Counts{Week: 1, Month: 2, Quarter: 2}) {
		t.Fatalf("active %+v", n)
	}
	// Local days are calendar days: the 90th began at midnight, and the
	// hours before it on the day before count in 90d, not in the days.
	if got := localDays(series, now, time.UTC); len(got) != 31 || got[30] != 4+8 {
		t.Fatalf("days %v", got)
	}
	series[cur-2159+12] = 64
	if got := localDays(series, now, time.UTC); len(got) != 90 || got[89] != 64 {
		t.Fatalf("days %d long, the 90th %d", len(got), got[len(got)-1])
	}
}

func logsHour(h int64) time.Time { return time.Unix(h*3600, 0).UTC() }

// TestAccountBurn: an account's and a project's burn weigh every kind of
// token in the local periods; usage stays input plus output in UTC days.
func TestAccountBurn(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "ann@acme.dev", true, nil, 0)
	addAccount(st, "codex", "ann@acme.dev", true, nil, 0)
	// 22:00 UTC yesterday is today at UTC+3.
	late := time.Date(2026, 8, 31, 22, 10, 0, 0, time.UTC)
	s := &state.Session{Provider: "claude", Project: "/work/app", Updated: late, Nulls: 3,
		By:   map[string]snapshot.Tokens{"ann@acme.dev": {Input: 100, Output: 10, CacheRead: 1000, CacheWrite: 40}},
		Side: snapshot.Tokens{Output: 6}}
	s.U.Add(late, meter.Use{Input: 100, Output: 10, CacheRead: 1000, CacheWrite: 40, CacheWrite1h: 20})
	st.Sessions[state.Key("claude", "a4c2e917")] = s
	spend(st, "codex", "ann@acme.dev", "/work/app", 50, now.Add(-10*24*time.Hour))
	st.Sessions[state.Key("codex", "ann@acme.dev", "/work/app", now.Add(-10*24*time.Hour).String())].Nulls = 4
	f := newFixture(t, st)
	f.in.Loc = time.FixedZone("UTC+3", 3*3600)
	r := Build(f.in)

	a := findAccount(t, r, "claude", "ann@acme.dev")
	// 100 + 10·5 + 1000·0.1 + 20·1.25 + 20·2
	if want := (Usage{Today: 315, Week: 315, Month: 315, Quarter: 315}); a.Burn != want {
		t.Errorf("burn %+v, want %+v", a.Burn, want)
	}
	if want := (Usage{Today: 150, Week: 150, Month: 150, Quarter: 150}); a.Fresh != want {
		t.Errorf("fresh %+v, want %+v", a.Fresh, want)
	}
	if !reflect.DeepEqual(a.BurnDays, []int64{315}) || a.Usage.Today != 0 || a.Usage.Week != 110 {
		t.Errorf("burn days %v, usage %+v", a.BurnDays, a.Usage)
	}
	// 6 of 10 output is Claude Code's own count: over twice the transcript's 4.
	if a.TranscriptGaps == nil || *a.TranscriptGaps != (TranscriptGaps{NullSnapshots: 3, OverTracked: 1}) {
		t.Errorf("transcript gaps %+v", a.TranscriptGaps)
	}
	if c := findAccount(t, r, "codex", "ann@acme.dev"); c.TranscriptGaps != nil || c.Burn.Month != 50 || c.Burn.Week != 0 {
		t.Errorf("codex account %+v", c)
	}
	if len(r.Projects) != 1 {
		t.Fatalf("projects %+v", r.Projects)
	}
	p := r.Projects[0]
	if p.Burn != (Usage{Today: 315, Week: 315, Month: 365, Quarter: 365}) || p.Fresh.Quarter != 200 ||
		p.Active != (Counts{Today: 1, Week: 1, Month: 2, Quarter: 2}) || p.Sessions != 2 {
		t.Errorf("project %+v", p)
	}
	for _, pv := range r.Providers {
		want := map[string]*time.Time{"claude": tp(time.Date(2026, 8, 31, 22, 0, 0, 0, time.UTC)), "codex": tp(logsHour(hour(now.Add(-10 * 24 * time.Hour))))}[pv.Provider]
		if !sameTime(pv.CoveredSince, want) {
			t.Errorf("%s covered since %v, want %v", pv.Provider, pv.CoveredSince, want)
		}
	}
}

// TestFillGivesAnEarlierReportBurn: a report saved before burn shows its
// input plus output as burn, the team's marked estimated, and its sessions
// of 90 days in every period.
func TestFillGivesAnEarlierReportBurn(t *testing.T) {
	r := loadReport(t, "team")
	for i := range r.Providers {
		for j := range r.Providers[i].Accounts {
			a := &r.Providers[i].Accounts[j]
			a.Burn, a.BurnDays, a.Days = Usage{}, nil, []int64{7, 0, 3}
		}
	}
	for i := range r.Projects {
		r.Projects[i].Burn, r.Projects[i].Active = Usage{}, Counts{}
	}
	t0 := &r.Team
	t0.Estimated, t0.EstimatedReason = false, nil
	for i := range t0.Devices {
		t0.Devices[i].Burn = Usage{}
	}
	for i := range t0.Providers {
		for j := range t0.Providers[i].Accounts {
			t0.Providers[i].Accounts[j].Burn = Usage{}
		}
	}
	ta := &t0.Providers[0].Accounts[0]
	ta.PerDevice = []DeviceUsage{{Device: "annbook (ann)", Usage: Usage{Week: 5, Quarter: 9}}}
	for i := range t0.Matrix.Columns {
		t0.Matrix.Columns[i].Burn, t0.Matrix.Columns[i].WindowBurn = Usage{}, 0
	}
	for i := range t0.Matrix.Rows {
		row := &t0.Matrix.Rows[i]
		row.Burn, row.BurnShare = Usage{}, Share{}
		for k := range row.Cells {
			row.Cells[k].Burn, row.Cells[k].BurnShare, row.Cells[k].WindowBurn = Usage{}, Share{}, 0
		}
	}
	Fill(&r)

	a := r.Providers[0].Accounts[0]
	if a.Burn != a.Usage || !reflect.DeepEqual(a.BurnDays, []int64{7, 0, 3}) {
		t.Errorf("account burn %+v days %v, usage %+v", a.Burn, a.BurnDays, a.Usage)
	}
	p := r.Projects[0]
	if p.Burn != p.Usage || p.Active != (Counts{Today: p.Sessions, Week: p.Sessions, Month: p.Sessions, Quarter: p.Sessions}) {
		t.Errorf("project %+v", p)
	}
	row, col := r.Team.Matrix.Rows[0], r.Team.Matrix.Columns[0]
	if row.Burn != row.Usage || !row.BurnEstimated || !reflect.DeepEqual(row.BurnShare, row.Share) ||
		row.Cells[0].Burn != row.Cells[0].Usage || row.Cells[0].WindowBurn != row.Cells[0].WindowTokens ||
		col.Burn != col.Usage || col.WindowBurn != col.WindowTokens {
		t.Errorf("matrix row %+v, column %+v", row, col)
	}
	d := r.Team.Devices[0]
	if d.Burn != d.Usage || !d.BurnEstimated || ta.Burn != ta.Usage || !ta.BurnEstimated || ta.PerDevice[0].Burn != ta.PerDevice[0].Usage {
		t.Errorf("device %+v, account %+v", d, ta)
	}
	if !r.Team.Estimated || r.Team.EstimatedReason == nil || !strings.Contains(*r.Team.EstimatedReason, "before v0.4.0") {
		t.Errorf("team estimated %v, reason %v", r.Team.Estimated, r.Team.EstimatedReason)
	}
	// A list the release did not save is an empty one.
	var old Report
	if err := json.Unmarshal([]byte(`{"providers":[{"accounts":[{"label":"x"}]}],"team":{"devices":[{"device":"d"}]}}`), &old); err != nil {
		t.Fatal(err)
	}
	Fill(&old)
	if old.Providers[0].Accounts[0].BurnDays == nil || old.Team.HeldBackBy == nil || old.Team.Devices[0].Limitations == nil || old.Team.Estimated {
		t.Errorf("filled lists: %+v", old)
	}
}
