package collect

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

func TestIncompleteReadDoesNotCountAgainLater(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	k := state.Key("claude", h)
	w.login("claude", h, "ann", nil)
	w.sessions("claude", h, sess("s1", "/p", 150, t0))
	run(t, o)

	// A sub-agent file cannot be opened this run, so s1 reads lower.
	w.now = t0.Add(15 * time.Minute)
	w.logs[k] = homeLogs{Sessions: []logs.Session{sess("s1", "/p", 100, t0)}, Unreadable: 1}
	run(t, o)

	w.now = t0.Add(30 * time.Minute)
	w.logs[k] = homeLogs{Sessions: []logs.Session{sess("s1", "/p", 150, t0)}}
	res := run(t, o)
	if got := totalsFor(t, res.State, "claude", "ann").Tokens; got != tok(150) {
		t.Fatalf("tokens = %+v, want %+v", got, tok(150))
	}
}

func TestCompleteReadThatDropsStartsFromTheLowerCount(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", nil)
	w.sessions("claude", h, sess("s1", "/p", 150, t0))
	run(t, o)
	// A sub-agent file aged out of the window: a real drop.
	w.now = t0.Add(15 * time.Minute)
	w.sessions("claude", h, sess("s1", "/p", 100, w.now))
	run(t, o)
	w.now = t0.Add(30 * time.Minute)
	w.sessions("claude", h, sess("s1", "/p", 120, w.now))
	res := run(t, o)
	if got := totalsFor(t, res.State, "claude", "ann").Tokens; got != tok(170) {
		t.Fatalf("tokens = %+v, want %+v", got, tok(170))
	}
}

func TestPruneDropsOldSessionsAndIdleAccounts(t *testing.T) {
	now := t0
	old := now.Add(-state.Retention - time.Hour)
	recent := now.Add(-state.Retention + time.Hour)
	st := &state.State{
		Current: map[string]string{state.Key("claude", "/h"): "cur"},
		Accounts: map[string]*state.Account{
			state.Key("claude", "cur"):        {Provider: "claude", Label: "cur", LastSeenAt: old},
			state.Key("claude", "idle"):       {Provider: "claude", Label: "idle", LastSeenAt: old},
			state.Key("claude", "used"):       {Provider: "claude", Label: "used", LastSeenAt: old},
			state.Key("claude", "quota"):      {Provider: "claude", Label: "quota", LastSeenAt: old, Quota: &state.Quota{At: recent}},
			state.Key("claude", "oldsess"):    {Provider: "claude", Label: "oldsess"},
			state.Key("codex", "cur"):         {Provider: "codex", Label: "cur", LastSeenAt: old},
			state.Key("claude", "seenrecent"): {Provider: "claude", Label: "seenrecent", LastSeenAt: recent},
		},
		Sessions: map[string]*state.Session{
			state.Key("claude", "keep"): {Provider: "claude", By: map[string]snapshot.Tokens{"used": tok(1)}, Updated: recent},
			state.Key("claude", "drop"): {Provider: "claude", By: map[string]snapshot.Tokens{"oldsess": tok(1)}, Updated: old},
		},
	}
	prune(st, now)
	var kept []string
	for _, a := range st.Accounts {
		kept = append(kept, a.Provider+"/"+a.Label)
	}
	sort.Strings(kept)
	want := []string{"claude/cur", "claude/quota", "claude/seenrecent", "claude/used"}
	if !reflect.DeepEqual(kept, want) {
		t.Fatalf("accounts kept = %v, want %v", kept, want)
	}
	if _, ok := st.Sessions[state.Key("claude", "drop")]; ok {
		t.Fatal("old session kept")
	}
	if _, ok := st.Sessions[state.Key("claude", "keep")]; !ok {
		t.Fatal("recent session dropped")
	}
}

