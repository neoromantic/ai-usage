package collect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

func TestFirstRunAttributesHistoryToCurrentAccount(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.sessions("claude", h,
		sess("s1", "/work/api", 1000, t0.Add(-48*time.Hour)),
		sess("s2", "/work/web", 300, t0.Add(-time.Hour)),
	)
	w.login("claude", h, "ann@example.com", quota(t0.Add(-time.Minute), 40, 80))

	res := run(t, o)
	a := totalsFor(t, res.State, "claude", "ann@example.com")
	if !a.Current || a.Sessions != 2 || a.Tokens != tok(1000).Add(tok(300)) {
		t.Fatalf("totals = %+v", a)
	}
	if a.Quota == nil || len(a.Quota.Windows) != 2 || a.Quota.Windows[1].Percent != 80 {
		t.Fatalf("quota = %+v", a.Quota)
	}
	if len(a.Projects) != 2 || a.Projects[0].Path != "/work/api" {
		t.Fatalf("projects = %+v", a.Projects)
	}
	if !a.LastActive.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("last active = %v", a.LastActive)
	}
	if got := growthOf(lastSample(t, o), "claude", "ann@example.com"); got != tok(1300) {
		t.Fatalf("first sample growth = %+v", got)
	}
	src := res.State.Sources["claude"]
	if src.Status != "ok" || src.Error != "" || !reflect.DeepEqual(src.Homes, []string{h}) {
		t.Fatalf("source = %+v", src)
	}
	if !res.State.LastSuccessAt.Equal(t0) || !res.State.LastRunAt.Equal(t0) {
		t.Fatalf("run times = %v %v", res.State.LastRunAt, res.State.LastSuccessAt)
	}
	for _, k := range w.asked {
		if strings.HasPrefix(k, "hermes") {
			t.Fatal("hermes has no probe and must not be asked")
		}
	}
}

func TestSecondRunAttributesOnlyGrowth(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", nil)
	w.sessions("claude", h, sess("s1", "/p", 1000, t0))
	run(t, o)

	w.now = t0.Add(15 * time.Minute)
	w.sessions("claude", h, sess("s1", "/p", 1500, w.now), sess("s2", "/q", 200, w.now))
	res := run(t, o)

	if got := growthOf(lastSample(t, o), "claude", "ann"); got != tok(500).Add(tok(200)) {
		t.Fatalf("second sample growth = %+v", got)
	}
	a := totalsFor(t, res.State, "claude", "ann")
	if a.Tokens != tok(1500).Add(tok(200)) || a.Sessions != 2 {
		t.Fatalf("totals = %+v", a)
	}

	// Nothing new: no growth, totals unchanged.
	w.now = t0.Add(30 * time.Minute)
	res = run(t, o)
	if got := growthOf(lastSample(t, o), "claude", "ann"); !got.Zero() {
		t.Fatalf("idle run growth = %+v", got)
	}
	if a2 := totalsFor(t, res.State, "claude", "ann"); a2.Tokens != a.Tokens {
		t.Fatalf("idle run changed totals: %+v", a2)
	}
}

func TestAccountSwitchKeepsPreviousAccount(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "codex")
	w.login("codex", h, "old@x", quota(t0, 55))
	w.sessions("codex", h, sess("s1", "/p", 1000, t0))
	run(t, o)

	w.now = t0.Add(15 * time.Minute)
	w.login("codex", h, "new@x", quota(w.now, 5))
	w.sessions("codex", h, sess("s1", "/p", 1100, w.now), sess("s2", "/p", 40, w.now))
	res := run(t, o)

	old := totalsFor(t, res.State, "codex", "old@x")
	if old.Current || old.Tokens != tok(1000) {
		t.Fatalf("previous account = %+v", old)
	}
	if old.Quota == nil || old.Quota.Windows[0].Percent != 55 || !old.Quota.At.Equal(t0) {
		t.Fatalf("previous account lost its quota: %+v", old.Quota)
	}
	cur := totalsFor(t, res.State, "codex", "new@x")
	if !cur.Current || cur.Tokens != tok(100).Add(tok(40)) || cur.Quota.Windows[0].Percent != 5 {
		t.Fatalf("new account = %+v", cur)
	}
	// s1 is split between the two accounts and counts as a session for both.
	if old.Sessions != 1 || cur.Sessions != 2 {
		t.Fatalf("sessions old=%d new=%d", old.Sessions, cur.Sessions)
	}
	if IsCurrent(res.State, "codex", "old@x") {
		t.Fatal("old account still marked current")
	}
}

