package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

func tempDir(t *testing.T) Dir {
	t.Helper()
	return Dir(filepath.Join(t.TempDir(), "ai-usage"))
}

func TestDefaultDirHonorsEnv(t *testing.T) {
	want := t.TempDir()
	t.Setenv("AI_USAGE_HOME", want)
	d, err := DefaultDir()
	if err != nil || string(d) != want {
		t.Fatalf("DefaultDir = %q, %v; want %q", d, err, want)
	}
}

func TestLoadStateMissingIsEmpty(t *testing.T) {
	st, err := tempDir(t).LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Sources == nil || st.Current == nil || st.Accounts == nil || st.Sessions == nil {
		t.Fatalf("empty state has nil maps: %+v", st)
	}
}

func TestLoadStateStartsAgainFromADamagedFile(t *testing.T) {
	for _, body := range []string{"{\"last_run_at\": \"2026-09", `{"sessions": 5}`} {
		d := tempDir(t)
		if err := fsutil.WriteFile(d.Path("state.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := d.LoadState()
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if st.Damage == "" || st.LastError != st.Damage || st.LastErrorAt.IsZero() || !st.LastRunAt.IsZero() {
			t.Fatalf("%q: state = %+v", body, st)
		}
		if st.Sources == nil || st.Current == nil || st.Accounts == nil || st.Sessions == nil {
			t.Fatalf("%q: nil maps: %+v", body, st)
		}
		if b, err := os.ReadFile(d.Path("state.json.bad")); err != nil || string(b) != body {
			t.Fatalf("%q: kept %q, %v", body, b, err)
		}
		// Damage is for this run only; saving writes a good file.
		if err := d.SaveState(st); err != nil {
			t.Fatal(err)
		}
		again, err := d.LoadState()
		if err != nil || again.Damage != "" || again.LastError != st.LastError {
			t.Fatalf("%q: reloaded %+v, %v", body, again, err)
		}
	}
}

func TestLoadStateFailsWhenTheFileCannotBeRead(t *testing.T) {
	d := tempDir(t)
	if err := os.MkdirAll(d.Path("state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := d.LoadState(); err == nil {
		t.Fatal("an unreadable state.json loaded as empty")
	}
}

func TestStateRoundTrip(t *testing.T) {
	d := tempDir(t)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	reset := at.Add(5 * time.Hour)
	in := &State{
		Ledger:        LedgerVersion,
		LastRunAt:     at,
		LastSuccessAt: at,
		Sources:       map[string]Source{"claude": {Status: "ok", Homes: []string{"/h/.claude"}}},
		Current:       map[string]string{Key("claude", "/h/.claude"): "a@b.c"},
		Accounts: map[string]*Account{Key("claude", "a@b.c"): {
			Provider: "claude", Label: "a@b.c", Plan: "max", LastSeenAt: at,
			Quota: &Quota{At: at, Source: "cache", Windows: []snapshot.Window{{Name: "5h", Percent: 12.5, ResetsAt: &reset, Minutes: 300}}},
		}},
		Sessions: map[string]*Session{Key("claude", "s1"): {
			Provider: "claude", Project: "/p", Seen: snapshot.Tokens{Input: 5}, By: map[string]snapshot.Tokens{"a@b.c": {Input: 5}}, Updated: at,
			U:    meter.Hours{497000: {Input: 5, CacheWrite: 3, CacheWrite1h: 1}},
			UB:   map[string]meter.Hours{"a@b.c": {497000: {Input: 5}}},
			Side: snapshot.Tokens{Output: 7}, Nulls: 2, Rederive: true,
		}},
		Relay: Relay{LastPushAt: at, Pending: true, NoExtUntil: at.Add(24 * time.Hour), NoCyclesUntil: at.Add(12 * time.Hour)},
	}
	if err := d.SaveState(in); err != nil {
		t.Fatal(err)
	}
	out, err := d.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip changed the state:\n in %+v\nout %+v", in, out)
	}
	checkPrivate(t, d.Path("state.json"))
}

func checkPrivate(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %v, want 0600", filepath.Base(path), info.Mode().Perm())
	}
}

// legacyState writes a state.json as ai-usage v0.3 did, with input plus
// output by hour, and returns its bytes.
func legacyState(t *testing.T, d Dir, sessions map[string]*Session) []byte {
	t.Helper()
	b, err := json.MarshalIndent(&State{Sessions: sessions}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"ledger": 0`) {
		t.Fatalf("not a legacy ledger: %s", b)
	}
	if err := fsutil.WriteFile(d.StateFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestLoadStateMigratesTheLedger: a v0.3 session's tokens of every kind
// follow the input plus output it kept by hour, and the file is kept once as
// state.json.pre-v0.4.
func TestLoadStateMigratesTheLedger(t *testing.T) {
	d := tempDir(t)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	raw := legacyState(t, d, map[string]*Session{Key("claude", "a4c2e917"): {
		Provider: "claude", Project: "/Users/ann/src/app", Updated: at,
		By:    map[string]snapshot.Tokens{"ann@acme.dev": {Input: 100, Output: 900, CacheRead: 50000, CacheWrite: 4000}},
		Hours: map[int64]int64{497000: 600, 497001: 400},
	}})
	for range 2 {
		st, err := d.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		s := st.Sessions[Key("claude", "a4c2e917")]
		want := meter.Hours{
			497000: {Input: 60, Output: 540, CacheRead: 30000, CacheWrite: 2400},
			497001: {Input: 40, Output: 360, CacheRead: 20000, CacheWrite: 1600},
		}
		if st.Ledger != LedgerVersion || !s.Rederive || !reflect.DeepEqual(s.U, want) || s.Hours != nil || s.ByHours != nil || s.UB != nil {
			t.Fatalf("ledger %d, session %+v; want U %v", st.Ledger, s, want)
		}
		// Loading again, as a report before the next collect does, keeps
		// the first copy.
		if b, err := os.ReadFile(d.StateFile() + ".pre-v0.4"); err != nil || string(b) != string(raw) {
			t.Fatalf("pre-v0.4 = %q, %v", b, err)
		}
	}
	// A saved ledger is not migrated again, and never writes the old keys.
	st, _ := d.LoadState()
	if err := d.SaveState(st); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(d.StateFile())
	if strings.Contains(string(b), `"h":`) || strings.Contains(string(b), `"bh":`) || !strings.Contains(string(b), `"ledger": 2`) {
		t.Fatalf("saved %s", b)
	}
	again, _ := d.LoadState()
	if !reflect.DeepEqual(again.Sessions, st.Sessions) {
		t.Fatalf("a v2 ledger changed on load: %+v", again.Sessions)
	}
}

func TestMigrationNeverReplacesAKeptCopy(t *testing.T) {
	d := tempDir(t)
	legacyState(t, d, map[string]*Session{})
	if err := os.WriteFile(d.StateFile()+".pre-v0.4", []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.LoadState(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(d.StateFile() + ".pre-v0.4"); string(b) != "first" {
		t.Fatalf("kept copy replaced by %q", b)
	}
	if left, _ := filepath.Glob(d.Path(".state.json.pre-v0.4.*")); len(left) > 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// TestConcurrentLoadsKeepOneWholeCopy: a report and a collect that load a
// v0.3 ledger at once leave one complete copy of it.
func TestConcurrentLoadsKeepOneWholeCopy(t *testing.T) {
	d := tempDir(t)
	sessions := map[string]*Session{}
	for i := range 200 {
		sessions[Key("codex", fmt.Sprintf("s%03d", i))] = &Session{Provider: "codex", By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: int64(i + 1)}}, Hours: map[int64]int64{497000: int64(i + 1)}}
	}
	raw := legacyState(t, d, sessions)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.LoadState(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if b, err := os.ReadFile(d.StateFile() + ".pre-v0.4"); err != nil || string(b) != string(raw) {
		t.Fatalf("pre-v0.4 is %d bytes, %v; want the %d of the original", len(b), err, len(raw))
	}
}

func TestMigrateSession(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 30, 0, 0, time.UTC)
	cases := []struct {
		name   string
		in     Session
		u      meter.Hours
		byHour map[string]meter.Hours
	}{
		{"two accounts keep their own hours",
			Session{By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 300}, "sam@mail.test": {Input: 100, CacheRead: 1000}},
				Hours:   map[int64]int64{1: 200, 2: 200},
				ByHours: map[string]map[int64]int64{"ann@acme.dev": {1: 150, 2: 150}, "sam@mail.test": {1: 50, 2: 50}}},
			meter.Hours{1: {Input: 200, CacheRead: 500}, 2: {Input: 200, CacheRead: 500}},
			map[string]meter.Hours{"ann@acme.dev": {1: {Input: 150}, 2: {Input: 150}}, "sam@mail.test": {1: {Input: 50, CacheRead: 500}, 2: {Input: 50, CacheRead: 500}}}},
		{"cache only: at the last activity",
			Session{Updated: at, By: map[string]snapshot.Tokens{"ann@acme.dev": {CacheRead: 5000}}},
			meter.Hours{at.Unix() / 3600: {CacheRead: 5000}}, nil},
		{"no hours: at the last activity",
			Session{Updated: at, By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 7, Output: 3}}},
			meter.Hours{at.Unix() / 3600: {Input: 7, Output: 3}}, nil},
		{"hours that dropped out keep only their share",
			Session{By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 1000, CacheRead: 10000}}, Hours: map[int64]int64{5: 250}},
			meter.Hours{5: {Input: 250, CacheRead: 2500}}, nil},
		{"no tokens",
			Session{Updated: at, By: map[string]snapshot.Tokens{}}, nil, nil},
	}
	for _, c := range cases {
		s := c.in
		migrateSession(&s)
		if !reflect.DeepEqual(s.U, c.u) || !reflect.DeepEqual(s.UB, c.byHour) || !s.Rederive || s.Hours != nil || s.ByHours != nil {
			t.Errorf("%s: U %v, UB %v, rd %v; want %v, %v", c.name, s.U, s.UB, s.Rederive, c.u, c.byHour)
		}
	}
}

func TestSaveStateKeepsTheLastCopy(t *testing.T) {
	d := tempDir(t)
	st := NewState()
	st.LastRunAt = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := d.SaveState(st); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(d.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	last, err := os.ReadFile(d.StateFile() + ".last")
	if err != nil || string(last) != string(b) {
		t.Fatalf("state.json.last = %q, %v; want %q", last, err, b)
	}
	checkPrivate(t, d.StateFile()+".last")
}

// TestLoadStateRestoresTheLastCopy: a state.json cut short loads the state
// saved before it, and says so.
func TestLoadStateRestoresTheLastCopy(t *testing.T) {
	d := tempDir(t)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	st := NewState()
	st.LastRunAt = at
	st.Sessions[Key("claude", "a4c2e917")] = &Session{Provider: "claude", By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 5}}, U: meter.Hours{497000: {Input: 5}}}
	if err := d.SaveState(st); err != nil {
		t.Fatal(err)
	}
	body := `{"last_run_at": "2026-09`
	if err := fsutil.WriteFile(d.StateFile(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := d.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	want := "state.json did not parse; restored the last saved state (state.json.last)"
	if got.Damage != want || got.LastError != want || got.LastErrorAt.IsZero() || !got.LastRunAt.Equal(at) {
		t.Fatalf("damage %q, last error %q at %v, last run %v", got.Damage, got.LastError, got.LastErrorAt, got.LastRunAt)
	}
	if !reflect.DeepEqual(got.Sessions, st.Sessions) {
		t.Fatalf("sessions = %+v; want %+v", got.Sessions, st.Sessions)
	}
	if b, err := os.ReadFile(d.StateFile() + ".bad"); err != nil || string(b) != body {
		t.Fatalf("kept %q, %v", b, err)
	}
	// Saving writes a good file again.
	if err := d.SaveState(got); err != nil {
		t.Fatal(err)
	}
	if again, err := d.LoadState(); err != nil || again.Damage != "" {
		t.Fatalf("reloaded damage %q, %v", again.Damage, err)
	}
}

func TestLoadStateStartsAgainWhenTheLastCopyIsDamagedToo(t *testing.T) {
	d := tempDir(t)
	for _, f := range []string{d.StateFile(), d.StateFile() + ".last"} {
		if err := fsutil.WriteFile(f, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := d.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Damage, "started again from an empty state") || len(st.Sessions) != 0 || st.Ledger != LedgerVersion {
		t.Fatalf("state = %+v", st)
	}
}

// olderWriter saves last as this build does, then writes state.json as an
// ai-usage before ledger version 2 would over it, with cur's sessions.
func olderWriter(t *testing.T, d Dir, last *State, cur *State) {
	t.Helper()
	if err := d.SaveState(last); err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(cur, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := fsutil.WriteFile(d.StateFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadStateMergesWhatAnOlderAiUsageCollected: an ai-usage before the
// ledger's version 2 collected after this one and dropped the hours of every
// kind. The session keeps them, and adds the growth since at the hours the
// older one placed it in after the last run.
func TestLoadStateMergesWhatAnOlderAiUsageCollected(t *testing.T) {
	d := tempDir(t)
	ran := time.Date(2026, 9, 1, 12, 20, 0, 0, time.UTC)
	h := hourOf(ran)
	s, gone, fresh := Key("claude", "a4c2e917"), Key("claude", "b71d03aa"), Key("codex", "c93e5f10")
	last := NewState()
	last.LastRunAt = ran
	last.Relay.NoExtUntil = ran.Add(24 * time.Hour)
	last.Relay.NoCyclesUntil = ran.Add(12 * time.Hour)
	last.Sessions[s] = &Session{
		Provider: "claude", Project: "/Users/ann/src/app", Updated: ran,
		Seen: snapshot.Tokens{Input: 1000, Output: 9000},
		By:   map[string]snapshot.Tokens{"ann@acme.dev": {Input: 1000, Output: 9000}},
		U:    meter.Hours{h - 2: {Input: 200, Output: 3000}, h - 1: {Input: 300, Output: 3000}, h: {Input: 500, Output: 3000, CacheRead: 70}},
		Side: snapshot.Tokens{Output: 400}, Nulls: 3, Rederive: true,
	}
	last.Sessions[gone] = &Session{Provider: "claude", By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 9}}, U: meter.Hours{h: {Input: 9}}}

	cur := &State{LastRunAt: ran.Add(time.Hour), Sessions: map[string]*Session{
		s: {
			Provider: "claude", Project: "/Users/ann/src/app", Updated: ran.Add(time.Hour),
			Seen: snapshot.Tokens{Input: 1500, Output: 9500},
			By:   map[string]snapshot.Tokens{"ann@acme.dev": {Input: 1500, Output: 9500}},
			// The older ai-usage placed the whole log anew: only the hours
			// from the one before the last run on can hold the growth.
			Hours: map[int64]int64{h - 5: 9000, h + 1: 1000},
		},
		fresh: {
			Provider: "codex", Updated: ran.Add(time.Hour),
			By:    map[string]snapshot.Tokens{"sam@mail.test": {Input: 40, Output: 10}},
			Hours: map[int64]int64{h + 1: 50},
		},
	}}
	olderWriter(t, d, last, cur)

	st, err := d.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	got := st.Sessions[s]
	want := last.Sessions[s].U.Clone()
	want[h+1] = meter.Use{Input: 500, Output: 500}
	if !reflect.DeepEqual(got.U, want) || got.UB != nil || got.Hours != nil {
		t.Fatalf("U = %v, UB %v, h %v; want U %v", got.U, got.UB, got.Hours, want)
	}
	if got.Side != (snapshot.Tokens{Output: 400}) || got.Nulls != 3 || !got.Rederive || got.Seen.Input != 1500 {
		t.Fatalf("session = %+v", got)
	}
	if _, ok := st.Sessions[gone]; ok {
		t.Fatal("a session the older ai-usage pruned came back")
	}
	if f := st.Sessions[fresh]; f == nil || !f.Rederive || !reflect.DeepEqual(f.U, meter.Hours{h + 1: {Input: 40, Output: 10}}) {
		t.Fatalf("a session only the older ai-usage saw = %+v", f)
	}
	if st.Ledger != LedgerVersion || !st.Relay.NoExtUntil.Equal(last.Relay.NoExtUntil) || !st.Relay.NoCyclesUntil.Equal(last.Relay.NoCyclesUntil) || !st.LastRunAt.Equal(cur.LastRunAt) {
		t.Fatalf("ledger %d, no ext until %v, last run %v", st.Ledger, st.Relay.NoExtUntil, st.LastRunAt)
	}
	wantDamage := "an older ai-usage (v0.3) collected here after v0.4; its hours were merged"
	if st.Damage != wantDamage || st.LastError != wantDamage || st.LastErrorAt.IsZero() {
		t.Fatalf("damage %q, last error %q at %v", st.Damage, st.LastError, st.LastErrorAt)
	}
	// Once saved, the ledger loads as it is.
	if err := d.SaveState(st); err != nil {
		t.Fatal(err)
	}
	again, err := d.LoadState()
	if err != nil || again.Damage != "" || !reflect.DeepEqual(again.Sessions, st.Sessions) {
		t.Fatalf("reloaded damage %q, %v", again.Damage, err)
	}
}

// TestMergeKeepsEachAccountsHours: in a session whose accounts keep their own
// hours, each account's growth goes to its own, and U stays their sum.
func TestMergeKeepsEachAccountsHours(t *testing.T) {
	d := tempDir(t)
	ran := time.Date(2026, 9, 1, 12, 20, 0, 0, time.UTC)
	h := hourOf(ran)
	k := Key("hermes", "d2e8a6b4")
	last := NewState()
	last.LastRunAt = ran
	last.Update.Installed = "v0.4.1"
	last.Sessions[k] = &Session{
		Provider: "hermes", Updated: ran,
		By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 100}, "sam@mail.test": {Input: 50}},
		U:  meter.Hours{h: {Input: 150}},
		UB: map[string]meter.Hours{"ann@acme.dev": {h: {Input: 100}}, "sam@mail.test": {h: {Input: 50}}},
	}
	cur := &State{LastRunAt: ran.Add(2 * time.Hour), Update: Update{Installed: "v0.3.2"}, Sessions: map[string]*Session{k: {
		Provider: "hermes", Updated: ran.Add(2 * time.Hour),
		By:      map[string]snapshot.Tokens{"ann@acme.dev": {Input: 130}, "sam@mail.test": {Input: 60}},
		Hours:   map[int64]int64{h + 1: 30, h + 2: 10},
		ByHours: map[string]map[int64]int64{"ann@acme.dev": {h + 1: 30}},
	}}}
	olderWriter(t, d, last, cur)
	st, err := d.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	got := st.Sessions[k]
	wantBy := map[string]meter.Hours{
		"ann@acme.dev":  {h: {Input: 100}, h + 1: {Input: 30}},
		"sam@mail.test": {h: {Input: 50}, h + 1: {Input: 8}, h + 2: {Input: 2}},
	}
	if !reflect.DeepEqual(got.UB, wantBy) {
		t.Fatalf("UB = %v; want %v", got.UB, wantBy)
	}
	if want := (meter.Hours{h: {Input: 150}, h + 1: {Input: 38}, h + 2: {Input: 2}}); !reflect.DeepEqual(got.U, want) {
		t.Fatalf("U = %v; want %v", got.U, want)
	}
	if !strings.Contains(st.Damage, "(v0.3.2)") {
		t.Fatalf("damage %q does not name the older release", st.Damage)
	}
}

// TestLoadStateMigratesAnOlderLedgerPutBack: a state.json from before
// version 2 that is older than the last copy, as state.json.pre-v0.4 put
// back, is migrated as it is.
func TestLoadStateMigratesAnOlderLedgerPutBack(t *testing.T) {
	d := tempDir(t)
	ran := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	k := Key("claude", "a4c2e917")
	last := NewState()
	last.LastRunAt = ran
	last.Sessions[k] = &Session{Provider: "claude", By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 900}}, U: meter.Hours{497000: {Input: 900}}}
	cur := &State{LastRunAt: ran.Add(-time.Hour), Sessions: map[string]*Session{k: {
		Provider: "claude", By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 100}}, Hours: map[int64]int64{496990: 100},
	}}}
	olderWriter(t, d, last, cur)
	st, err := d.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Damage != "" || !reflect.DeepEqual(st.Sessions[k].U, meter.Hours{496990: {Input: 100}}) {
		t.Fatalf("damage %q, U %v", st.Damage, st.Sessions[k].U)
	}
}

// TestSaveStateNeverWritesOverANewerLedger: an ai-usage that finds a ledger
// a newer one saved reads it and leaves it as it is.
func TestSaveStateNeverWritesOverANewerLedger(t *testing.T) {
	d := tempDir(t)
	body := []byte(`{"ledger": 3, "last_run_at": "2026-09-01T12:00:00Z", "sessions": {}, "future": [1]}` + "\n")
	if err := fsutil.WriteFile(d.StateFile(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := d.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Newer || st.Damage != "" || st.LastRunAt.IsZero() {
		t.Fatalf("state = %+v", st)
	}
	if err := d.SaveState(st); err != ErrNewer {
		t.Fatalf("SaveState = %v; want ErrNewer", err)
	}
	if b, _ := os.ReadFile(d.StateFile()); string(b) != string(body) {
		t.Fatalf("state.json changed to %q", b)
	}
	if _, err := os.Stat(d.StateFile() + ".last"); !os.IsNotExist(err) {
		t.Fatalf("state.json.last written: %v", err)
	}
}

// TestAnOlderWriterAfterANewerLedgerKeepsItsCopy: when an older ai-usage
// wrote over a ledger newer than this build's, this build does not replace
// the newer copy either.
func TestAnOlderWriterAfterANewerLedgerKeepsItsCopy(t *testing.T) {
	d := tempDir(t)
	newer := []byte(`{"ledger": 3, "last_run_at": "2026-09-01T12:00:00Z"}` + "\n")
	if err := fsutil.WriteFile(d.StateFile()+".last", newer, 0o600); err != nil {
		t.Fatal(err)
	}
	legacyState(t, d, map[string]*Session{})
	st, err := d.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Newer || d.SaveState(st) != ErrNewer {
		t.Fatalf("newer = %v", st.Newer)
	}
	if b, _ := os.ReadFile(d.StateFile() + ".last"); string(b) != string(newer) {
		t.Fatalf("state.json.last changed to %q", b)
	}
}

// TestMergeGrowsASplitSessionByNoMoreThanItsLog: an older ai-usage that
// keys an auxiliary route's part apart counts it again. The accounts grow
// by what the log grew, in proportion, and no more.
func TestMergeGrowsASplitSessionByNoMoreThanItsLog(t *testing.T) {
	d := tempDir(t)
	ran := time.Date(2026, 9, 1, 12, 20, 0, 0, time.UTC)
	h := hourOf(ran)
	k := Key("hermes", "e4f1c9a2")
	in := func(n int64) snapshot.Tokens { return snapshot.Tokens{Input: n} }
	last := NewState()
	last.LastRunAt = ran
	last.Sessions[k] = &Session{
		Provider: "hermes", Updated: ran, Seen: in(1650),
		By: map[string]snapshot.Tokens{"anthropic": in(1650)}, Parts: map[string]snapshot.Tokens{"anthropic": in(1650)},
		U: meter.Hours{h: {Input: 1650}},
	}
	// The log grew by 100, and the older ai-usage added the 550 of the
	// auxiliary part again.
	cur := &State{LastRunAt: ran.Add(time.Hour), Sessions: map[string]*Session{k: {
		Provider: "hermes", Updated: ran.Add(time.Hour), Seen: in(1750),
		By:    map[string]snapshot.Tokens{"anthropic": in(1650), "auto": in(650)},
		Parts: map[string]snapshot.Tokens{"anthropic": in(1200), "auto": in(550)},
		Hours: map[int64]int64{h + 1: 650},
	}}}
	olderWriter(t, d, last, cur)
	st, err := d.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	got := st.Sessions[k]
	if want := map[string]snapshot.Tokens{"anthropic": in(1650), "auto": in(100)}; !reflect.DeepEqual(got.By, want) {
		t.Fatalf("by = %v, want %v", got.By, want)
	}
	if want := (meter.Hours{h: {Input: 1650}, h + 1: {Input: 100}}); !reflect.DeepEqual(got.U, want) {
		t.Fatalf("U = %v, want %v", got.U, want)
	}
	// The parts stay as the older ai-usage last saw the log.
	if !reflect.DeepEqual(got.Parts, cur.Sessions[k].Parts) || got.Seen != in(1750) {
		t.Fatalf("parts = %v, seen %v", got.Parts, got.Seen)
	}
}
