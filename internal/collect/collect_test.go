package collect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

var t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func TestReadErrorOnOneProviderDoesNotStopOthers(t *testing.T) {
	w, o := newWorld(t)
	ch := w.home(t, "claude")
	xh := w.home(t, "codex")
	w.readErr[state.Key("claude", ch)] = errors.New("permission denied")
	w.login("claude", ch, "ann", nil)
	w.login("codex", xh, "bob", nil)
	w.sessions("codex", xh, sess("c1", "/p", 10, t0))
	res := run(t, o)

	if src := res.State.Sources["claude"]; src.Status != "error" || !strings.Contains(src.Error, ch) || !strings.Contains(src.Error, "permission denied") {
		t.Fatalf("claude source = %+v", src)
	}
	if src := res.State.Sources["codex"]; src.Status != "ok" {
		t.Fatalf("codex source = %+v", src)
	}
	if totalsFor(t, res.State, "codex", "bob").Tokens != tok(10) {
		t.Fatal("codex was not collected")
	}
	if !res.State.LastSuccessAt.IsZero() {
		t.Fatalf("a run with a failed source counted as success: %v", res.State.LastSuccessAt)
	}
	if !strings.HasPrefix(res.State.LastError, "claude") || !strings.Contains(res.State.LastError, "permission denied") || !res.State.LastErrorAt.Equal(t0) {
		t.Fatalf("last error = %q at %v", res.State.LastError, res.State.LastErrorAt)
	}
	// The error reaches this run's snapshot, not the next one.
	if got, _ := res.Key.Open(res.Doc.LastError); !strings.Contains(got, "permission denied") {
		t.Fatalf("snapshot last error = %q", got)
	}
}

// A bug that panics in one source's parser or probe is that source's error.
// The other sources are still collected, and the run still saves its state.
func TestPanicIsItsSourceError(t *testing.T) {
	for _, where := range []string{"read", "probe"} {
		t.Run(where, func(t *testing.T) {
			w, o := newWorld(t)
			ch := w.home(t, "claude")
			xh := w.home(t, "codex")
			w.login("claude", ch, "ann", nil)
			w.login("codex", xh, "bob", nil)
			w.sessions("codex", xh, sess("c1", "/p", 10, t0))
			if where == "read" {
				read := o.ReadLogs
				o.ReadLogs = func(p string, homes []string, since time.Time) logs.Result {
					if p == "claude" {
						panic("a parser bug")
					}
					return read(p, homes, since)
				}
			} else {
				ask := o.Ask
				o.Ask = func(ctx context.Context, p, home string, lastUse time.Time) (probe.Reading, error) {
					if p == "claude" {
						panic("a parser bug")
					}
					return ask(ctx, p, home, lastUse)
				}
			}
			res := run(t, o)
			if src := res.State.Sources["claude"]; src.Status != "error" || !strings.Contains(src.Error, "stopped by a bug") {
				t.Fatalf("claude source = %+v", src)
			}
			if src := res.State.Sources["codex"]; src.Status != "ok" || !IsCurrent(res.State, "codex", "bob") || totalsFor(t, res.State, "codex", "bob").Tokens != tok(10) {
				t.Fatalf("codex source = %+v", src)
			}
			if !strings.Contains(res.State.LastError, "stopped by a bug") {
				t.Fatalf("last error = %q", res.State.LastError)
			}
			st, err := o.Dir.LoadState()
			if err != nil || st.Sources["codex"].Status != "ok" {
				t.Fatalf("saved state = %+v, %v", st, err)
			}
		})
	}
}

func TestPartialReads(t *testing.T) {
	w, o := newWorld(t)
	def := w.home(t, "claude")
	extra := w.extraHome(t, "claude", "claude-work")
	w.readErr[state.Key("claude", extra)] = errors.New("boom")
	r := w.logs[state.Key("claude", def)]
	r.Malformed, r.Unreadable = 3, 1
	w.logs[state.Key("claude", def)] = r
	res := run(t, o)
	src := res.State.Sources["claude"]
	if src.Status != "partial" {
		t.Fatalf("status = %q", src.Status)
	}
	for _, want := range []string{extra, "boom", "3 malformed", "1 unreadable"} {
		if !strings.Contains(src.Error, want) {
			t.Fatalf("error %q lacks %q", src.Error, want)
		}
	}
}

