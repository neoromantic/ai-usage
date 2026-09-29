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
		Relay: Relay{LastPushAt: at, Pending: true, NoExtUntil: at.Add(24 * time.Hour)},
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
