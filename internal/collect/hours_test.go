package collect

import (
	"reflect"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// TestHoursFollowTheSession: in a ledger from before accounts kept their own
// hours, each account's part of a session is spread over the session's hours
// by its share, a session without hours puts it at the hour the share last
// grew, and the days and window counts come from them.
func TestHoursFollowTheSession(t *testing.T) {
	h := func(at time.Time) int64 { return at.Unix() / 3600 }
	day := 24 * time.Hour
	st := &state.State{
		Accounts: map[string]*state.Account{},
		Sessions: map[string]*state.Session{
			// 400 tokens in, 0 out: 3 parts to ann, 1 to bo, over two hours.
			state.Key("codex", "s1"): {Provider: "codex", Project: "/p", Updated: t0,
				By:    map[string]snapshot.Tokens{"ann": {Input: 300}, "bo": {Input: 100}},
				Hours: map[int64]int64{h(t0.Add(-2 * day)): 200, h(t0): 200}},
			// No hours: at the hour ann's share last grew.
			state.Key("claude", "s2"): {Provider: "claude", Project: "/p", Updated: t0,
				By:   map[string]snapshot.Tokens{"ann": {Input: 50, Output: 10, CacheRead: 1000}},
				Last: map[string]time.Time{"ann": t0.Add(-day)}},
		},
	}
	byLabel := map[string]AccountTotals{}
	for _, a := range Totals(st, nil) {
		byLabel[a.Provider+"/"+a.Label] = a
	}
	ann, bo := byLabel["codex/ann"], byLabel["codex/bo"]
	if want := map[int64]int64{h(t0.Add(-2 * day)): 150, h(t0): 150}; !reflect.DeepEqual(ann.Hours, want) {
		t.Errorf("ann's hours = %v, want %v", ann.Hours, want)
	}
	if want := map[int64]int64{h(t0.Add(-2 * day)): 50, h(t0): 50}; !reflect.DeepEqual(bo.Hours, want) {
		t.Errorf("bo's hours = %v, want %v", bo.Hours, want)
	}
	if got, want := DaysOf(ann.Hours, t0), []int64{150, 0, 150}; !reflect.DeepEqual(got, want) {
		t.Errorf("days = %v, want %v", got, want)
	}
	if got := DaysOf(byLabel["claude/ann"].Hours, t0); !reflect.DeepEqual(got, []int64{0, 60}) {
		t.Errorf("claude days = %v, want [0 60]: cache is not counted", got)
	}
	if got := SinceStart(ann.Hours, t0.Add(-day)); got != 150 {
		t.Errorf("since a day ago = %d, want 150", got)
	}
	ps := Projects(st, nil)
	if len(ps) != 1 || ps[0].Sessions != 2 || !reflect.DeepEqual(ps[0].Providers, []string{"claude", "codex"}) || DaysOf(ps[0].Hours, t0)[2] != 200 {
		t.Errorf("projects = %+v", ps)
	}
}

// TestLedgerPlacesHours: a log with times gives the hours of a session read
// for the first time, a later read adds its growth where the log grew, even
// when it is partial, and a log without times has each run's growth at the
// session's last activity.
func TestLedgerPlacesHours(t *testing.T) {
	h := t0.Unix() / 3600
	st := state.NewState()
	growth := map[string]snapshot.Tokens{}
	timed := logs.Session{ID: "s1", Tokens: snapshot.Tokens{Input: 90, Output: 10}, Updated: t0, Hours: map[int64]int64{h - 1: 40, h: 60}}
	attribute(st, "codex", timed, "ann", false, t0, growth)
	if got := st.Sessions[state.Key("codex", "s1")].Hours; !reflect.DeepEqual(got, map[int64]int64{h - 1: 40, h: 60}) {
		t.Errorf("hours = %v", got)
	}
	// A partial read shows 110 tokens, all in the last hour: 10 more.
	timed.Tokens.Input, timed.Hours = 100, map[int64]int64{h: 110}
	attribute(st, "codex", timed, "ann", true, t0, growth)
	if got := st.Sessions[state.Key("codex", "s1")].Hours; !reflect.DeepEqual(got, map[int64]int64{h - 1: 40, h: 70}) {
		t.Errorf("hours after a partial read = %v, want the 10 grown added", got)
	}

	untimed := logs.Session{ID: "s2", Tokens: snapshot.Tokens{Input: 30}, Updated: t0.Add(-2 * time.Hour)}
	attribute(st, "hermes", untimed, "openrouter", false, t0, growth)
	untimed.Tokens.Input, untimed.Updated = 50, t0
	attribute(st, "hermes", untimed, "openrouter", false, t0, growth)
	if got := st.Sessions[state.Key("hermes", "s2")].Hours; !reflect.DeepEqual(got, map[int64]int64{h - 2: 30, h: 20}) {
		t.Errorf("untimed hours = %v", got)
	}
}

// TestSwitchedSessionKeepsEachAccountsHours: a session continued under
// another login gives each account the hours it spent in, not a share of
// every hour of the session, and so does one continued under the first
// login again.
func TestSwitchedSessionKeepsEachAccountsHours(t *testing.T) {
	h := func(at time.Time) int64 { return at.Unix() / 3600 }
	day := 24 * time.Hour
	st := state.NewState()
	growth := map[string]snapshot.Tokens{}
	s := logs.Session{ID: "s1", Project: "/p", Tokens: snapshot.Tokens{Input: 100}, Updated: t0.Add(-3 * day), Hours: map[int64]int64{h(t0.Add(-3 * day)): 100}}
	attribute(st, "claude", s, "ann@acme.dev", false, t0.Add(-3*day), growth)
	// ann's weekly window is full: bo logs in and continues s1 today.
	s.Tokens.Input, s.Updated = 120, t0.Add(-2*time.Hour)
	s.Hours = map[int64]int64{h(t0.Add(-3 * day)): 100, h(s.Updated): 20}
	attribute(st, "claude", s, "bo@acme.dev", false, s.Updated, growth)
	days := func(label string) []int64 { return DaysOf(totalsFor(t, st, "claude", label).Hours, t0) }
	if got, want := days("ann@acme.dev"), []int64{0, 0, 0, 100}; !reflect.DeepEqual(got, want) {
		t.Errorf("ann's days = %v, want %v", got, want)
	}
	if got, want := days("bo@acme.dev"), []int64{20}; !reflect.DeepEqual(got, want) {
		t.Errorf("bo's days = %v, want %v", got, want)
	}
	if got := SinceStart(totalsFor(t, st, "claude", "ann@acme.dev").Hours, t0.Add(-day)); got != 0 {
		t.Errorf("ann since yesterday = %d, want 0", got)
	}
	// ann logs in again and continues it for 30 more.
	s.Tokens.Input, s.Updated = 150, t0
	s.Hours = map[int64]int64{h(t0.Add(-3 * day)): 100, h(t0.Add(-2 * time.Hour)): 20, h(t0): 30}
	attribute(st, "claude", s, "ann@acme.dev", false, t0, growth)
	if got, want := days("ann@acme.dev"), []int64{30, 0, 0, 100}; !reflect.DeepEqual(got, want) {
		t.Errorf("ann's days = %v, want %v", got, want)
	}
	if got, want := days("bo@acme.dev"), []int64{20}; !reflect.DeepEqual(got, want) {
		t.Errorf("bo's days = %v, want %v", got, want)
	}
	if ps := Projects(st, nil); len(ps) != 1 || !reflect.DeepEqual(DaysOf(ps[0].Hours, t0), []int64{50, 0, 0, 100}) {
		t.Errorf("projects = %+v", ps)
	}
}

// TestSwitchedSessionFromALedgerWithoutAccountsHours: a ledger from before
// accounts kept their own hours shares a session's hours by the accounts'
// shares of its tokens. From its next growth, each account keeps its own:
// what they had stays as it was shown, and the growth is its spender's.
func TestSwitchedSessionFromALedgerWithoutAccountsHours(t *testing.T) {
	h := func(at time.Time) int64 { return at.Unix() / 3600 }
	day := 24 * time.Hour
	st := &state.State{Accounts: map[string]*state.Account{}, Sessions: map[string]*state.Session{
		state.Key("codex", "s1"): {Provider: "codex", Project: "/p", Seen: snapshot.Tokens{Input: 400}, Updated: t0.Add(-time.Hour),
			By:    map[string]snapshot.Tokens{"ann@acme.dev": {Input: 300}, "bo@acme.dev": {Input: 100}},
			Hours: map[int64]int64{h(t0.Add(-2 * day)): 200, h(t0.Add(-time.Hour)): 200}},
	}}
	s := logs.Session{ID: "s1", Project: "/p", Tokens: snapshot.Tokens{Input: 440}, Updated: t0,
		Hours: map[int64]int64{h(t0.Add(-2 * day)): 200, h(t0.Add(-time.Hour)): 200, h(t0): 40}}
	attribute(st, "codex", s, "bo@acme.dev", false, t0, map[string]snapshot.Tokens{})
	e := st.Sessions[state.Key("codex", "s1")]
	want := map[string]map[int64]int64{
		"ann@acme.dev": {h(t0.Add(-2 * day)): 150, h(t0.Add(-time.Hour)): 150},
		"bo@acme.dev":  {h(t0.Add(-2 * day)): 50, h(t0.Add(-time.Hour)): 50, h(t0): 40},
	}
	if !reflect.DeepEqual(e.ByHours, want) {
		t.Errorf("accounts' hours = %v, want %v", e.ByHours, want)
	}
	if got, want := e.Hours, map[int64]int64{h(t0.Add(-2 * day)): 200, h(t0.Add(-time.Hour)): 200, h(t0): 40}; !reflect.DeepEqual(got, want) {
		t.Errorf("session's hours = %v, want %v", got, want)
	}
}

// TestUntimedHistoryFromBeforeHoursStays: a session the ledger kept from
// before it recorded hours has its tokens at the hour its share last grew.
// When its log, which records no times, shows it grow, that history stays in
// its day beside the growth.
func TestUntimedHistoryFromBeforeHoursStays(t *testing.T) {
	day := 24 * time.Hour
	for _, parts := range []map[string]snapshot.Tokens{nil, {"openrouter": {Input: 510}}} {
		st := &state.State{Accounts: map[string]*state.Account{}, Sessions: map[string]*state.Session{
			state.Key("hermes", "s2"): {Provider: "hermes", Project: "/srv1", Seen: snapshot.Tokens{Input: 500}, Updated: t0.Add(-2 * day),
				By: map[string]snapshot.Tokens{"openrouter": {Input: 500}}, Last: map[string]time.Time{"openrouter": t0.Add(-2 * day)},
				Parts: map[string]snapshot.Tokens{"openrouter": {Input: 500}}},
		}}
		if got := DaysOf(totalsFor(t, st, "hermes", "openrouter").Hours, t0); !reflect.DeepEqual(got, []int64{0, 0, 500}) {
			t.Fatalf("days before = %v", got)
		}
		s := logs.Session{ID: "s2", Project: "/srv1", Tokens: snapshot.Tokens{Input: 510}, Updated: t0, Account: "openrouter", Parts: parts}
		attribute(st, "hermes", s, "openrouter", false, t0, map[string]snapshot.Tokens{})
		a := totalsFor(t, st, "hermes", "openrouter")
		if got := DaysOf(a.Hours, t0); a.Tokens.Input != 510 || !reflect.DeepEqual(got, []int64{10, 0, 500}) {
			t.Errorf("parts %v: days after = %v for %d tokens, want [10 0 500]", parts, got, a.Tokens.Input)
		}
	}

	// A session on two routes keeps each route's history in its own day.
	st := &state.State{Accounts: map[string]*state.Account{}, Sessions: map[string]*state.Session{
		state.Key("hermes", "s3"): {Provider: "hermes", Project: "/srv1", Seen: snapshot.Tokens{Input: 700}, Updated: t0.Add(-day),
			By:    map[string]snapshot.Tokens{"openrouter": {Input: 500}, "nous": {Input: 200}},
			Last:  map[string]time.Time{"openrouter": t0.Add(-2 * day), "nous": t0.Add(-day)},
			Parts: map[string]snapshot.Tokens{"openrouter": {Input: 500}, "nous": {Input: 200}}},
	}}
	s := logs.Session{ID: "s3", Project: "/srv1", Tokens: snapshot.Tokens{Input: 710}, Updated: t0, Account: "openrouter",
		Parts: map[string]snapshot.Tokens{"openrouter": {Input: 510}, "nous": {Input: 200}}}
	attribute(st, "hermes", s, "openrouter", false, t0, map[string]snapshot.Tokens{})
	if got := DaysOf(totalsFor(t, st, "hermes", "openrouter").Hours, t0); !reflect.DeepEqual(got, []int64{10, 0, 500}) {
		t.Errorf("openrouter's days = %v, want [10 0 500]", got)
	}
	if got := DaysOf(totalsFor(t, st, "hermes", "nous").Hours, t0); !reflect.DeepEqual(got, []int64{0, 200}) {
		t.Errorf("nous's days = %v, want [0 200]", got)
	}
}

// TestHoursNeverExceedTheTokens: a Claude log spreads the tokens it records
// no time for over its timed hours, anew as the session grows, and a file
// that cannot be read keeps every read partial. A session's hours still add
// up to its tokens, and its account's days count no more than it spent.
func TestHoursNeverExceedTheTokens(t *testing.T) {
	h := t0.Unix() / 3600
	st := state.NewState()
	growth := map[string]snapshot.Tokens{}
	// 100 used at h-5, and side calls of 100 spread over that hour.
	s1 := logs.Session{ID: "s1", Tokens: snapshot.Tokens{Input: 200}, Updated: t0.Add(-5 * time.Hour), Hours: map[int64]int64{h - 5: 200}}
	attribute(st, "claude", s1, "ann@acme.dev", true, t0, growth)
	// Resumed for 100 more at h, with the side calls not read this time.
	s1.Updated, s1.Hours = t0, map[int64]int64{h - 5: 100, h: 100}
	attribute(st, "claude", s1, "ann@acme.dev", true, t0, growth)
	if got, want := st.Sessions[state.Key("claude", "s1")].Hours, map[int64]int64{h - 5: 200}; !reflect.DeepEqual(got, want) {
		t.Errorf("hours of a session that did not grow = %v, want %v", got, want)
	}
	// The same with the side calls read: 100 more, spread anew.
	s2 := logs.Session{ID: "s2", Tokens: snapshot.Tokens{Input: 200}, Updated: t0.Add(-5 * time.Hour), Hours: map[int64]int64{h - 5: 200}}
	attribute(st, "claude", s2, "ann@acme.dev", true, t0, growth)
	s2.Tokens.Input, s2.Updated, s2.Hours = 300, t0, map[int64]int64{h - 5: 150, h: 150}
	attribute(st, "claude", s2, "ann@acme.dev", true, t0, growth)
	if got, want := st.Sessions[state.Key("claude", "s2")].Hours, map[int64]int64{h - 5: 200, h: 100}; !reflect.DeepEqual(got, want) {
		t.Errorf("hours = %v, want %v", got, want)
	}
	ann := totalsFor(t, st, "claude", "ann@acme.dev")
	if got := DaysOf(ann.Hours, t0); len(got) != 1 || got[0] != ann.Tokens.InOut() {
		t.Errorf("days = %v for %d tokens", got, ann.Tokens.InOut())
	}
}

// TestGrowthIsNotPlacedInHoursTheLedgerDropped: the log of a session that
// runs longer than the retention window still shows the hours the ledger
// dropped. What the session grows by goes to the hours it was spent in.
func TestGrowthIsNotPlacedInHoursTheLedgerDropped(t *testing.T) {
	h := func(at time.Time) int64 { return at.Unix() / 3600 }
	old, yesterday := t0.Add(-state.Retention-24*time.Hour), t0.Add(-24*time.Hour)
	st := state.NewState()
	growth := map[string]snapshot.Tokens{}
	s := logs.Session{ID: "s1", Tokens: snapshot.Tokens{Input: 1100}, Updated: yesterday, Hours: map[int64]int64{h(old): 1000, h(yesterday): 100}}
	attribute(st, "codex", s, "ann@acme.dev", false, yesterday, growth)
	prune(st, t0)
	s.Tokens.Input, s.Updated, s.Hours = 1150, t0, map[int64]int64{h(old): 1000, h(yesterday): 100, h(t0): 50}
	attribute(st, "codex", s, "ann@acme.dev", false, t0, growth)
	if got, want := st.Sessions[state.Key("codex", "s1")].Hours, map[int64]int64{h(yesterday): 100, h(t0): 50}; !reflect.DeepEqual(got, want) {
		t.Errorf("hours = %v, want %v", got, want)
	}
}