func TestSameSessionInTwoHomesCountsOnce(t *testing.T) {
	w, o := newWorld(t)
	def := w.home(t, "claude")
	work := w.extraHome(t, "claude", "claude-work")
	w.login("claude", def, "personal", nil)
	w.login("claude", work, "work", nil)
	// work is a copy of the default home; s1 was resumed there.
	w.sessions("claude", def, sess("s1", "/p", 100, t0.Add(-time.Hour)))
	w.sessions("claude", work, sess("s1", "/p", 130, t0))

	res := run(t, o)
	want := totalsFor(t, res.State, "claude", "work").Tokens.Add(totalsFor(t, res.State, "claude", "personal").Tokens)
	if want != tok(130) {
		t.Fatalf("first run counted %+v, want %+v", want, tok(130))
	}
	for i := 1; i <= 3; i++ {
		w.now = t0.Add(time.Duration(i) * 15 * time.Minute)
		res = run(t, o)
		got := snapshot.Tokens{}
		for _, a := range Totals(res.State) {
			got = got.Add(a.Tokens)
		}
		if got != tok(130) {
			t.Fatalf("run %d: unchanged logs counted again: total %+v", i, got)
		}
	}
}

func TestCurrentForgetsRemovedHome(t *testing.T) {
	w, o := newWorld(t)
	def := w.home(t, "codex")
	extra := w.extraHome(t, "codex", "codex-work")
	w.login("codex", def, "personal", nil)
	w.login("codex", extra, "work", quota(t0, 12))
	w.sessions("codex", extra, sess("c1", "/p", 10, t0))
	res := run(t, o)
	if !IsCurrent(res.State, "codex", "work") || !IsCurrent(res.State, "codex", "personal") {
		t.Fatalf("current = %v", res.State.Current)
	}

	if err := os.RemoveAll(extra); err != nil {
		t.Fatal(err)
	}
	w.now = t0.Add(15 * time.Minute)
	res = run(t, o)
	if IsCurrent(res.State, "codex", "work") {
		t.Fatalf("account of a removed home still current: %v", res.State.Current)
	}
	work := totalsFor(t, res.State, "codex", "work")
	if work.Tokens != tok(10) || work.Quota == nil {
		t.Fatalf("removed home's account lost its record: %+v", work)
	}
	if !IsCurrent(res.State, "codex", "personal") {
		t.Fatal("default home's account dropped")
	}
}

func TestSkippedProviderForgetsCurrent(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "grok")
	w.login("grok", h, "gina", nil)
	res := run(t, o)
	if !IsCurrent(res.State, "grok", "gina") {
		t.Fatal("not current after first run")
	}
	if err := os.RemoveAll(h); err != nil {
		t.Fatal(err)
	}
	w.now = t0.Add(15 * time.Minute)
	res = run(t, o)
	if res.State.Sources["grok"].Status != "skipped" || IsCurrent(res.State, "grok", "gina") {
		t.Fatalf("grok source %+v current %v", res.State.Sources["grok"], res.State.Current)
	}
}

func TestNothingInstalledIsASuccessfulRun(t *testing.T) {
	_, o := newWorld(t)
	res := run(t, o)
	for _, p := range snapshot.Providers {
		if res.State.Sources[p].Status != "skipped" {
			t.Fatalf("%s = %+v", p, res.State.Sources[p])
		}
	}
	if !res.State.LastSuccessAt.Equal(t0) || res.State.LastError != "" {
		t.Fatalf("state = %+v", res.State)
	}
	if len(res.Doc.Accounts) != 0 || len(res.Doc.Sources) != len(snapshot.Providers) {
		t.Fatalf("doc = %+v", res.Doc)
	}
}

func TestInstalledToolWithoutItsHomeIsNotAsked(t *testing.T) {
	w, o := newWorld(t)
	bin := filepath.Join(w.userHome, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	res := run(t, o)
	if len(w.asked) != 0 {
		t.Fatalf("asked %v", w.asked)
	}
	// Installed, so not "skipped", which the report shows as not installed.
	if s := res.State.Sources["claude"]; s.Status != "ok" || len(s.Homes) != 0 {
		t.Fatalf("source = %+v", s)
	}
}

func TestRunPrunesAfterRetention(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", nil)
	w.sessions("claude", h, sess("s1", "/p", 100, t0))
	run(t, o)

	// Three months later the session aged out and ann logged out for bob.
	w.now = t0.Add(state.Retention + 24*time.Hour)
	w.login("claude", h, "bob", nil)
	w.sessions("claude", h)
	res := run(t, o)
	if len(res.State.Sessions) != 0 {
		t.Fatalf("sessions = %v", res.State.Sessions)
	}
	if _, ok := res.State.Accounts[state.Key("claude", "ann")]; ok {
		t.Fatal("idle account kept past retention")
	}
	if !IsCurrent(res.State, "claude", "bob") {
		t.Fatal("current account missing")
	}
}

func TestRemembersHomesFromEnvironment(t *testing.T) {
	w, o := newWorld(t)
	w.home(t, "codex")
	extra := w.extraHome(t, "codex", "codex-work")
	w.env["CLAUDE_CONFIG_DIR"] = filepath.Join(w.userHome, ".claude") // the default: not remembered
	w.home(t, "claude")
	run(t, o)

	cfg, err := o.Dir.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Homes, map[string][]string{"codex": {extra}}) {
		t.Fatalf("remembered = %v", cfg.Homes)
	}

	// The scheduler's run has no CODEX_HOME and still reads it.
	w.env = map[string]string{}
	w.now = t0.Add(15 * time.Minute)
	res := run(t, o)
	if homes := res.State.Sources["codex"].Homes; !reflect.DeepEqual(homes, []string{filepath.Join(w.userHome, ".codex"), extra}) {
		t.Fatalf("codex homes = %v", homes)
	}
}

