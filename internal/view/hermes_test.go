package view

import (
	"reflect"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

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
	if h.Quota == nil || h.Quota.From != "codex" || len(h.Quota.Windows) != 1 || h.Quota.Windows[0].Percent != 60 {
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
	// In the matrix, Hermes counts under the login it spends through.
	if c := r.Team.Matrix.Columns[column(t, r.Team.Matrix, "bob")]; c.Usage.Today != 210 {
		t.Fatalf("bob's column = %+v", c)
	}
}

// A borrowed reading that matches two accounts names neither. What Hermes
// spent through each is on the wire, so it still goes to the right one, in
// the matrix too.
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
	if cols := r.Team.Matrix.Columns; len(cols) != 2 || cols[0].Label != "bob" || cols[0].Usage.Today != 150+60 || cols[1].Usage.Today != 15 {
		t.Fatalf("columns = %+v", cols)
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
	if cols := r.Team.Matrix.Columns; len(cols) != 1 || cols[0].Usage.Today != 150+45 {
		t.Fatalf("columns = %+v", cols)
	}
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

// LAST counts what Hermes spent through a login: a login bot containers use
// may see nothing else.
func TestLastActivityThroughHermes(t *testing.T) {
	f := newFixture(t, emptyState())
	bot := emptyState()
	addAccount(bot, "codex", "lee@corp.test", true, codexQuota(now.Add(-time.Hour), 30), 0)
	addAccount(bot, "codex", "sam@mail.test", false, nil, 0)
	spend(bot, "codex", "sam@mail.test", "/w", 10, now.Add(-3*24*time.Hour))
	addHermes(bot, "openai-codex", "codex", "lee@corp.test", 3_000_000)
	// One session through sam, older than the one through lee.
	bot.Sessions[state.Key("hermes", "sam")] = &state.Session{
		Provider: "hermes", Project: "/work/sam", Updated: now.Add(-2 * time.Hour),
		By:  map[string]snapshot.Tokens{"openai-codex": {Input: 20}},
		Via: map[string]snapshot.Tokens{state.Key("codex", "sam@mail.test"): {Input: 20}},
	}
	r := withTeam(t, f, otherDoc(t, f.key, "d-bot-device", "srv1", now, bot))

	// Hermes' newest activity on the device, through whichever login.
	for _, label := range []string{"lee@corp.test", "sam@mail.test"} {
		if a := findTeamAccount(t, r, "codex", label); a.LastActiveAt == nil || !a.LastActiveAt.Equal(now.Add(-time.Hour)) {
			t.Fatalf("%s last active = %v", label, a.LastActiveAt)
		}
	}
	if d := findTeamAccount(t, r, "codex", "sam@mail.test").PerDevice[0]; d.LastActiveAt == nil || !d.LastActiveAt.Equal(now.Add(-3*24*time.Hour)) {
		t.Fatalf("sam's own last active = %v", d.LastActiveAt)
	}
}