func TestHarnessThatStopsAnsweringKeepsLastAccountAndQuota(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", quota(t0, 30))
	w.sessions("claude", h, sess("s1", "/p", 100, t0))
	run(t, o)

	w.now = t0.Add(15 * time.Minute)
	w.readings[state.Key("claude", h)] = probe.Reading{}
	w.askErr[state.Key("claude", h)] = errors.New("claude auth status: timed out")
	w.sessions("claude", h, sess("s1", "/p", 160, w.now))
	res := run(t, o)

	a := totalsFor(t, res.State, "claude", "ann")
	if !a.Current || a.Tokens != tok(160) {
		t.Fatalf("totals = %+v", a)
	}
	if a.Quota == nil || a.Quota.Windows[0].Percent != 30 || !a.Quota.At.Equal(t0) {
		t.Fatalf("last good quota lost: %+v", a.Quota)
	}
	if hasTotals(res.State, "claude", UnknownAccount) {
		t.Fatal("growth went to the unknown account")
	}
	src := res.State.Sources["claude"]
	if src.Status != "partial" || !strings.Contains(src.Error, "timed out") {
		t.Fatalf("source = %+v", src)
	}
	if !strings.Contains(res.State.LastError, "timed out") {
		t.Fatalf("last error = %q", res.State.LastError)
	}
	// A probe problem is not a failed read.
	if !res.State.LastSuccessAt.Equal(w.now) {
		t.Fatalf("last success = %v", res.State.LastSuccessAt)
	}
}

func TestFirstNamedAccountClaimsUnknownHistory(t *testing.T) {
	// A codex too old to answer names nobody; once one that answers is
	// found, what was counted meanwhile is its account's, as the first
	// run's history is.
	w, o := newWorld(t)
	h := w.home(t, "codex")
	k := state.Key("codex", h)
	w.askErr[k] = errors.New("codex initialize: app-server exited without answering")
	w.sessions("codex", h, sess("s1", "/p", 1000, t0))
	res := run(t, o)
	// Nobody is known to be logged in, so the unknown account is not current.
	if u := totalsFor(t, res.State, "codex", UnknownAccount); u.Current || u.Tokens != tok(1000) {
		t.Fatalf("unknown = %+v", u)
	}

	w.now = t0.Add(15 * time.Minute)
	delete(w.askErr, k)
	w.login("codex", h, "ops@x", quota(w.now, 20))
	w.sessions("codex", h, sess("s1", "/p", 1200, w.now), sess("s2", "/q", 50, w.now))
	res = run(t, o)
	a := totalsFor(t, res.State, "codex", "ops@x")
	if !a.Current || a.Tokens != tok(1200).Add(tok(50)) || a.Sessions != 2 || !a.LastActive.Equal(w.now) {
		t.Fatalf("claimed = %+v", a)
	}
	if hasTotals(res.State, "codex", UnknownAccount) {
		t.Fatal("unknown account kept its history")
	}

	// Later unknown usage has an account to compare with, and stays unknown.
	w.now = t0.Add(30 * time.Minute)
	w.logout("codex", h)
	w.sessions("codex", h, sess("s1", "/p", 1300, w.now), sess("s2", "/q", 50, w.now))
	run(t, o)
	w.now = t0.Add(45 * time.Minute)
	delete(w.askErr, k)
	w.login("codex", h, "dev@x", nil)
	res = run(t, o)
	if u := totalsFor(t, res.State, "codex", UnknownAccount); u.Tokens != tok(100) {
		t.Fatalf("unknown after a named account = %+v", u)
	}
	if d := totalsFor(t, res.State, "codex", "dev@x"); !d.Tokens.Zero() {
		t.Fatalf("a later account claimed usage from before it: %+v", d)
	}
}

