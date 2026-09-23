package view

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

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

// Another device's link is not on the wire: it is the one account of the
// linked provider on that doc with the reading the Hermes account borrowed.
func TestTeamLinkFromTheWire(t *testing.T) {
	f := newFixture(t, emptyState())
	other := emptyState()
	addAccount(other, "codex", "bob", true, codexQuota(now.Add(-time.Hour), 60), 100)
	addAccount(other, "codex", "carl", false, codexQuota(now.Add(-2*time.Hour), 60), 10)
	addHermes(other, "openai-codex", "codex", "bob", 40)
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))

	h := findTeamAccount(t, r, "hermes", "openai-codex")
	if h.Link == nil || *h.Link != (Link{"codex", "bob"}) {
		t.Fatalf("link = %+v", h.Link)
	}
	if h.Quota == nil || h.Quota.From != "codex" || h.HeadlinePercent == nil || *h.HeadlinePercent != 60 {
		t.Fatalf("hermes quota = %+v", h.Quota)
	}
	bob := findTeamAccount(t, r, "codex", "bob")
	want := []LinkedUsage{{Provider: "hermes", Label: "openai-codex", Devices: []string{"otherbox (kim)"}, Sessions: 1, Tokens: snapshot.Tokens{Input: 40, Output: 20}}}
	if !reflect.DeepEqual(bob.LinkedUsage, want) {
		t.Fatalf("linked usage = %+v", bob.LinkedUsage)
	}
	if bob.Tokens != (snapshot.Tokens{Input: 100, Output: 50}) {
		t.Fatalf("hermes tokens added to codex: %+v", bob.Tokens)
	}
	if carl := findTeamAccount(t, r, "codex", "carl"); len(carl.LinkedUsage) != 0 || carl.LinkedUsage == nil {
		t.Fatalf("carl linked usage = %#v", carl.LinkedUsage)
	}

	out := text(r)
	hasLine(t, out, "└", "hermes openai-codex", "otherbox", "assumed")
	hasLine(t, out, "└", "quota of codex bob", "assumed")
}

// A borrowed reading that matches two accounts names neither. What Hermes
// spent through each is on the wire, so it still goes to the right one.
func TestTeamLinkThatMatchesTwoAccountsHasNoLabel(t *testing.T) {
	f := newFixture(t, emptyState())
	other := emptyState()
	q := codexQuota(now.Add(-time.Hour), 60)
	addAccount(other, "codex", "bob", true, q, 100)
	addAccount(other, "codex", "carl", false, q, 10)
	addHermes(other, "openai-codex", "codex", "bob", 40)
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))

	h := findTeamAccount(t, r, "hermes", "openai-codex")
	if h.Link == nil || *h.Link != (Link{"codex", ""}) || h.Quota == nil || h.Quota.From != "codex" {
		t.Fatalf("link = %+v, quota = %+v", h.Link, h.Quota)
	}
	if bob := findTeamAccount(t, r, "codex", "bob"); len(bob.LinkedUsage) != 1 || bob.LinkedUsage[0].Tokens != (snapshot.Tokens{Input: 40, Output: 20}) {
		t.Fatalf("bob linked usage = %+v", bob.LinkedUsage)
	}
	if carl := findTeamAccount(t, r, "codex", "carl"); len(carl.LinkedUsage) != 0 {
		t.Fatalf("carl linked usage = %+v", carl.LinkedUsage)
	}
	out := text(r)
	hasLine(t, out, "└", "quota of codex", "assumed")
	if strings.Contains(out, "quota of codex bob") || strings.Contains(out, "quota of codex carl") {
		t.Fatalf("the borrowed reading names an account:\n%s", out)
	}
}

// This device knows its link even when the linked account has no reading.
func TestTeamLinkOfThisDevice(t *testing.T) {
	st := emptyState()
	addAccount(st, "codex", "ann", true, nil, 100)
	addHermes(st, "openai-codex", "codex", "ann", 30)
	f := newFixture(t, st)
	r := withTeam(t, f)

	local := findAccount(t, r, "hermes", "openai-codex")
	if local.Link == nil || *local.Link != (Link{"codex", "ann"}) || local.Quota != nil {
		t.Fatalf("local hermes = %+v", local)
	}
	ann := findAccount(t, r, "codex", "ann")
	if want := []LinkedUsage{{Provider: "hermes", Sessions: 1, Tokens: snapshot.Tokens{Input: 30, Output: 15}}}; !reflect.DeepEqual(ann.LinkedUsage, want) {
		t.Fatalf("local linked usage = %+v", ann.LinkedUsage)
	}
	h := findTeamAccount(t, r, "hermes", "openai-codex")
	if h.Link == nil || *h.Link != (Link{"codex", "ann"}) {
		t.Fatalf("team link = %+v", h.Link)
	}
	if got := findTeamAccount(t, r, "codex", "ann").LinkedUsage; len(got) != 1 || got[0].Label != "openai-codex" || got[0].Sessions != 1 {
		t.Fatalf("team linked usage = %+v", got)
	}
	out := text(r)
	hasLine(t, out, "└", "quota of codex ann", "no reading yet")
	hasLine(t, out, "hermes ○ openai-codex", "via codex")
}

