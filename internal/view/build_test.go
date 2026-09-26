package view

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
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
projects projects.folders projects.last_active_at projects.path projects.providers projects.sessions projects.tokens
projects.tokens.cache_read projects.tokens.cache_write projects.tokens.input projects.tokens.output
projects.usage projects.usage.30d projects.usage.7d projects.usage.90d projects.usage.today
providers providers.accounts providers.accounts.current providers.accounts.days
providers.accounts.home providers.accounts.label providers.accounts.last_active_at
providers.accounts.link providers.accounts.link.label providers.accounts.link.provider
providers.accounts.linked_usage providers.accounts.linked_usage.provider providers.accounts.linked_usage.sessions
providers.accounts.linked_usage.tokens providers.accounts.linked_usage.tokens.cache_read providers.accounts.linked_usage.tokens.cache_write
providers.accounts.linked_usage.tokens.input providers.accounts.linked_usage.tokens.output providers.accounts.name providers.accounts.plan
providers.accounts.projects providers.accounts.projects.folders providers.accounts.projects.last_active_at providers.accounts.projects.path providers.accounts.projects.sessions
providers.accounts.projects.tokens providers.accounts.projects.tokens.cache_read providers.accounts.projects.tokens.cache_write
providers.accounts.projects.tokens.input providers.accounts.projects.tokens.output
providers.accounts.projects.usage providers.accounts.projects.usage.30d providers.accounts.projects.usage.7d
providers.accounts.projects.usage.90d providers.accounts.projects.usage.today
providers.accounts.quota providers.accounts.quota.age_seconds providers.accounts.quota.from providers.accounts.quota.observed_at
providers.accounts.quota.source providers.accounts.quota.stale providers.accounts.quota.windows
providers.accounts.quota.windows.forecast providers.accounts.quota.windows.forecast.elapsed
providers.accounts.quota.windows.forecast.percent providers.accounts.quota.windows.forecast.runs_out_at
providers.accounts.quota.windows.limits providers.accounts.quota.windows.main providers.accounts.quota.windows.minutes providers.accounts.quota.windows.name
providers.accounts.quota.windows.observed_at providers.accounts.quota.windows.percent providers.accounts.quota.windows.reset
providers.accounts.quota.windows.resets_at providers.accounts.quota.windows.stale providers.accounts.quota.windows.state
providers.accounts.sessions providers.accounts.state providers.accounts.tokens providers.accounts.tokens.cache_read
providers.accounts.tokens.cache_write providers.accounts.tokens.input providers.accounts.tokens.output
providers.accounts.usage providers.accounts.usage.30d providers.accounts.usage.7d providers.accounts.usage.90d providers.accounts.usage.today
providers.error providers.homes providers.provider providers.status
schema_version
team team.devices team.devices.age_seconds team.devices.behind_since team.devices.collected_at team.devices.collector_version
team.devices.device team.devices.error team.devices.label team.devices.last_error team.devices.last_success_at
team.devices.not_updating team.devices.old team.devices.os_user team.devices.silent team.devices.sources team.devices.sources.error team.devices.sources.provider
team.devices.sources.status team.devices.this_device team.devices.update_error team.devices.usage team.devices.usage.30d team.devices.usage.7d
team.devices.usage.90d team.devices.usage.today
team.latest_version
team.matrix team.matrix.columns team.matrix.columns.label team.matrix.columns.name team.matrix.columns.no_quota
team.matrix.columns.percent team.matrix.columns.provider team.matrix.columns.state team.matrix.columns.usage
team.matrix.columns.usage.30d team.matrix.columns.usage.7d team.matrix.columns.usage.90d team.matrix.columns.usage.today
team.matrix.columns.window_tokens
team.matrix.rows team.matrix.rows.cells team.matrix.rows.cells.share team.matrix.rows.cells.share.30d
team.matrix.rows.cells.share.7d team.matrix.rows.cells.share.90d team.matrix.rows.cells.share.today team.matrix.rows.cells.usage
team.matrix.rows.cells.usage.30d team.matrix.rows.cells.usage.7d team.matrix.rows.cells.usage.90d team.matrix.rows.cells.usage.today
team.matrix.rows.cells.window_tokens team.matrix.rows.device team.matrix.rows.device_id team.matrix.rows.usage
team.matrix.rows.usage.30d team.matrix.rows.usage.7d team.matrix.rows.usage.90d team.matrix.rows.usage.today
team.matrix.rows.share team.matrix.rows.share.30d team.matrix.rows.share.7d team.matrix.rows.share.90d team.matrix.rows.share.today
team.providers team.providers.accounts team.providers.accounts.alias team.providers.accounts.busiest team.providers.accounts.current
team.providers.accounts.devices team.providers.accounts.label team.providers.accounts.last_active_at
team.providers.accounts.link team.providers.accounts.link.label team.providers.accounts.link.provider
team.providers.accounts.linked_usage team.providers.accounts.linked_usage.devices team.providers.accounts.linked_usage.label
team.providers.accounts.linked_usage.provider team.providers.accounts.linked_usage.sessions team.providers.accounts.linked_usage.tokens
team.providers.accounts.linked_usage.tokens.cache_read team.providers.accounts.linked_usage.tokens.cache_write
team.providers.accounts.linked_usage.tokens.input team.providers.accounts.linked_usage.tokens.output
team.providers.accounts.name
team.providers.accounts.per_device team.providers.accounts.per_device.current team.providers.accounts.per_device.device
team.providers.accounts.per_device.device_id team.providers.accounts.per_device.last_active_at team.providers.accounts.per_device.sessions
team.providers.accounts.per_device.tokens team.providers.accounts.per_device.tokens.cache_read team.providers.accounts.per_device.tokens.cache_write
team.providers.accounts.per_device.tokens.input team.providers.accounts.per_device.tokens.output
team.providers.accounts.per_device.usage team.providers.accounts.per_device.usage.30d team.providers.accounts.per_device.usage.7d
team.providers.accounts.per_device.usage.90d team.providers.accounts.per_device.usage.today
team.providers.accounts.plan team.providers.accounts.quota
team.providers.accounts.quota.age_seconds team.providers.accounts.quota.device team.providers.accounts.quota.from team.providers.accounts.quota.observed_at
team.providers.accounts.quota.stale team.providers.accounts.quota.windows
team.providers.accounts.quota.windows.forecast team.providers.accounts.quota.windows.forecast.elapsed
team.providers.accounts.quota.windows.forecast.percent team.providers.accounts.quota.windows.forecast.runs_out_at
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
