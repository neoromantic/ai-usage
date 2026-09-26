package view

import (
	"reflect"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/state"
)

// Users counts the devices that spent on the account since its main window
// began, Hermes through it included, and names the busiest.
func TestUsersSinceTheWindowBegan(t *testing.T) {
	start := now.Add(-3 * 24 * time.Hour)
	st := emptyState()
	addAccount(st, "codex", "bots", true, weekQuota(now, 40), 0)
	spend(st, "codex", "bots", "/w", 50, now.Add(-2*time.Hour))
	f := newFixture(t, st)

	busy := emptyState()
	addAccount(busy, "codex", "bots", true, weekQuota(now.Add(-time.Hour), 40), 0)
	addHermes(busy, "openai-codex", "codex", "bots", 0)
	spendVia(busy, "openai-codex", "/h", "codex", "bots", 300, now.Add(-5*time.Hour))
	// Before the window began only.
	before := emptyState()
	addAccount(before, "codex", "bots", true, nil, 0)
	spend(before, "codex", "bots", "/w", 1000, start.Add(-24*time.Hour))
	// Silent since before the window began.
	gone := emptyState()
	addAccount(gone, "codex", "bots", true, nil, 0)
	spend(gone, "codex", "bots", "/w", 1000, start.Add(-2*time.Hour))

	r := withTeam(t, f,
		otherDoc(t, f.key, "d-busy-device", "build", now, busy),
		otherDoc(t, f.key, "d-before-device", "early", now, before),
		otherDoc(t, f.key, "d-gone-device", "gone", start.Add(-time.Hour), gone),
	)
	bots := findTeamAccount(t, r, "codex", "bots")
	if bots.Users != 2 || bots.Busiest == nil || *bots.Busiest != "build" {
		t.Fatalf("users = %d busiest %v", bots.Users, bots.Busiest)
	}

	// With no reading, users are the devices of the last 7 days.
	st2 := emptyState()
	addAccount(st2, "claude", "lee", false, nil, 0)
	spend(st2, "claude", "lee", "/w", 5, now.Add(-6*24*time.Hour))
	f2 := newFixture(t, st2)
	old := emptyState()
	addAccount(old, "claude", "lee", false, nil, 0)
	spend(old, "claude", "lee", "/w", 50, now.Add(-8*24*time.Hour))
	r2 := withTeam(t, f2, otherDoc(t, f2.key, "d-old-device", "oldbox", now, old))
	if s := findTeamAccount(t, r2, "claude", "lee"); s.Users != 1 || s.Busiest == nil || *s.Busiest != "thisbox" {
		t.Fatalf("lee users = %d busiest %v", s.Users, s.Busiest)
	}
}

