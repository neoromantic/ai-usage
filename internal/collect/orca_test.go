package collect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// orcaHome makes a per-account Codex home the way Orca does, under data.
func orcaHome(t *testing.T, data, id string, marked bool) string {
	t.Helper()
	h := filepath.Join(data, "codex-accounts", id, "home")
	if err := os.MkdirAll(h, 0o700); err != nil {
		t.Fatal(err)
	}
	if marked {
		if err := os.WriteFile(filepath.Join(h, ".orca-managed-home"), []byte(id+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func TestDiscoverFindsOrcaCodexHomes(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	def := filepath.Join(home, ".codex")
	if err := os.MkdirAll(def, 0o700); err != nil {
		t.Fatal(err)
	}
	data := defaultAppData("orca", home)
	b := orcaHome(t, data, "e4c8", true)
	a := orcaHome(t, data, "5b21", true)
	orcaHome(t, data, "unmarked", false)
	// Orca's own runtime home is not an account's.
	if err := os.MkdirAll(filepath.Join(data, "codex-runtime-home", "home"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Found by path alone, as under the scheduler: default home first.
	got := Discover(home, func(string) string { return "" }, nil)
	if want := []string{def, a, b}; !reflect.DeepEqual(got["codex"], want) {
		t.Fatalf("codex homes = %v, want %v", got["codex"], want)
	}
	// They are found again every run, so they are not remembered.
	if next, changed := Remember(nil, home, got); changed {
		t.Fatalf("remembered %v", next)
	}

	// Where a variable moves the app's data, its homes are found through it
	// and remembered for the scheduler.
	v := appDataEnv[runtime.GOOS]
	if v == "" {
		return
	}
	env := map[string]string{v: filepath.Join(root, "moved")}
	moved := orcaHome(t, filepath.Join(root, "moved", "orca"), "dev1", true)
	got = Discover(home, func(k string) string { return env[k] }, nil)
	if want := []string{def, a, b, moved}; !reflect.DeepEqual(got["codex"], want) {
		t.Fatalf("codex homes = %v, want %v", got["codex"], want)
	}
	next, _ := Remember(nil, home, got)
	if want := []string{moved}; !reflect.DeepEqual(next["codex"], want) {
		t.Fatalf("remembered = %v, want %v", next["codex"], want)
	}
	if got := Discover(home, func(string) string { return "" }, next); !reflect.DeepEqual(got["codex"], []string{def, a, b, moved}) {
		t.Fatalf("scheduled run homes = %v", got["codex"])
	}
}

// Each Orca account home is probed on its own. One with nobody logged in
// holds no account and is not a problem; the default home logged out still
// is, once it has been used.
func TestOrcaHomeWithoutLoginIsNoAccount(t *testing.T) {
	w, o := newWorld(t)
	def := w.home(t, "codex")
	w.sessions("codex", def, sess("s1", "/p", 100, t0))
	data := defaultAppData("orca", w.userHome)
	a := orcaHome(t, data, "5b21", true)
	b := orcaHome(t, data, "e4c8", true)
	w.login("codex", def, "sam", nil)
	w.login("codex", a, "bea", quota(t0, 20))
	w.askErr[state.Key("codex", b)] = notLoggedIn("codex")
	res := run(t, o)

	if got := res.State.Sources["codex"]; got.Status != "ok" || !reflect.DeepEqual(got.Homes, []string{def, a, b}) {
		t.Fatalf("codex source = %+v", got)
	}
	if !IsCurrent(res.State, "codex", "sam") || !IsCurrent(res.State, "codex", "bea") {
		t.Fatalf("current = %v", res.State.Current)
	}
	if q := totalsFor(t, res.State, "codex", "bea").Quota; q == nil || q.Windows[0].Percent != 20 {
		t.Fatalf("bea quota = %+v", q)
	}
	if hasTotals(res.State, "codex", UnknownAccount) {
		t.Fatal("a home with nobody logged in made an account")
	}

	w.now = t0.Add(15 * time.Minute)
	w.askErr[state.Key("codex", def)] = notLoggedIn("codex")
	if got := run(t, o).State.Sources["codex"]; got.Status != "partial" {
		t.Fatalf("default home logged out: source = %+v", got)
	}
}

// weekly is a quota whose seven-day window resets at reset.
func weekly(at time.Time, pct float64, reset time.Time) *probe.Quota {
	return &probe.Quota{At: at, Source: "harness", Windows: []snapshot.Window{
		{Name: "5h", Percent: 1, Minutes: 300, ResetsAt: ptr(at.Add(2 * time.Hour))},
		{Name: "7d", Percent: pct, Minutes: 10080, ResetsAt: &reset},
	}}
}

func ptr(t time.Time) *time.Time { return &t }

// mirrored is a session whose rollout Orca linked into every home, with the
// weekly window its newest token_count recorded.
func mirrored(id string, in int64, at time.Time, reset *time.Time, homes ...string) logs.Session {
	s := sess(id, "/p", in, at)
	s.Homes = homes
	if reset != nil {
		// The five-hour window resets at another time for every account; only
		// the longest window is compared.
		s.Limits = &logs.Limits{ObservedAt: at, Windows: []snapshot.Window{
			{Name: "5h", Minutes: 300, ResetsAt: ptr(at.Add(3 * time.Hour))},
			{Name: "7d", Minutes: 10080, ResetsAt: reset},
		}}
	}
	return s
}

// A rollout linked into several homes goes to the account whose weekly
// window its rate limits show, and otherwise stays with the first home.
func TestMirroredSessionFollowsItsRateLimits(t *testing.T) {
	w, o := newWorld(t)
	def := w.home(t, "codex")
	data := defaultAppData("orca", w.userHome)
	a := orcaHome(t, data, "5b21", true)
	b := orcaHome(t, data, "e4c8", true)
	samReset, beaReset := t0.Add(50*time.Hour), t0.Add(100*time.Hour)
	w.login("codex", def, "sam", weekly(t0, 30, samReset))
	w.login("codex", a, "bea", weekly(t0, 60, beaReset))
	w.login("codex", b, "sam", weekly(t0, 30, samReset))
	near := beaReset.Add(20 * time.Second)
	w.sessions("codex", def,
		mirrored("bea-ran", 100, t0, &near, def, a, b),
		mirrored("sam-ran", 200, t0, &samReset, def, a, b),
		mirrored("no-limits", 300, t0, nil, def, a, b),
		mirrored("other-week", 400, t0, ptr(t0.Add(-100*time.Hour)), def, a, b),
		// Only in the default home: its limits are not consulted.
		mirrored("default-only", 500, t0, &near),
	)
	res := run(t, o)
	if got := totalsFor(t, res.State, "codex", "bea"); got.Tokens != tok(100) || got.Sessions != 1 {
		t.Fatalf("bea = %+v", got)
	}
	if got := totalsFor(t, res.State, "codex", "sam"); got.Tokens != tok(200+300+400+500) || got.Sessions != 4 {
		t.Fatalf("sam = %+v", got)
	}

	// Two accounts whose weeks reset together cannot be told apart.
	w.now = t0.Add(15 * time.Minute)
	w.login("codex", a, "bea", weekly(w.now, 61, samReset))
	w.sessions("codex", def, mirrored("tie", 50, w.now, &samReset, def, a))
	res = run(t, o)
	if by := res.State.Sessions[state.Key("codex", "tie")].By; !reflect.DeepEqual(by, map[string]snapshot.Tokens{"sam": tok(50)}) {
		t.Fatalf("tie = %+v", by)
	}
}

// History counted before any Orca home answered goes to the account whose
// weekly window the rollout shows, as new usage does, not to the first home's.
func TestMirroredUnknownHistoryFollowsItsRateLimits(t *testing.T) {
	w, o := newWorld(t)
	def := w.home(t, "codex")
	data := defaultAppData("orca", w.userHome)
	a := orcaHome(t, data, "5b21", true)
	b := orcaHome(t, data, "e4c8", true)
	for _, h := range []string{def, a, b} {
		w.askErr[state.Key("codex", h)] = errors.New("no answer")
	}
	samReset, beaReset := t0.Add(50*time.Hour), t0.Add(100*time.Hour)
	near := beaReset.Add(20 * time.Second)
	w.sessions("codex", def,
		mirrored("bea-ran", 100, t0, &near, def, a, b),
		mirrored("sam-ran", 200, t0, &samReset, def, a, b),
	)
	run(t, o)

	// The default home says nobody is logged in; the Orca homes name their
	// accounts for the first time.
	w.now = t0.Add(15 * time.Minute)
	for _, h := range []string{def, a, b} {
		delete(w.askErr, state.Key("codex", h))
	}
	w.askErr[state.Key("codex", def)] = notLoggedIn("codex")
	w.readings[state.Key("codex", def)] = probe.Reading{}
	w.login("codex", a, "bea", weekly(w.now, 60, beaReset))
	w.login("codex", b, "sam", weekly(w.now, 30, samReset))
	res := run(t, o)
	if by := res.State.Sessions[state.Key("codex", "bea-ran")].By; !reflect.DeepEqual(by, map[string]snapshot.Tokens{"bea": tok(100)}) {
		t.Fatalf("bea-ran = %+v", by)
	}
	if by := res.State.Sessions[state.Key("codex", "sam-ran")].By; !reflect.DeepEqual(by, map[string]snapshot.Tokens{"sam": tok(200)}) {
		t.Fatalf("sam-ran = %+v", by)
	}
}

// The homes of one provider are asked at once, so a harness that does not
// answer in one of several Orca homes costs its timeout once, not per home.
func TestHomesAreAskedTogether(t *testing.T) {
	w, o := newWorld(t)
	def := w.home(t, "codex")
	data := defaultAppData("orca", w.userHome)
	homes := []string{def, orcaHome(t, data, "5b21", true), orcaHome(t, data, "e4c8", true)}
	for i, h := range homes {
		w.login("codex", h, []string{"sam", "bea", "kim"}[i], nil)
	}
	var arrived sync.WaitGroup
	arrived.Add(len(homes))
	all := make(chan struct{})
	go func() { arrived.Wait(); close(all) }()
	ask := o.Ask
	o.Ask = func(ctx context.Context, p, home string, lastUse time.Time) (probe.Reading, error) {
		if p == "codex" {
			arrived.Done()
			select {
			case <-all:
			case <-time.After(5 * time.Second):
				return probe.Reading{}, errors.New("homes were asked one at a time")
			}
		}
		return ask(ctx, p, home, lastUse)
	}
	res := run(t, o)
	if got := res.State.Sources["codex"]; got.Status != "ok" {
		t.Fatalf("codex source = %+v", got)
	}
	for _, l := range []string{"sam", "bea", "kim"} {
		if !IsCurrent(res.State, "codex", l) {
			t.Fatalf("%s not current: %v", l, res.State.Current)
		}
	}
}

// A probe that panics is its source's error, as a parser that panics is.
func TestPanicInProbeIsItsSourceError(t *testing.T) {
	w, o := newWorld(t)
	ch := w.home(t, "claude")
	xh := w.home(t, "codex")
	w.login("claude", ch, "ann", nil)
	w.login("codex", xh, "bob", nil)
	ask := o.Ask
	o.Ask = func(ctx context.Context, p, home string, lastUse time.Time) (probe.Reading, error) {
		if p == "claude" {
			panic("boom")
		}
		return ask(ctx, p, home, lastUse)
	}
	res := run(t, o)
	if src := res.State.Sources["claude"]; src.Status != "error" || !strings.Contains(src.Error, "stopped by a bug") {
		t.Fatalf("claude source = %+v", src)
	}
	if src := res.State.Sources["codex"]; src.Status != "ok" || !IsCurrent(res.State, "codex", "bob") {
		t.Fatalf("codex source = %+v", src)
	}
}
