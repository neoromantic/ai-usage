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
	return legacyLedgerOf(t, dir, lastRun, map[string]any{key: session})
}

// legacyLedgerOf is legacyLedger with several sessions, by key.
func legacyLedgerOf(t *testing.T, dir state.Dir, lastRun time.Time, sessions map[string]any) *state.State {
	t.Helper()
	b, err := json.Marshal(map[string]any{"last_run_at": lastRun, "sessions": sessions})
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

// codexLegacy is a Codex session as a v0.3.4 ledger kept it, all ann's:
// its count, input plus output by hour, and when it last grew.
func codexLegacy(project string, seen snapshot.Tokens, at time.Time, hours map[int64]int64) map[string]any {
	return map[string]any{
		"p": "codex", "proj": project, "at": at, "seen": seen,
		"by":   map[string]snapshot.Tokens{"ann@acme.dev": seen},
		"last": map[string]time.Time{"ann@acme.dev": at},
		"h":    hours,
	}
}

// codexRead is a full read of a Codex session: its tokens by hour.
func codexRead(id string, updated time.Time, hours meter.Hours) logs.Session {
	return logs.Session{ID: id, Project: "/work/app", Tokens: hours.Sum().Tokens(), Hours: hours, Updated: updated}
}

func use2(in, out int64) meter.Use { return meter.Use{Input: in, Output: out} }

// A sub-agent whose parent's file aged out first was, for a while, a session
// of its own. A read that finds it in its parent's family again moves what
// the ledger kept for it onto the family: what it counted there is not new.
func TestFoldedSubAgentMovesOntoItsFamily(t *testing.T) {
	h0 := logs.HourOf(t0.Add(-5 * time.Hour))
	at := logs.HourStart(h0 + 1).Add(30 * time.Minute)
	st := legacyLedgerOf(t, state.Dir(t.TempDir()), at, map[string]any{
		// The parent's ledger entry is from before the sub-agent ran.
		state.Key("codex", "p1"): codexLegacy("/work/app", tok(1000), at.Add(-time.Hour), map[int64]int64{h0: 1100}),
		state.Key("codex", "c1"): codexLegacy("/work/app", tok(400), at, map[int64]int64{h0 + 1: 440}),
	})
	before := totalsFor(t, st, "codex", "ann@acme.dev")
	parsed := codexRead("p1", at, meter.Hours{h0: use2(1000, 100), h0 + 1: use2(400, 40)})
	parsed.Folded = []string{"c1"}
	growth := map[string]snapshot.Tokens{}
	if g := attribute(st, "codex", parsed, "ann@acme.dev", false, t0, growth); len(g) != 0 || len(growth) != 0 {
		t.Fatalf("growth = %+v", g)
	}
	if _, ok := st.Sessions[state.Key("codex", "c1")]; ok {
		t.Fatal("the folded session's entry was kept")
	}
	e := st.Sessions[state.Key("codex", "p1")]
	if e.Seen != tok(1400) || !reflect.DeepEqual(e.By, map[string]snapshot.Tokens{"ann@acme.dev": tok(1400)}) || !e.Last["ann@acme.dev"].Equal(at) {
		t.Fatalf("family = %+v", e)
	}
	if want := (meter.Hours{h0: use2(1000, 100), h0 + 1: use2(400, 40)}); !reflect.DeepEqual(e.U, want) {
		t.Fatalf("hours = %v, want %v", e.U, want)
	}
	// One session now, with the same tokens in the same hours.
	after := totalsFor(t, st, "codex", "ann@acme.dev")
	if after.Sessions != 1 || after.Tokens != before.Tokens || !reflect.DeepEqual(after.Hours, before.Hours) || !reflect.DeepEqual(after.Burn, before.Burn) {
		t.Fatalf("totals changed:\n%+v\n%+v", before, after)
	}
}

// While a sub-agent was a session of its own, what it spent counted there
// and in its parent's family too. Once folded back, the family's count is
// more than its log holds by that much, and the double count is taken back
// from the hours it was placed in twice.
func TestFoldedSubAgentCountedTwiceIsTakenBack(t *testing.T) {
	h0 := logs.HourOf(t0.Add(-5 * time.Hour))
	at := logs.HourStart(h0 + 1).Add(30 * time.Minute)
	st := legacyLedgerOf(t, state.Dir(t.TempDir()), at, map[string]any{
		state.Key("codex", "p1"): codexLegacy("/work/app", tok(1400), at, map[int64]int64{h0: 1100, h0 + 1: 440}),
		state.Key("codex", "c1"): codexLegacy("/work/app", tok(400), at, map[int64]int64{h0 + 1: 440}),
	})
	if got := totalsFor(t, st, "codex", "ann@acme.dev").Tokens; got != tok(1800) {
		t.Fatalf("ledger before = %+v", got)
	}
	parsed := codexRead("p1", at, meter.Hours{h0: use2(1000, 100), h0 + 1: use2(400, 40)})
	parsed.Folded = []string{"c1"}
	growth := map[string]snapshot.Tokens{}
	if g := attribute(st, "codex", parsed, "ann@acme.dev", false, t0, growth); len(g) != 0 {
		t.Fatalf("growth = %+v", g)
	}
	e := st.Sessions[state.Key("codex", "p1")]
	if e.Seen != tok(1400) || e.By["ann@acme.dev"] != tok(1400) || len(st.Sessions) != 1 {
		t.Fatalf("family = %+v, sessions %d", e, len(st.Sessions))
	}
	if want := (meter.Hours{h0: use2(1000, 100), h0 + 1: use2(400, 40)}); !reflect.DeepEqual(e.U, want) {
		t.Fatalf("hours = %v, want %v", e.U, want)
	}
}

// A release before v0.4.0 run again after a sub-agent was folded reads the
// family without it, and makes the sub-agent a session of its own again: the
// family's count drops to its own while its share keeps the sub-agent's. The
// next fold takes the family's count from its share, so the sub-agent's
// tokens, counted there already, are taken back rather than kept twice.
func TestFoldedSubAgentAfterAnOlderReleaseRanAgain(t *testing.T) {
	h0, h1 := logs.HourOf(t0.Add(-5*time.Hour)), logs.HourOf(t0.Add(-3*time.Hour))
	hs := logs.HourOf(t0.Add(-15 * time.Minute))
	st := state.NewState()
	st.LastRunAt = t0.Add(-15 * time.Minute)
	st.Sessions[state.Key("codex", "p1")] = &state.Session{Provider: "codex", Seen: tok(1000),
		By: map[string]snapshot.Tokens{"ann@acme.dev": tok(1500)}, Updated: t0.Add(-3 * time.Hour),
		U: meter.Hours{h0: use2(1000, 100), h1: use2(500, 50)}}
	st.Sessions[state.Key("codex", "c1")] = &state.Session{Provider: "codex", Seen: tok(500),
		By: map[string]snapshot.Tokens{"ann@acme.dev": tok(500)}, Updated: t0.Add(-15 * time.Minute),
		U: meter.Hours{hs: use2(500, 50)}, Rederive: true}
	parsed := codexRead("p1", t0.Add(-3*time.Hour), meter.Hours{h0: use2(1000, 100), h1: use2(500, 50)})
	parsed.Folded = []string{"c1"}
	growth := map[string]snapshot.Tokens{}
	if g := attribute(st, "codex", parsed, "ann@acme.dev", false, t0, growth); len(g) != 0 {
		t.Fatalf("growth = %+v", g)
	}
	e := st.Sessions[state.Key("codex", "p1")]
	if e.Seen != tok(1500) || e.By["ann@acme.dev"] != tok(1500) || len(st.Sessions) != 1 {
		t.Fatalf("family = %+v, sessions %d", e, len(st.Sessions))
	}
	if !reflect.DeepEqual(e.U, parsed.Hours) {
		t.Fatalf("hours = %v, want %v", e.U, parsed.Hours)
	}
}

// A fork whose source's file is gone counted the history it replayed as its
// own, in the hour it was forked. A full read no longer counts it, so the
// ledger takes it back, as a v0.3.4 ledger migrated holds it and as one this
// release placed by the hour.
func TestCodexCountThatFellIsTakenBack(t *testing.T) {
	hf := logs.HourOf(t0.Add(-6 * time.Hour))
	at := logs.HourStart(hf + 2).Add(10 * time.Minute)
	own := meter.Hours{hf + 1: use2(2000, 200), hf + 2: use2(4000, 400)}
	parsed := codexRead("f1", at, own)
	seen := snapshot.Tokens{Input: 10000, Output: 600}

	t.Run("migrated", func(t *testing.T) {
		st := legacyLedger(t, state.Dir(t.TempDir()), at, state.Key("codex", "f1"),
			codexLegacy("/work/app", seen, at, map[int64]int64{hf: 4000, hf + 1: 2200, hf + 2: 4400}))
		growth := map[string]snapshot.Tokens{}
		if g := attribute(st, "codex", parsed, "ann@acme.dev", false, t0, growth); len(g) != 0 {
			t.Fatalf("growth = %+v", g)
		}
		e := st.Sessions[state.Key("codex", "f1")]
		want := snapshot.Tokens{Input: 6000, Output: 600}
		if e.Seen != want || e.By["ann@acme.dev"] != want || e.Rederive {
			t.Fatalf("session = %+v", e)
		}
		if !reflect.DeepEqual(e.U, own) {
			t.Fatalf("hours = %v, want %v", e.U, own)
		}
		if got := totalsFor(t, st, "codex", "ann@acme.dev").Tokens; got != want {
			t.Fatalf("totals = %+v", got)
		}
	})

	placed := func() *state.State {
		st := state.NewState()
		st.Sessions[state.Key("codex", "f1")] = &state.Session{
			Provider: "codex", Project: "/work/app", Seen: seen, Updated: at,
			By:   map[string]snapshot.Tokens{"ann@acme.dev": seen},
			Last: map[string]time.Time{"ann@acme.dev": at},
			U:    meter.Hours{hf: use2(4000, 0), hf + 1: use2(2000, 200), hf + 2: use2(4000, 400)},
		}
		return st
	}
	t.Run("placed by the hour", func(t *testing.T) {
		st := placed()
		attribute(st, "codex", parsed, "ann@acme.dev", false, t0, map[string]snapshot.Tokens{})
		e := st.Sessions[state.Key("codex", "f1")]
		if e.By["ann@acme.dev"] != (snapshot.Tokens{Input: 6000, Output: 600}) || !reflect.DeepEqual(e.U, own) {
			t.Fatalf("session = %+v", e)
		}
	})
	t.Run("partial read", func(t *testing.T) {
		st := placed()
		attribute(st, "codex", parsed, "ann@acme.dev", true, t0, map[string]snapshot.Tokens{})
		e := st.Sessions[state.Key("codex", "f1")]
		if e.Seen != seen || e.By["ann@acme.dev"] != seen || len(e.U) != 3 {
			t.Fatalf("a partial read shrank the session: %+v", e)
		}
	})
	// A sub-agent whose file aged out of the window leaves the count lower
	// too, but what it spent is in hours the ledger no longer keeps.
	t.Run("aged out", func(t *testing.T) {
		st := placed()
		e := st.Sessions[state.Key("codex", "f1")]
		delete(e.U, hf)
		now := logs.HourStart(hf).Add(state.Retention + 2*time.Hour)
		attribute(st, "codex", parsed, "ann@acme.dev", false, now, map[string]snapshot.Tokens{})
		if e.By["ann@acme.dev"] != seen || e.Seen != (snapshot.Tokens{Input: 6000, Output: 600}) {
			t.Fatalf("session = %+v", e)
		}
	})
	// Claude's counts can fall for reasons of its own: the ledger keeps them.
	t.Run("claude", func(t *testing.T) {
		st := placed()
		e := st.Sessions[state.Key("codex", "f1")]
		e.Provider = "claude"
		st.Sessions[state.Key("claude", "f1")] = e
		attribute(st, "claude", parsed, "ann@acme.dev", false, t0, map[string]snapshot.Tokens{})
		if e.By["ann@acme.dev"] != seen {
			t.Fatalf("claude session = %+v", e)
		}
	})
}

// ann's and sam's weekly windows reset 34 seconds apart. A rollout Orca
// linked into both homes goes first to the account Codex's thread table says
// created it, and otherwise to the account whose reset its reading is
// nearest, within 20 seconds and 5 seconds nearer than the other's.
func TestServedBy(t *testing.T) {
	annReset := t0.Add(50 * time.Hour)
	labels := map[string]string{"/a": "ann@acme.dev", "/s": "sam@mail.test"}
	ids := map[string]string{"/a": "acct-7f3e0c21", "/s": "acct-19b4d6aa"}
	s := func(sec int, creator string) logs.Session {
		reset := annReset.Add(time.Duration(sec) * time.Second)
		s := mirrored("r1", 100, t0, &reset, "/a", "/s")
		s.CreatorAccount = creator
		return s
	}
	for _, c := range []struct {
		name string
		// gap is how many seconds after ann's window sam's resets.
		gap  int
		s    logs.Session
		ids  map[string]string
		want string
	}{
		{"ids win over resets", 34, s(0, "acct-19b4d6aa"), ids, "sam@mail.test"},
		{"no creator id", 34, s(34, ""), ids, "sam@mail.test"},
		{"a creator no home names", 34, s(0, "acct-00000000"), ids, "ann@acme.dev"},
		{"one creator for both homes", 34, s(0, "acct-7f3e0c21"), map[string]string{"/a": "acct-7f3e0c21", "/s": "acct-7f3e0c21"}, "ann@acme.dev"},
		{"12s from ann", 34, s(12, ""), nil, "ann@acme.dev"},
		{"5s from ann, 15s from sam", 20, s(5, ""), nil, "ann@acme.dev"},
		{"15s from ann, 5s from sam", 20, s(15, ""), nil, "sam@mail.test"},
		{"3s from ann, 6s from sam", 9, s(3, ""), nil, ""},
		{"the same reset", 0, s(0, ""), nil, ""},
		{"more than 20s from both", 34, s(-25, ""), nil, ""},
	} {
		st := state.NewState()
		for l, r := range map[string]time.Time{"ann@acme.dev": annReset, "sam@mail.test": annReset.Add(time.Duration(c.gap) * time.Second)} {
			st.Accounts[state.Key("codex", l)] = &state.Account{Provider: "codex", Label: l, Quota: &state.Quota{At: t0, Windows: []snapshot.Window{
				{Name: "5h", Minutes: 300, ResetsAt: ptr(t0.Add(time.Hour))},
				{Name: "7d", Minutes: 10080, ResetsAt: ptr(r)},
			}}}
		}
		if got := servedBy(st, "codex", c.s, labels, c.ids); got != c.want {
			t.Errorf("%s: served by %q, want %q", c.name, got, c.want)
		}
	}
}

// An ai-usage before v0.4.0 that runs after this one keys an auxiliary
// route's part apart, finds it missing and counts it again. Merging what it
// saved adds no more than the log grew, so the next read finds the session
// as the log has it.
func TestAuxPartAnOlderAiUsageCountedAgainIsNotKept(t *testing.T) {
	const id = "20260801_090000_b7d1f3a0"
	k := state.Key("hermes", id)
	dir := state.Dir(t.TempDir())
	at := t0.Add(-2 * time.Hour)
	in := func(n int64) snapshot.Tokens { return snapshot.Tokens{Input: n} }
	parts := map[string]snapshot.Tokens{"anthropic": in(1100), "auto": in(550)}
	st := state.NewState()
	attribute(st, "hermes", hermesSess(id, "anthropic", at, parts), "anthropic", false, at, map[string]snapshot.Tokens{})
	st.LastRunAt = at
	if err := dir.SaveState(st); err != nil {
		t.Fatal(err)
	}

	// The older ai-usage keeps both parts apart, and adds the auxiliary one.
	older := state.NewState()
	older.Ledger = 0
	older.LastRunAt = at.Add(time.Hour)
	older.Sessions[k] = &state.Session{
		Provider: "hermes", Project: "/work/agent", Updated: at, Seen: in(1650),
		By:    map[string]snapshot.Tokens{"anthropic": in(1650), "auto": in(550)},
		Parts: parts,
		Hours: map[int64]int64{logs.HourOf(at): 2200},
	}
	b, err := json.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir.StateFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err = dir.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Damage == "" {
		t.Fatal("the older ai-usage's state was not merged")
	}

	growth := map[string]snapshot.Tokens{}
	if g := attribute(st, "hermes", hermesSess(id, "anthropic", at, parts), "anthropic", false, t0, growth); len(g) != 0 {
		t.Fatalf("growth = %+v", g)
	}
	e := st.Sessions[k]
	var by snapshot.Tokens
	for _, t := range e.By {
		by = by.Add(t)
	}
	if by != in(1650) || e.U.Sum() != meter.FromTokens(in(1650)) {
		t.Fatalf("by = %+v, hours = %v; the log holds 1650", e.By, e.U)
	}
}