func TestLastActiveIsPerAccount(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "old", nil)
	w.sessions("claude", h, sess("s1", "/p", 100, t0))
	run(t, o)

	// After a switch, new continues s1 five days later.
	w.now = t0.Add(5 * 24 * time.Hour)
	w.login("claude", h, "new", nil)
	w.sessions("claude", h, sess("s1", "/p", 150, w.now))
	res := run(t, o)
	if old := totalsFor(t, res.State, "claude", "old"); !old.LastActive.Equal(t0) {
		t.Fatalf("old last active = %v, its last use was %v", old.LastActive, t0)
	}
	if cur := totalsFor(t, res.State, "claude", "new"); !cur.LastActive.Equal(w.now) {
		t.Fatalf("new last active = %v", cur.LastActive)
	}
}

func TestPruneDropsAnAccountsOldShareOfALiveSession(t *testing.T) {
	now := t0
	old := now.Add(-state.Retention - time.Hour)
	st := &state.State{
		Current: map[string]string{},
		Accounts: map[string]*state.Account{
			state.Key("claude", "old"): {Provider: "claude", Label: "old", LastSeenAt: old},
			state.Key("claude", "new"): {Provider: "claude", Label: "new", LastSeenAt: now},
		},
		Sessions: map[string]*state.Session{state.Key("claude", "s1"): {
			Provider: "claude", Seen: tok(150), Updated: now,
			By:   map[string]snapshot.Tokens{"old": tok(100), "new": tok(50)},
			Last: map[string]time.Time{"old": old, "new": now},
			U:    meter.Hours{logs.HourOf(old): meter.FromTokens(tok(100)), logs.HourOf(now): meter.FromTokens(tok(50))},
			UB:   map[string]meter.Hours{"old": {logs.HourOf(old): meter.FromTokens(tok(100))}, "new": {logs.HourOf(now): meter.FromTokens(tok(50))}},
		}},
	}
	prune(st, now)
	s := st.Sessions[state.Key("claude", "s1")]
	if s == nil || s.Seen != tok(150) || !reflect.DeepEqual(s.By, map[string]snapshot.Tokens{"new": tok(50)}) {
		t.Fatalf("session = %+v", s)
	}
	want := meter.Hours{logs.HourOf(now): meter.FromTokens(tok(50))}
	if !reflect.DeepEqual(s.U, want) || !reflect.DeepEqual(s.UB, map[string]meter.Hours{"new": want}) {
		t.Fatalf("hours = %v, by account %v", s.U, s.UB)
	}
	if _, ok := st.Accounts[state.Key("claude", "old")]; ok {
		t.Fatal("idle account kept by a session another account continued")
	}
}