func TestMatrix(t *testing.T) {
	start := now.Add(-3 * 24 * time.Hour)
	st := emptyState()
	addAccount(st, "claude", "ann@a.io", true, weekQuota(now, 60), 0)
	addAccount(st, "codex", "bots@a.io", true, weekQuota(now, 40), 0)
	spend(st, "claude", "ann@a.io", "/w", 3_000_000, now.Add(-time.Hour))
	spend(st, "codex", "bots@a.io", "/w", 1_000_000, now.Add(-time.Hour), start.Add(-12*time.Hour))
	f := newFixture(t, st)

	build := emptyState()
	addAccount(build, "codex", "bots@a.io", true, weekQuota(now, 40), 0)
	addHermes(build, "openai-codex", "codex", "bots@a.io", 0)
	spendVia(build, "openai-codex", "/h", "codex", "bots@a.io", 3_000_000, now.Add(-2*time.Hour))
	// A Hermes key with no subscription.
	build.Accounts[state.Key("hermes", "openrouter")] = &state.Account{Provider: "hermes", Label: "openrouter", LastSeenAt: now}
	spend(build, "hermes", "openrouter", "/h", 500_000, now.Add(-3*time.Hour))

	r := withTeam(t, f, otherDoc(t, f.key, "d-build-device", "build", now, build))
	mx := r.Team.Matrix
	var cols []string
	for _, c := range mx.Columns {
		cols = append(cols, c.Provider+":"+c.Name)
	}
	if want := []string{"claude:ann", "codex:bots", "hermes:hermes"}; !reflect.DeepEqual(cols, want) {
		t.Fatalf("columns = %v", cols)
	}
	if c := mx.Columns[2]; !c.NoQuota || c.Label != "" || c.Usage.Week != 500_000 || c.Percent != nil {
		t.Fatalf("no quota column = %+v", c)
	}
	if c := mx.Columns[1]; c.Percent == nil || *c.Percent != 40 || c.State != StateTight || c.Usage.Week != 5_000_000 || c.WindowTokens != 4_000_000 {
		t.Fatalf("codex column = %+v", c)
	}
	// Rows by tokens in 7 days: build spent 3.5M, this device 5M.
	if len(mx.Rows) != 2 || mx.Rows[0].Device != "thisbox" || mx.Rows[1].Device != "build" {
		t.Fatalf("rows = %+v", mx.Rows)
	}
	this, other := mx.Rows[0], mx.Rows[1]
	if this.Usage.Week != 5_000_000 || other.Usage.Week != 3_500_000 {
		t.Fatalf("row usage = %+v %+v", this.Usage, other.Usage)
	}
	// Hermes through the bots login counts in the bots column: 3M of the
	// team's 5M in 7 days.
	if c := other.Cells[1]; c.Usage.Week != 3_000_000 || c.WindowTokens != 3_000_000 || !near(c.Share.Week, 60) || !near(c.Share.Today, 75) {
		t.Fatalf("build codex cell = %+v", c)
	}
	// This device spent 2M in 7 days, 1M of them since the window began.
	if c := this.Cells[1]; c.WindowTokens != 1_000_000 || !near(c.Share.Week, 40) || !near(c.Share.Today, 25) || c.Usage.Month != 2_000_000 {
		t.Fatalf("this codex cell = %+v", c)
	}
	if c := this.Cells[0]; !near(c.Share.Week, 100) {
		t.Fatalf("this claude cell = %+v", c)
	}
	if c := other.Cells[0]; !near(c.Share.Week, 0) {
		t.Fatalf("build claude cell = %+v", c)
	}
	if c := other.Cells[2]; c.Usage.Week != 500_000 || !near(c.Share.Week, 100) {
		t.Fatalf("no quota cell = %+v", c)
	}
	// Of the team's 8.5M in 7 days, this device spent 5M.
	if !near(this.Share.Week, 5/8.5*100) || !near(other.Share.Week, 3.5/8.5*100) {
		t.Fatalf("row shares = %+v %+v", this.Share, other.Share)
	}
	if bots := findTeamAccount(t, r, "codex", "bots@a.io"); bots.Users != 2 || *bots.Busiest != "build" {
		t.Fatalf("bots users = %d %v", bots.Users, bots.Busiest)
	}
	if h := findTeamAccount(t, r, "hermes", "openrouter"); h.Subscription {
		t.Fatalf("hermes is a subscription: %+v", h)
	}
}

// In the matrix and in USERS too, each login counts what went through it.
// The snapshot does not split Hermes' days by login, so they go by the
// same proportion.
func TestMatrixSplitsHermesByLogin(t *testing.T) {
	f := newFixture(t, emptyState())
	other := emptyState()
	addAccount(other, "codex", "bots@acme.dev", false, weekQuota(now, 40), 0)
	addAccount(other, "codex", "sam@mail.test", true, weekQuota(now, 10), 0)
	addHermes(other, "openai-codex", "codex", "bots@acme.dev", 0)
	spendVia(other, "openai-codex", "/bots", "codex", "bots@acme.dev", 4_000_000, now.Add(-2*time.Hour))
	spendVia(other, "openai-codex", "/sam", "codex", "sam@mail.test", 1_000_000, now.Add(-3*time.Hour))
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))

	if h := findTeamAccount(t, r, "hermes", "openai-codex"); h.Link == nil || h.Link.Label != "bots@acme.dev" {
		t.Fatalf("link = %+v", h.Link)
	}
	mx := r.Team.Matrix
	if len(mx.Rows) != 2 || mx.Rows[0].Device != "otherbox" || mx.Rows[0].Usage.Week != 5_000_000 {
		t.Fatalf("rows = %+v", mx.Rows)
	}
	for label, want := range map[string]struct {
		tokens int64
		share  float64
	}{"bots@acme.dev": {4_000_000, 100}, "sam@mail.test": {1_000_000, 100}} {
		col := column(t, mx, label)
		if c := mx.Columns[col]; c.Usage.Week != want.tokens || c.WindowTokens != want.tokens {
			t.Fatalf("%s column = %+v", label, c)
		}
		if c := mx.Rows[0].Cells[col]; c.Usage.Week != want.tokens || !near(c.Share.Week, want.share) {
			t.Fatalf("%s cell = %+v", label, c)
		}
	}
	if sam := findTeamAccount(t, r, "codex", "sam@mail.test"); sam.Users != 1 || sam.Busiest == nil || *sam.Busiest != "otherbox" {
		t.Fatalf("sam users = %d %v", sam.Users, sam.Busiest)
	}
	if got := shares(10, []int64{1, 1, 1}); !reflect.DeepEqual(got, []int64{3, 4, 3}) {
		t.Fatalf("shares = %v", got)
	}
}
