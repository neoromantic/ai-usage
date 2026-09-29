package collect

import (
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
