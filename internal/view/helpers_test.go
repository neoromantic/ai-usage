package view

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
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

func emptyState() *state.State {
	return &state.State{
		Sources:  map[string]state.Source{},
		Current:  map[string]string{},
		Accounts: map[string]*state.Account{},
		Sessions: map[string]*state.Session{},
	}
}

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

type fixture struct {
	key *team.Key
	in  Input
}

func newFixture(t *testing.T, st *state.State) *fixture {
	t.Helper()
	key := mustKey(t)
	cfg := state.Config{Device: "d-this-device"}
	st.LastRunAt, st.LastSuccessAt = now, now
	for _, p := range collect.Providers {
		if _, ok := st.Sources[p]; !ok {
			st.Sources[p] = state.Source{Status: "ok", Homes: []string{"/home/." + p}}
		}
	}
	doc := collect.BuildDoc(st, key, state.Config{Device: cfg.Device}, "thisbox", "sam", "v1.2.3", now)
	return &fixture{key: key, in: Input{
		Version: "v1.2.3", RelayURL: "https://relay.example", Config: cfg, State: st, Key: key,
		Doc: doc, Hostname: "thisbox", OSUser: "sam", Now: now,
	}}
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

// otherDoc is another device's published snapshot, decoded as a pull would.
func otherDoc(t *testing.T, key *team.Key, device, host string, at time.Time, st *state.State) snapshot.Doc {
	t.Helper()
	body, err := json.Marshal(collect.BuildDoc(st, key, state.Config{Device: device}, host, "kim", "v1.2.0", at))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := snapshot.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	return doc
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

// olderDoc is another device's snapshot as a collector older than v0.2.0
// sends it: each account's tokens over 90 days, but no days, and no tokens
// since a window began.
func olderDoc(t *testing.T, key *team.Key, device, host string, at time.Time, st *state.State) snapshot.Doc {
	t.Helper()
	doc := collect.BuildDoc(st, key, state.Config{Device: device}, host, "dana", "v0.1.4", at)
	for i := range doc.Accounts {
		doc.Accounts[i].Days, doc.Accounts[i].Recent = nil, nil
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if doc, err = snapshot.Decode(body); err != nil {
		t.Fatal(err)
	}
	return doc
}

// testKey is a team key with a fixed seed, so a page that shows the team is
// the same on every run.
const testKey = "aiu-team-1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

// olderTeam is a team with a device on a collector older than v0.2.0:
// annbook, this device, and srv1 on v0.2.0, and MacBook-Old on v0.1.4. The
// old device spent on ann 2 hours ago and on kim, which no other device
// uses, 5 hours ago; on lee, last 40 days ago.
func olderTeam(t *testing.T) Report {
	t.Helper()
	key, err := team.Import(testKey)
	if err != nil {
		t.Fatal(err)
	}
	// Every window began 3 days ago.
	start := now.Add(-3 * 24 * time.Hour)
	q := func(at time.Time, pct float64) *state.Quota {
		return &state.Quota{At: at, Source: "harness", Windows: []snapshot.Window{week7(pct, start.Add(week))}}
	}
	homes := func(st *state.State, home string) {
		for _, p := range collect.Providers {
			st.Sources[p] = state.Source{Status: "ok", Homes: []string{home + "/." + p}}
		}
	}

	st := emptyState()
	homes(st, "/Users/ann")
	addAccount(st, "claude", "ann@acme.dev", true, q(now, 60), 0)
	spend(st, "claude", "ann@acme.dev", "/Users/ann/src/app", 4_000_000, now.Add(-time.Hour), now.Add(-2*24*time.Hour), now.Add(-20*24*time.Hour))
	st.LastRunAt, st.LastSuccessAt = now.Add(-7*time.Minute), now.Add(-7*time.Minute)
	st.Relay.LastPushAt, st.Relay.LastPullAt = now.Add(-7*time.Minute), now.Add(-7*time.Minute)
	st.Update.CheckedAt, st.Update.Latest = now.Add(-time.Hour), "v0.2.0"
	st.Schedule.Registered = true
	cfg := state.Config{Device: "d-annbook"}

	srv := emptyState()
	homes(srv, "/root")
	addAccount(srv, "codex", "lee@corp.test", true, q(now.Add(-10*time.Minute), 40), 0)
	spend(srv, "codex", "lee@corp.test", "/srv/bots", 30_000_000, now.Add(-2*time.Hour), now.Add(-4*24*time.Hour), now.Add(-50*24*time.Hour))
	srvDoc := collect.BuildDoc(srv, key, state.Config{Device: "d-srv1"}, "srv1", "root", "v0.2.0", now.Add(-10*time.Minute))

	old := emptyState()
	homes(old, "/Users/dana")
	addAccount(old, "claude", "ann@acme.dev", false, nil, 0)
	spend(old, "claude", "ann@acme.dev", "/Users/dana/src/app", 100_000_000, now.Add(-2*time.Hour), now.Add(-9*24*time.Hour), now.Add(-35*24*time.Hour), now.Add(-80*24*time.Hour))
	addAccount(old, "claude", "kim@corp.test", true, q(now.Add(-20*time.Minute), 30), 0)
	spend(old, "claude", "kim@corp.test", "/Users/dana/notes", 50_000_000, now.Add(-5*time.Hour), now.Add(-10*24*time.Hour))
	addAccount(old, "codex", "lee@corp.test", false, nil, 0)
	spend(old, "codex", "lee@corp.test", "/Users/dana/src/app", 60_000_000, now.Add(-40*24*time.Hour), now.Add(-60*24*time.Hour))
	oldDoc := olderDoc(t, key, "d-macbook-old", "MacBook-Old", now.Add(-20*time.Minute), old)

	return Build(Input{
		Version: "v0.2.0", RelayURL: "https://relay.example", Config: cfg, State: st, Key: key,
		Doc:      collect.BuildDoc(st, key, cfg, "annbook", "ann", "v0.2.0", now.Add(-7*time.Minute)),
		Team:     collect.TeamCache{PulledAt: now.Add(-7 * time.Minute), Team: key.Fingerprint(), Docs: []snapshot.Doc{srvDoc, oldDoc}},
		Hostname: "annbook", OSUser: "ann", Now: now,
	})
}