// TestClaimTakesTheUnknownHours: the account that claims unknown usage in a
// session that keeps each account's hours takes the hours it was spent in.
func TestClaimTakesTheUnknownHours(t *testing.T) {
	h := logs.HourOf(t0)
	st := &state.State{Current: map[string]string{}, Accounts: map[string]*state.Account{}, Sessions: map[string]*state.Session{
		state.Key("codex", "s1"): {Provider: "codex", Project: "/p", Updated: t0,
			By:      map[string]snapshot.Tokens{UnknownAccount: tok(100), "bo@acme.dev": tok(50)},
			Hours:   map[int64]int64{h - 5: 110, h: 55},
			ByHours: map[string]map[int64]int64{UnknownAccount: {h - 5: 110}, "bo@acme.dev": {h: 55}}},
	}}
	home := "/home/sam/.codex"
	res := logs.Result{Sessions: []logs.Session{{ID: "s1", Home: home}}, Homes: map[string]logs.HomeRead{home: {}}}
	claimUnknown(st, "codex", []string{home}, []answer{{reading: probe.Reading{Account: "ann@acme.dev"}}}, res, map[string]string{home: "ann@acme.dev"})
	if a := totalsFor(t, st, "codex", "ann@acme.dev"); a.Tokens != tok(100) || !reflect.DeepEqual(a.Hours, map[int64]int64{h - 5: 110}) {
		t.Errorf("ann = %d tokens in %v, want 110 at h-5", a.Tokens.InOut(), a.Hours)
	}
	if hasTotals(st, "codex", UnknownAccount) {
		t.Error("unknown kept its share")
	}
}

func TestEachHomeClaimsItsOwnUnknownHistory(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "codex")
	x := w.extraHome(t, "codex", "work-codex")
	hk, xk := state.Key("codex", h), state.Key("codex", x)
	w.askErr[hk] = errors.New("no answer")
	w.askErr[xk] = errors.New("no answer")
	w.sessions("codex", h, sess("s1", "/p", 1000, t0))
	w.sessions("codex", x, sess("s2", "/q", 500, t0))
	run(t, o)

	// One home answers and the other still does not: only the one that
	// answered claims, and only what was read from it.
	w.now = t0.Add(15 * time.Minute)
	delete(w.askErr, hk)
	w.login("codex", h, "ann@x", nil)
	w.askErr[xk] = errors.New("codex account/rateLimits/read: 401 Unauthorized")
	res := run(t, o)
	if a := totalsFor(t, res.State, "codex", "ann@x"); a.Tokens != tok(1000) {
		t.Fatalf("ann = %+v", a)
	}
	if u := totalsFor(t, res.State, "codex", UnknownAccount); u.Tokens != tok(500) {
		t.Fatalf("unknown = %+v", u)
	}

	// The other home answers later, with another account, and claims its own.
	w.now = t0.Add(30 * time.Minute)
	delete(w.askErr, xk)
	w.login("codex", x, "bob@x", nil)
	res = run(t, o)
	if a, b := totalsFor(t, res.State, "codex", "ann@x"), totalsFor(t, res.State, "codex", "bob@x"); a.Tokens != tok(1000) || b.Tokens != tok(500) {
		t.Fatalf("ann = %+v, bob = %+v", a, b)
	}
	if hasTotals(res.State, "codex", UnknownAccount) {
		t.Fatal("unknown account kept history")
	}
}

func TestHomeClaimsOnceItsLogsAreRead(t *testing.T) {
	// The home names its account first in a run that could not read its
	// logs. The history stays unknown then and is claimed when they read.
	w, o := newWorld(t)
	h := w.home(t, "codex")
	k := state.Key("codex", h)
	w.askErr[k] = errors.New("no answer")
	w.sessions("codex", h, sess("s1", "/p", 1000, t0))
	run(t, o)
	w.now = t0.Add(15 * time.Minute)
	delete(w.askErr, k)
	w.login("codex", h, "ann@x", nil)
	w.readErr[k] = errors.New("permission denied")
	run(t, o)
	w.now = t0.Add(30 * time.Minute)
	delete(w.readErr, k)
	res := run(t, o)
	if a := totalsFor(t, res.State, "codex", "ann@x"); a.Tokens != tok(1000) {
		t.Fatalf("ann = %+v", a)
	}
	if hasTotals(res.State, "codex", UnknownAccount) {
		t.Fatal("unknown account kept history")
	}
}

func TestUnusedHomeWithoutLoginIsNoProblem(t *testing.T) {
	// A tool in a bot's image that nobody uses: Claude Code makes its home
	// when asked who is logged in, and the next run finds it empty.
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.logout("claude", h)
	if got := run(t, o).State.Sources["claude"]; got.Status != "ok" || got.Error != "" {
		t.Fatalf("claude source = %+v", got)
	}
	w.now = t0.Add(15 * time.Minute)
	w.sessions("claude", h, sess("s1", "/p", 100, w.now))
	if got := run(t, o).State.Sources["claude"]; got.Status != "partial" {
		t.Fatalf("a used home logged out: source = %+v", got)
	}
}

