package state

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
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
		}},
		Relay: Relay{LastPushAt: at, Pending: true},
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