// legacyLedger writes a state.json from before ledger version 2 holding one
// session, and loads it as every command does.
func legacyLedger(t *testing.T, dir state.Dir, lastRun time.Time, key string, session map[string]any) *state.State {
	t.Helper()
	b, err := json.Marshal(map[string]any{"last_run_at": lastRun, "sessions": map[string]any{key: session}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(string(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir.StateFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := dir.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A ledger from before auxiliary calls billed to the session's main route
// credited them apart, and a read that finds them under the main route must
// move that credit, with its hours, rather than count them again there.
func TestAuxShareOfAMigratedLedgerMovesToTheMainRoute(t *testing.T) {
	const id = "20260801_090000_a4c2e917"
	k := state.Key("hermes", id)
	h := logs.HourOf(t0.Add(-5 * time.Hour))
	at := logs.HourStart(h + 1).Add(20 * time.Minute)
	in := func(n int64) snapshot.Tokens { return snapshot.Tokens{Input: n} }
	st := legacyLedger(t, state.Dir(t.TempDir()), at, k, map[string]any{
		"p": "hermes", "proj": "/work/bot", "at": at, "seen": in(13000),
		"by":    map[string]snapshot.Tokens{UnknownAccount: in(3000), "openai-codex": in(10000)},
		"parts": map[string]snapshot.Tokens{UnknownAccount: in(3000), "openai-codex": in(10000)},
		"last":  map[string]time.Time{UnknownAccount: at.Add(-time.Hour), "openai-codex": at},
		"h":     map[int64]int64{h: 6000, h + 1: 7000},
		"bh":    map[string]map[int64]int64{UnknownAccount: {h: 3000}, "openai-codex": {h: 3000, h + 1: 7000}},
	})
	e := st.Sessions[k]
	hours := e.U.Clone()
	if !e.Rederive || len(hours) != 2 || e.UB[UnknownAccount].Sum().Input != 3000 {
		t.Fatalf("migrated = %+v", e)
	}

	now := t0
	growth := map[string]snapshot.Tokens{}
	parsed := hermesSess(id, "openai-codex", now.Add(-time.Minute), map[string]snapshot.Tokens{"openai-codex": in(13000)})
	if g := attribute(st, "hermes", parsed, "openai-codex", false, now, growth); len(g) != 0 || len(growth) != 0 {
		t.Fatalf("growth = %+v, %+v", g, growth)
	}
	if !reflect.DeepEqual(e.U, hours) {
		t.Fatalf("hours = %v, want %v", e.U, hours)
	}
	if want := map[string]snapshot.Tokens{"openai-codex": in(13000)}; !reflect.DeepEqual(e.By, want) || !reflect.DeepEqual(e.Parts, want) {
		t.Fatalf("by = %+v, parts = %+v", e.By, e.Parts)
	}
	if _, ok := e.UB[UnknownAccount]; ok || e.UB["openai-codex"].Sum().Input != 13000 {
		t.Fatalf("hours by account = %+v", e.UB)
	}
	if _, ok := e.Last[UnknownAccount]; ok || !e.Last["openai-codex"].Equal(at) {
		t.Fatalf("last = %+v", e.Last)
	}
	// Its tokens stay in the hours they were spent in, now the main route's.
	a := totalsFor(t, st, "hermes", "openai-codex")
	if a.Tokens != in(13000) || hasTotals(st, "hermes", UnknownAccount) {
		t.Fatalf("totals = %+v", Totals(st, nil))
	}
}

// An earlier release dated a Claude session by its file's time, which a
// later write moves. A full read dates it by its last message, even when
// that is earlier; a partial read, which may have missed the last message,
// leaves it.
func TestClaudeSessionIsDatedByItsLastMessage(t *testing.T) {
	st := state.NewState()
	k := state.Key("claude", "a4c2e917")
	mtime := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	last := time.Date(2026, 9, 1, 12, 10, 0, 0, time.UTC)
	tokens := snapshot.Tokens{Input: 100, Output: 900}
	st.Sessions[k] = &state.Session{
		Provider: "claude", Project: "/work/app", Seen: tokens, Updated: mtime,
		By:   map[string]snapshot.Tokens{"ann@acme.dev": tokens},
		Last: map[string]time.Time{"ann@acme.dev": mtime},
		U:    meter.Hours{logs.HourOf(last): meter.FromTokens(tokens)},
	}
	s := logs.Session{ID: "a4c2e917", Project: "/work/app", Tokens: tokens, Updated: last}
	now := mtime.Add(time.Hour)
	growth := map[string]snapshot.Tokens{}
	attribute(st, "claude", s, "ann@acme.dev", true, now, growth)
	if e := st.Sessions[k]; !e.Updated.Equal(mtime) {
		t.Fatalf("partial read dated it %v", e.Updated)
	}
	attribute(st, "claude", s, "ann@acme.dev", false, now, growth)
	e := st.Sessions[k]
	if !e.Updated.Equal(last) || !e.Last["ann@acme.dev"].Equal(mtime) || len(growth) != 0 {
		t.Fatalf("session = %+v, growth %+v", e, growth)
	}
	// Other harnesses keep their newest time.
	gk := state.Key("grok", "b5c6d7e8")
	st.Sessions[gk] = &state.Session{Provider: "grok", Seen: tokens, Updated: mtime, By: map[string]snapshot.Tokens{"ann@acme.dev": tokens}}
	attribute(st, "grok", logs.Session{ID: "b5c6d7e8", Tokens: tokens, Updated: last}, "ann@acme.dev", false, now, growth)
	if got := st.Sessions[gk].Updated; !got.Equal(mtime) {
		t.Fatalf("grok session dated %v", got)
	}
}