// Each home is asked about with when its logs show it was last used, so
// Claude Code reads the usage only for a home in use.
func TestAskTellsWhenTheHomeWasLastUsed(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	idle := w.extraHome(t, "claude", "idle-claude")
	w.sessions("claude", h, sess("s1", "/p", 100, t0.Add(-time.Hour)), sess("s2", "/p", 100, t0.Add(-2*time.Hour)))
	run(t, o)
	if got := w.lastUse[state.Key("claude", h)]; !got.Equal(t0.Add(-time.Hour)) {
		t.Errorf("used home: last use %v", got)
	}
	if got, ok := w.lastUse[state.Key("claude", idle)]; !ok || !got.IsZero() {
		t.Errorf("idle home: last use %v, asked %v", got, ok)
	}
}

// Homes that share their logs through a symlink read the same sessions,
// which go to one of them. Which login ran them is not known, so no home of
// the pair is told it was used, and Claude Code is asked for neither.
func TestHomesSharingClaudeLogsAreNotToldTheirUse(t *testing.T) {
	w, o := newWorld(t)
	personal := w.home(t, "claude")
	work := w.extraHome(t, "claude", "work-claude")
	own := filepath.Join(filepath.Dir(w.userHome), "own-claude")
	editConfig(t, o.Dir, func(cfg *state.Config) { cfg.Homes = map[string][]string{"claude": {own}} })
	mkdirs(t, filepath.Join(personal, "projects"), filepath.Join(own, "projects"))
	if err := os.Symlink(filepath.Join(personal, "projects"), filepath.Join(work, "projects")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	w.sessions("claude", personal, sess("s1", "/p", 100, t0.Add(-5*time.Minute)))
	w.sessions("claude", own, sess("s2", "/p", 100, t0.Add(-time.Hour)))
	run(t, o)
	for _, h := range []string{personal, work} {
		if got, ok := w.lastUse[state.Key("claude", h)]; !ok || !got.IsZero() {
			t.Errorf("%s: last use %v, asked %v", h, got, ok)
		}
	}
	if got := w.lastUse[state.Key("claude", own)]; !got.Equal(t0.Add(-time.Hour)) {
		t.Errorf("home with its own logs: last use %v", got)
	}
}

func TestLoggedOutHomeDoesNotClaimLater(t *testing.T) {
	// A home that said nobody is logged in has answered; the usage counted
	// then is no one's, whoever logs in afterwards.
	w, o := newWorld(t)
	h := w.home(t, "codex")
	k := state.Key("codex", h)
	w.logout("codex", h)
	w.sessions("codex", h, sess("s1", "/p", 1000, t0))
	run(t, o)
	w.now = t0.Add(15 * time.Minute)
	delete(w.askErr, k)
	w.login("codex", h, "ann@x", nil)
	res := run(t, o)
	if u := totalsFor(t, res.State, "codex", UnknownAccount); u.Tokens != tok(1000) {
		t.Fatalf("unknown = %+v", u)
	}
}

func TestClaimOnceAfterTheAccountAgesOut(t *testing.T) {
	// The ledger forgets an idle account after the retention window; the
	// home still answered before, so unknown usage since is not claimed.
	w, o := newWorld(t)
	h := w.home(t, "codex")
	k := state.Key("codex", h)
	w.login("codex", h, "ann@x", nil)
	w.sessions("codex", h, sess("s1", "/p", 500, t0))
	run(t, o)
	w.logout("codex", h)
	for day := 1; day <= 95; day += 2 {
		w.now = t0.Add(time.Duration(day) * 24 * time.Hour)
		w.sessions("codex", h, sess(fmt.Sprintf("u%d", day), "/p", 100, w.now))
		run(t, o)
	}
	delete(w.askErr, k)
	w.login("codex", h, "bea@x", nil)
	w.now = w.now.Add(time.Hour)
	res := run(t, o)
	if b := totalsFor(t, res.State, "codex", "bea@x"); !b.Tokens.Zero() {
		t.Fatalf("a later account claimed usage from before it: %+v", b)
	}
}