func TestRelativeHomeIsRememberedAbsolute(t *testing.T) {
	w, o := newWorld(t)
	root := filepath.Dir(w.userHome)
	if err := os.MkdirAll(filepath.Join(root, "rel-codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	w.env["CODEX_HOME"] = "rel-codex"
	run(t, o)
	cfg, _ := o.Dir.LoadConfig()
	got := cfg.Homes["codex"]
	if len(got) != 1 || !filepath.IsAbs(got[0]) || filepath.Base(got[0]) != "rel-codex" {
		t.Fatalf("remembered = %v", got)
	}
}

// A scheduler run, which has no CLAUDE_CONFIG_DIR, asks claude with the exact
// value an interactive run saw, even for the default home.
func TestSchedulerRunProbesClaudeWithRememberedConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	w, o := newWorld(t)
	def := w.home(t, "claude")
	bin := t.TempDir()
	record := filepath.Join(bin, "record")
	script := "#!/bin/sh\nprintf '%s|' \"${CLAUDE_CONFIG_DIR-unset}\" >> '" + record + "'\n" +
		"echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\",\"email\":\"ann\"}'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	o.Ask = nil
	o.Probe.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return filepath.Join(bin, "claude"), nil
		}
		return "", os.ErrNotExist
	}
	o.Probe.Timeout = 10 * time.Second
	exported := def + "/"
	w.env["CLAUDE_CONFIG_DIR"] = exported
	o.Probe.Environ = []string{"PATH=/usr/bin:/bin", "CLAUDE_CONFIG_DIR=" + exported}
	res := run(t, o)
	if !IsCurrent(res.State, "claude", "ann") {
		t.Fatalf("current = %v", res.State.Current)
	}

	w.env = map[string]string{}
	o.Probe.Environ = []string{"PATH=/usr/bin:/bin"}
	w.now = t0.Add(15 * time.Minute)
	run(t, o)

	body, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSuffix(string(body), "|"), exported+"|"+exported; got != want {
		t.Fatalf("claude saw CLAUDE_CONFIG_DIR %q, want %q", got, want)
	}
	cfg, _ := o.Dir.LoadConfig()
	if cfg.HomeEnv["claude"][def] != exported || len(cfg.Homes["claude"]) != 0 {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestSampleKeepsRecentQuotaWithoutGrowth(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", quota(t0, 25))
	run(t, o)
	s := lastSample(t, o)
	if len(s.Accounts) != 1 || s.Accounts[0].QuotaAt == nil || !s.Accounts[0].QuotaAt.Equal(t0) || s.Accounts[0].Windows[0].Percent != 25 {
		t.Fatalf("sample = %+v", s)
	}
	// A reading older than a day is not repeated into new samples.
	w.now = t0.Add(25 * time.Hour)
	w.readings[state.Key("claude", h)] = probe.Reading{Account: "ann"}
	run(t, o)
	if s := lastSample(t, o); len(s.Accounts) != 0 {
		t.Fatalf("sample repeats a day-old reading: %+v", s)
	}
}

// One damaged state.json must not stop every later run.
func TestDamagedStateStartsAgain(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", quota(t0, 20))
	w.sessions("claude", h, sess("s1", "/p", 100, t0))
	run(t, o)
	if err := os.WriteFile(o.Dir.Path("state.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	afterRan := false
	o.After = func(context.Context, *state.Config, *state.State) { afterRan = true }
	w.now = t0.Add(15 * time.Minute)
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("run on a damaged state.json: %v", err)
	}
	if !afterRan {
		t.Fatal("scheduler and self-update housekeeping did not run")
	}
	if !strings.Contains(res.State.LastError, "state.json") || !res.State.LastErrorAt.Equal(w.now) {
		t.Fatalf("last error = %q at %v", res.State.LastError, res.State.LastErrorAt)
	}
	// Collection starts again, like a first run.
	if a := totalsFor(t, res.State, "claude", "ann"); !a.Current || a.Tokens != tok(100) || a.Quota == nil {
		t.Fatalf("totals = %+v", a)
	}
	if _, err := os.Stat(o.Dir.Path("state.json.bad")); err != nil {
		t.Fatalf("damaged file not kept: %v", err)
	}
	if st, err := o.Dir.LoadState(); err != nil || st.Damage != "" {
		t.Fatalf("saved state = %v, damage %q", err, st.Damage)
	}
}

func TestRunFailsWhileLocked(t *testing.T) {
	_, o := newWorld(t)
	unlock, err := o.Dir.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("Run ran while another run holds the lock")
	}
}

// TestRunWaitsForTheRunItOverlaps: a run that may wait uses the result of
// the run it waited for, and collects itself when that run collected nothing
// or collected with other inputs: another release, or homes this run's
// environment names. A run someone started reads the team, so it does not
// reuse the result of a scheduled run that skipped the read.
func TestRunWaitsForTheRunItOverlaps(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		collects, newEnv, newVersion bool
		relay, readTeam              bool
		waited                       bool
	}{
		{name: "collected", collects: true, waited: true},
		{name: "did not collect"},
		{name: "new folder", collects: true, newEnv: true},
		{name: "other release", collects: true, newVersion: true},
		{name: "skipped the team read", collects: true, relay: true, waited: false},
		{name: "read the team too", collects: true, relay: true, readTeam: true, waited: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, o := newWorld(t)
			h := w.home(t, "claude")
			w.sessions("claude", h, sess("s1", "/p", 100, t0))
			if tc.relay {
				o.Relay = newRelay(t, w).client(w)
			}
			run(t, o)
			held, err := o.Dir.Lock()
			if err != nil {
				t.Fatal(err)
			}
			if tc.newEnv {
				w.extraHome(t, "codex", "work-codex")
			}
			if tc.newVersion {
				o.Version = "v1.2.4"
			}
			reads := 0
			read := o.ReadLogs
			o.ReadLogs = func(p string, homes []string, since time.Time) logs.Result {
				reads++
				return read(p, homes, since)
			}
			w.now = t0.Add(30 * time.Minute)
			o.Wait = 10 * time.Second
			waiting := 0
			o.Waiting = func() {
				waiting++
				go func() {
					if tc.collects {
						st, _ := o.Dir.LoadState()
						st.LastRunAt = t0.Add(15 * time.Minute)
						if err := o.Dir.SaveState(st); err != nil {
							t.Error(err)
						}
						if tc.readTeam {
							c, _ := LoadTeamCache(o.Dir)
							c.PulledAt = t0.Add(15 * time.Minute)
							if err := saveTeamCache(o.Dir, c); err != nil {
								t.Error(err)
							}
						}
					}
					held()
				}()
			}
			res := run(t, o)
			if waiting != 1 || res.Waited != tc.waited || (reads == 0) != tc.waited {
				t.Fatalf("waiting %d, waited %v, %d reads", waiting, res.Waited, reads)
			}
			want := w.now
			if tc.waited {
				want = t0.Add(15 * time.Minute)
			}
			if !res.State.LastRunAt.Equal(want) || !res.Doc.CollectedAt.Equal(want) {
				t.Fatalf("result of %s, doc of %s, want %s", res.State.LastRunAt, res.Doc.CollectedAt, want)
			}
			if tc.relay {
				pulled := w.now
				if tc.readTeam {
					pulled = t0.Add(15 * time.Minute)
				}
				if !res.Team.PulledAt.Equal(pulled) {
					t.Fatalf("team read at %s, want %s", res.Team.PulledAt, pulled)
				}
			}
		})
	}
}

