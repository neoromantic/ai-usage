package view

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

var now = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func mustKey(t *testing.T) *team.Key {
	t.Helper()
	k, err := team.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func tp(t time.Time) *time.Time { return &t }

func win(name string, pct float64, reset time.Time) snapshot.Window {
	return snapshot.Window{Name: name, Percent: pct, ResetsAt: &reset}
}

func emptyState() *state.State { return state.NewState() }

// addAccount puts an account with an optional quota and one session in st.
func addAccount(st *state.State, provider, label string, current bool, q *state.Quota, tokens int64) {
	st.Accounts[state.Key(provider, label)] = &state.Account{Provider: provider, Label: label, Quota: q, LastSeenAt: now}
	if current {
		st.Current[state.Key(provider, "/home/."+provider)] = label
	}
	if tokens > 0 {
		st.Sessions[state.Key(provider, label+"-s")] = &state.Session{
			Provider: provider, Project: "/work/" + label, Updated: now.Add(-time.Hour),
			By: map[string]snapshot.Tokens{label: {Input: tokens, Output: tokens / 2}},
		}
	}
}

// spend adds a session of label's that spent n input tokens in each of the
// given hours.
func spend(st *state.State, provider, label, project string, n int64, at ...time.Time) {
	s := &state.Session{Provider: provider, Project: project, By: map[string]snapshot.Tokens{label: {Input: n * int64(len(at))}}, Hours: map[int64]int64{}}
	for _, t := range at {
		s.Hours[logs.HourOf(t)] += n
		if t.After(s.Updated) {
			s.Updated = t
		}
	}
	st.Sessions[state.Key(provider, label, project, at[0].String())] = s
}

// spendVia adds a session of the Hermes account label's that spent n input
// tokens at at, all of them through provider's login to.
func spendVia(st *state.State, label, project, provider, to string, n int64, at time.Time) {
	spend(st, "hermes", label, project, n, at)
	st.Sessions[state.Key("hermes", label, project, at.String())].Via = map[string]snapshot.Tokens{state.Key(provider, to): {Input: n}}
}

type fixture struct {
	key *team.Key
	in  Input
}

func newFixture(t *testing.T, st *state.State) *fixture {
	t.Helper()
	key := mustKey(t)
	cfg := state.Config{Device: "d-this-device"}
	st.LastRunAt, st.LastSuccessAt = now, now
	for _, p := range snapshot.Providers {
		if _, ok := st.Sources[p]; !ok {
			st.Sources[p] = state.Source{Status: "ok", Homes: []string{"/home/." + p}}
		}
	}
	f := &fixture{key: key, in: Input{
		Version: "v1.2.3", RelayURL: "https://relay.example", Config: cfg, State: st, Key: key,
		Hostname: "thisbox", OSUser: "sam", Now: now,
	}}
	f.seal(now)
	return f
}

// seal makes f's snapshot of this device from its state and config, as a
// run at at would.
func (f *fixture) seal(at time.Time) {
	f.in.Doc = collect.BuildDoc(f.in.State, f.key, f.in.Config, "thisbox", "sam", "v1.2.3", at)
}

func findAccount(t *testing.T, r Report, provider, label string) Account {
	t.Helper()
	for _, p := range r.Providers {
		if p.Provider != provider {
			continue
		}
		for _, a := range p.Accounts {
			if a.Label == label {
				return a
			}
		}
	}
	t.Fatalf("no %s account %q", provider, label)
	return Account{}
}

// docWith is another device's snapshot built with its own config.
func docWith(t *testing.T, key *team.Key, cfg state.Config, host string, at time.Time, st *state.State) snapshot.Doc {
	t.Helper()
	body, err := json.Marshal(collect.BuildDoc(st, key, cfg, host, "kim", "v1.2.0", at))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := snapshot.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// otherDoc is another device's published snapshot, decoded as a pull would.
func otherDoc(t *testing.T, key *team.Key, device, host string, at time.Time, st *state.State) snapshot.Doc {
	t.Helper()
	return docWith(t, key, state.Config{Device: device}, host, at, st)
}

// addHermes adds a Hermes account that bills through provider/to, with one
// session spent through it.
func addHermes(st *state.State, label, provider, to string, tokens int64) {
	st.Accounts[state.Key("hermes", label)] = &state.Account{Provider: "hermes", Label: label, LastSeenAt: now, Link: &state.Link{Provider: provider, Label: to}}
	tok := snapshot.Tokens{Input: tokens, Output: tokens / 2}
	st.Sessions[state.Key("hermes", label+"-"+to)] = &state.Session{
		Provider: "hermes", Project: "/work/hermes", Updated: now.Add(-time.Hour),
		By:  map[string]snapshot.Tokens{label: tok},
		Via: map[string]snapshot.Tokens{state.Key(provider, to): tok},
	}
}

func codexQuota(at time.Time, pct float64) *state.Quota {
	return &state.Quota{At: at, Source: "harness", Windows: []snapshot.Window{win("7d", pct, now.Add(48*time.Hour))}}
}

// weekQuota is a harness reading, taken at at, of a weekly window begun 3
// days before now and pct full.
func weekQuota(at time.Time, pct float64) *state.Quota {
	return &state.Quota{At: at, Source: "harness", Windows: []snapshot.Window{week7(pct, now.Add(-3*24*time.Hour).Add(week))}}
}

func findTeamAccount(t *testing.T, r Report, provider, label string) TeamAccount {
	t.Helper()
	for _, p := range r.Team.Providers {
		for _, a := range p.Accounts {
			if p.Provider == provider && a.Label == label {
				return a
			}
		}
	}
	t.Fatalf("no team %s account %q in %+v", provider, label, r.Team.Providers)
	return TeamAccount{}
}

func withTeam(t *testing.T, f *fixture, docs ...snapshot.Doc) Report {
	t.Helper()
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: docs}
	return Build(f.in)
}

// attentionList is r's ATTENTION, one "kind devices message" line per item.
func attentionList(r Report) []string {
	var got []string
	for _, a := range r.Attention {
		got = append(got, a.Kind+" "+strings.Join(a.Devices, ",")+" "+a.Message)
	}
	return got
}

// column is the matrix column of the subscription with label.
func column(t *testing.T, mx Matrix, label string) int {
	t.Helper()
	for i, c := range mx.Columns {
		if c.Label == label {
			return i
		}
	}
	t.Fatalf("no column %q", label)
	return -1
}

// near says a share is not nil and about want.
func near(v *float64, want float64) bool {
	return v != nil && math.Abs(*v-want) < 1e-9
}