func TestTeamLinkedUsageAddsUpAcrossDevices(t *testing.T) {
	f := newFixture(t, emptyState())
	var docs []snapshot.Doc
	for i, host := range []string{"abox", "bbox"} {
		st := emptyState()
		addAccount(st, "codex", "bob", true, codexQuota(now.Add(-time.Duration(i+1)*time.Hour), 50), 100)
		addHermes(st, "openai-codex", "codex", "bob", int64(10*(i+1)))
		docs = append(docs, otherDoc(t, f.key, "d-"+host+"-device", host, now, st))
	}
	r := withTeam(t, f, docs...)
	got := findTeamAccount(t, r, "codex", "bob").LinkedUsage
	want := []LinkedUsage{{Provider: "hermes", Label: "openai-codex", Devices: []string{"abox (kim)", "bbox (kim)"}, Sessions: 2, Tokens: snapshot.Tokens{Input: 30, Output: 15}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("linked usage = %+v", got)
	}
}

// A device whose Hermes homes bill one route through two logins credits
// each with what went through it, not with all of Hermes' tokens.
func TestTeamLinkedUsageSplitsByLogin(t *testing.T) {
	f := newFixture(t, emptyState())
	other := emptyState()
	addAccount(other, "codex", "bots", false, codexQuota(now.Add(-time.Hour), 40), 0)
	addAccount(other, "codex", "sam", true, codexQuota(now.Add(-2*time.Hour), 5), 0)
	addHermes(other, "openai-codex", "codex", "bots", 400)
	other.Sessions[state.Key("hermes", "own")] = &state.Session{
		Provider: "hermes", Project: "/work/own", Updated: now.Add(-time.Hour),
		By:  map[string]snapshot.Tokens{"openai-codex": {Input: 20}},
		Via: map[string]snapshot.Tokens{state.Key("codex", "sam"): {Input: 20}},
	}
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))
	for label, want := range map[string]snapshot.Tokens{"bots": {Input: 400, Output: 200}, "sam": {Input: 20}} {
		got := findTeamAccount(t, r, "codex", label).LinkedUsage
		if len(got) != 1 || got[0].Tokens != want || got[0].Sessions != 1 {
			t.Fatalf("%s linked usage = %+v", label, got)
		}
	}
}

func TestTeamPlanIsTheNewestAndPerDeviceIsByTokens(t *testing.T) {
	f := newFixture(t, emptyState())
	var docs []snapshot.Doc
	for _, d := range []struct {
		host   string
		at     time.Duration
		plan   string
		tokens int64
	}{{"cbox", -3 * time.Hour, "plus", 30}, {"abox", -2 * time.Hour, "pro", 10}, {"bbox", -time.Hour, "", 30}} {
		st := emptyState()
		addAccount(st, "codex", "bob", true, nil, d.tokens)
		st.Accounts[state.Key("codex", "bob")].Plan = d.plan
		docs = append(docs, otherDoc(t, f.key, "d-"+d.host+"-device", d.host, now.Add(d.at), st))
	}
	r := withTeam(t, f, docs...)
	bob := findTeamAccount(t, r, "codex", "bob")
	if bob.Plan == nil || *bob.Plan != "pro" {
		t.Fatalf("plan = %v", bob.Plan)
	}
	var order []string
	for _, d := range bob.PerDevice {
		order = append(order, d.Device)
	}
	if want := []string{"bbox (kim)", "cbox (kim)", "abox (kim)"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("per device = %v", order)
	}
	if d := bob.PerDevice[0]; d.DeviceID != "d-bbox-device" || !d.Current || d.Sessions != 1 || d.Tokens.Input != 30 || d.LastActiveAt == nil {
		t.Fatalf("per device entry = %+v", d)
	}
}

// A harness's own error starts with its name, so the views do not name the
// provider a second time.
func TestSourceErrorNamesTheProviderOnce(t *testing.T) {
	st := emptyState()
	st.Sources["codex"] = state.Source{Status: "partial", Error: "codex: not logged in"}
	f := newFixture(t, st)
	other := emptyState()
	other.Sources["codex"] = state.Source{Status: "partial", Error: "codex: not logged in"}
	other.Sources["claude"] = state.Source{Status: "partial", Error: "2 malformed lines"}
	out := text(withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other)))
	for _, want := range []string{"codex partial: not logged in", "codex: not logged in", "claude: 2 malformed lines"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "codex: codex:") || strings.Contains(out, "partial: codex:") {
		t.Errorf("provider named twice:\n%s", out)
	}
}