// TestRunDoesNotBringBackARemovedHome: a folder a person removes while a run
// records another one stays removed.
func TestRunDoesNotBringBackARemovedHome(t *testing.T) {
	w, o := newWorld(t)
	old := w.extraHome(t, "codex", "old-codex")
	run(t, o)
	added := w.extraHome(t, "codex", "new-codex")
	getenv := o.Getenv
	removed := false
	o.Getenv = func(k string) string {
		if k == "CODEX_HOME" && !removed {
			removed = true
			if _, err := o.Dir.EditConfig(func(c *state.Config) error {
				c.Homes["codex"] = slices.DeleteFunc(c.Homes["codex"], func(h string) bool { return h == old })
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		return getenv(k)
	}
	w.now = t0.Add(15 * time.Minute)
	run(t, o)
	cfg, err := o.Dir.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !removed || !reflect.DeepEqual(cfg.Homes["codex"], []string{added}) {
		t.Fatalf("remembered = %v", cfg.Homes["codex"])
	}
}

func TestAfterHookSeesTheStateBeforeSave(t *testing.T) {
	_, o := newWorld(t)
	o.After = func(_ context.Context, _ *state.Config, st *state.State) {
		st.Schedule.Registered = true
	}
	run(t, o)
	st, _ := o.Dir.LoadState()
	if !st.Schedule.Registered {
		t.Fatal("After's change was not saved")
	}
}
