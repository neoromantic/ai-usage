package collect

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

var t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// world stands in for the harness homes and the harnesses themselves.
type world struct {
	mu       sync.Mutex
	now      time.Time
	userHome string
	env      map[string]string
	logs     map[string]logs.Result // provider+home
	readErr  map[string]error
	readings map[string]probe.Reading // provider+home
	askErr   map[string]error
	asked    []string
}

func newWorld(t *testing.T) (*world, Options) {
	t.Helper()
	root := t.TempDir()
	w := &world{
		now:      t0,
		userHome: filepath.Join(root, "home"),
		env:      map[string]string{},
		logs:     map[string]logs.Result{},
		readErr:  map[string]error{},
		readings: map[string]probe.Reading{},
		askErr:   map[string]error{},
	}
	if err := os.MkdirAll(w.userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	o := Options{
		Dir:      state.Dir(filepath.Join(root, "ai-usage")),
		Version:  "v1.2.3",
		Getenv:   func(k string) string { return w.env[k] },
		UserHome: w.userHome,
		Hostname: "workbox",
		OSUser:   "sam",
		Probe: probe.Env{
			LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
			HomeDir:  w.userHome,
		},
		Now:      func() time.Time { return w.now },
		ReadLogs: w.read,
		Ask:      w.ask,
	}
	return w, o
}

// home makes a provider's default home and returns its path.
func (w *world) home(t *testing.T, p string) string {
	t.Helper()
	h := filepath.Join(w.userHome, "."+p)
	if err := os.MkdirAll(h, 0o700); err != nil {
		t.Fatal(err)
	}
	return h
}

// extraHome makes a non-default home named through the provider's variable.
func (w *world) extraHome(t *testing.T, p, name string) string {
	t.Helper()
	h := filepath.Join(filepath.Dir(w.userHome), name)
	if err := os.MkdirAll(h, 0o700); err != nil {
		t.Fatal(err)
	}
	w.env[homeEnv[p]] = h
	return h
}

// read stands in for logs.ReadHomes: each home's logs, tagged with the home,
// and a session kept in two homes once, from the larger copy.
func (w *world) read(p string, homes []string, since time.Time) logs.Result {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := logs.Result{Homes: map[string]logs.HomeRead{}}
	at := map[string]int{}
	for _, home := range homes {
		k := state.Key(p, home)
		r := w.logs[k]
		out.Homes[home] = logs.HomeRead{Err: w.readErr[k], Malformed: r.Malformed, Unreadable: r.Unreadable, Limits: r.Limits}
		for _, s := range r.Sessions {
			s.Home = home
			if i, ok := at[s.ID]; ok {
				if s.Tokens.Total() > out.Sessions[i].Tokens.Total() {
					out.Sessions[i] = s
				}
				continue
			}
			at[s.ID] = len(out.Sessions)
			out.Sessions = append(out.Sessions, s)
		}
	}
	return out
}

func (w *world) ask(_ context.Context, p, home string) (probe.Reading, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	k := state.Key(p, home)
	w.asked = append(w.asked, k)
	return w.readings[k], w.askErr[k]
}

func (w *world) sessions(p, home string, ss ...logs.Session) {
	r := w.logs[state.Key(p, home)]
	r.Sessions = ss
	w.logs[state.Key(p, home)] = r
}

func (w *world) login(p, home, account string, q *probe.Quota) {
	w.readings[state.Key(p, home)] = probe.Reading{Account: account, Quota: q}
}

func sess(id, project string, input int64, updated time.Time) logs.Session {
	return logs.Session{ID: id, Project: project, Tokens: snapshot.Tokens{Input: input, Output: input / 10}, Updated: updated}
}

func quota(at time.Time, pct ...float64) *probe.Quota {
	q := &probe.Quota{At: at, Source: "harness"}
	names := []string{"5h", "7d"}
	for i, p := range pct {
		reset := at.Add(time.Duration(i+1) * 5 * time.Hour)
		q.Windows = append(q.Windows, snapshot.Window{Name: names[i], Percent: p, ResetsAt: &reset})
	}
	return q
}

func run(t *testing.T, o Options) *Result {
	t.Helper()
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func totalsFor(t *testing.T, st *state.State, provider, label string) AccountTotals {
	t.Helper()
	for _, a := range Totals(st) {
		if a.Provider == provider && a.Label == label {
			return a
		}
	}
	t.Fatalf("no totals for %s %s in %+v", provider, label, Totals(st))
	return AccountTotals{}
}

func hasTotals(st *state.State, provider, label string) bool {
	for _, a := range Totals(st) {
		if a.Provider == provider && a.Label == label {
			return true
		}
	}
	return false
}

func lastSample(t *testing.T, o Options) state.Sample {
	t.Helper()
	ss, err := o.Dir.LoadSamples(time.Time{})
	if err != nil || len(ss) == 0 {
		t.Fatalf("samples: %v, %v", ss, err)
	}
	return ss[len(ss)-1]
}

func growthOf(s state.Sample, provider, label string) snapshot.Tokens {
	for _, a := range s.Accounts {
		if a.Provider == provider && a.Label == label {
			return a.Growth
		}
	}
	return snapshot.Tokens{}
}

func tok(in int64) snapshot.Tokens { return snapshot.Tokens{Input: in, Output: in / 10} }

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
	w.askErr[k] = notLoggedIn("codex")
	w.readings[k] = probe.Reading{}
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
	w.askErr[state.Key("claude", h)] = notLoggedIn("claude")
	w.readings[state.Key("claude", h)] = probe.Reading{}
	if got := run(t, o).State.Sources["claude"]; got.Status != "ok" || got.Error != "" {
		t.Fatalf("claude source = %+v", got)
	}
	w.now = t0.Add(15 * time.Minute)
	w.sessions("claude", h, sess("s1", "/p", 100, w.now))
	if got := run(t, o).State.Sources["claude"]; got.Status != "partial" {
		t.Fatalf("a used home logged out: source = %+v", got)
	}
}

func TestLoggedOutHomeDoesNotClaimLater(t *testing.T) {
	// A home that said nobody is logged in has answered; the usage counted
	// then is no one's, whoever logs in afterwards.
	w, o := newWorld(t)
	h := w.home(t, "codex")
	k := state.Key("codex", h)
	w.askErr[k] = notLoggedIn("codex")
	w.readings[k] = probe.Reading{}
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
	w.askErr[k] = notLoggedIn("codex")
	w.readings[k] = probe.Reading{}
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

// A ledger from before homes were recorded, whose harness named an account,
// claims nothing.
func TestLegacyLedgerWithANamedAccountDoesNotClaim(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "codex")
	k := state.Key("codex", h)
	w.login("codex", h, "ann@x", nil)
	w.sessions("codex", h, sess("s1", "/p", 500, t0))
	run(t, o)
	w.askErr[k] = errors.New("no answer")
	w.now = t0.Add(15 * time.Minute)
	w.sessions("codex", h, sess("s1", "/p", 500, t0), sess("s2", "/q", 300, w.now))
	delete(w.readings, k)
	run(t, o)
	st, err := o.Dir.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	st.Answered = nil
	if err := o.Dir.SaveState(st); err != nil {
		t.Fatal(err)
	}
	w.now = t0.Add(30 * time.Minute)
	delete(w.askErr, k)
	w.login("codex", h, "bea@x", nil)
	res := run(t, o)
	if b := totalsFor(t, res.State, "codex", "bea@x"); !b.Tokens.Zero() {
		t.Fatalf("bea = %+v", b)
	}
}

func TestProbeErrorsNameTheirHome(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "codex")
	x := w.extraHome(t, "codex", "work-codex")
	w.login("codex", h, "ann@x", nil)
	w.login("codex", x, "ann@x", nil)
	w.askErr[state.Key("codex", x)] = errors.New("codex account/rateLimits/read: 401 Unauthorized")
	res := run(t, o)
	if got := res.State.Sources["codex"].Error; !strings.Contains(got, x) || !strings.Contains(got, "401 Unauthorized") {
		t.Fatalf("error = %q, want it to name %s", got, x)
	}

	// The same error from every home is said once.
	w.now = t0.Add(15 * time.Minute)
	w.askErr[state.Key("codex", h)] = w.askErr[state.Key("codex", x)]
	res = run(t, o)
	if got := res.State.Sources["codex"].Error; strings.Count(got, "401 Unauthorized") != 1 || strings.Contains(got, x) {
		t.Fatalf("error = %q", got)
	}
}

// notLoggedIn is how a probe says the harness answered and nobody is logged in.
type notLoggedIn string

func (e notLoggedIn) Error() string { return string(e) + ": not logged in" }
func (notLoggedIn) LoggedOut() bool { return true }

func TestHarnessThatSaysLoggedOutEndsCurrent(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "codex")
	k := state.Key("codex", h)
	w.login("codex", h, "ann", quota(t0, 30))
	w.sessions("codex", h, sess("s1", "/p", 100, t0))
	run(t, o)

	// codex logout: account/read answers account:null. The probe's error may
	// carry other problems too.
	w.now = t0.Add(15 * time.Minute)
	w.readings[k] = probe.Reading{}
	w.askErr[k] = fmt.Errorf("codex app-server: slow; %w", notLoggedIn("codex"))
	w.sessions("codex", h, sess("s1", "/p", 130, w.now))
	res := run(t, o)

	if IsCurrent(res.State, "codex", "ann") {
		t.Fatalf("ann still current after the harness said nobody is logged in: %v", res.State.Current)
	}
	a := totalsFor(t, res.State, "codex", "ann")
	if a.Current || a.Tokens != tok(100) || a.Quota == nil || a.Quota.Windows[0].Percent != 30 {
		t.Fatalf("previous account = %+v", a)
	}
	if u := totalsFor(t, res.State, "codex", UnknownAccount); u.Current || u.Tokens != tok(30) {
		t.Fatalf("growth after logout = %+v", u)
	}
	if src := res.State.Sources["codex"]; src.Status != "partial" || !strings.Contains(src.Error, "codex: not logged in") {
		t.Fatalf("source = %+v", src)
	}
	for _, d := range res.Doc.Accounts {
		if d.Current {
			t.Fatalf("snapshot still publishes a current account: %+v", d)
		}
	}

	// A later probe that does not answer does not bring ann back.
	w.now = t0.Add(30 * time.Minute)
	w.askErr[k] = errors.New("codex app-server: not answering")
	res = run(t, o)
	if IsCurrent(res.State, "codex", "ann") {
		t.Fatal("a probe that did not answer brought the logged-out account back")
	}
}

func codexLimits(at time.Time, pct float64) *logs.Limits {
	reset := at.Add(5 * time.Hour)
	return &logs.Limits{ObservedAt: at, Plan: "plus", Windows: []snapshot.Window{{Name: "5h", Percent: pct, ResetsAt: &reset, Minutes: 300}}}
}

func TestCodexLogLimitsAreOnlyAFallback(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "codex")
	k := state.Key("codex", h)

	// The probe answers with a quota: the log reading is ignored.
	w.login("codex", h, "ann", quota(t0, 10))
	w.logs[k] = logs.Result{Limits: codexLimits(t0.Add(time.Minute), 99)}
	res := run(t, o)
	q := totalsFor(t, res.State, "codex", "ann").Quota
	if q.Source != "harness" || q.Windows[0].Percent != 10 {
		t.Fatalf("probe quota replaced by log: %+v", q)
	}

	// No quota from the probe: a log reading newer than the last run is used.
	w.now = t0.Add(15 * time.Minute)
	w.login("codex", h, "ann", nil)
	w.logs[k] = logs.Result{Limits: codexLimits(t0.Add(10*time.Minute), 20)}
	res = run(t, o)
	a := totalsFor(t, res.State, "codex", "ann")
	if a.Quota.Source != "log" || a.Quota.Windows[0].Percent != 20 || a.Plan != "plus" {
		t.Fatalf("log fallback not used: %+v plan %q", a.Quota, a.Plan)
	}
}

func TestCodexLogLimitsFromBeforeASwitchStayWithTheOldAccount(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "codex")
	k := state.Key("codex", h)
	w.login("codex", h, "old", nil)
	w.logs[k] = logs.Result{Limits: codexLimits(t0.Add(-time.Hour), 70)}
	run(t, o)

	// Logged into another account; the logs still hold old's last limits.
	w.now = t0.Add(15 * time.Minute)
	w.login("codex", h, "new", nil)
	res := run(t, o)
	if q := totalsFor(t, res.State, "codex", "new").Quota; q != nil {
		t.Fatalf("new account took the old account's log reading: %+v", q)
	}
	if q := totalsFor(t, res.State, "codex", "old").Quota; q == nil || q.Windows[0].Percent != 70 {
		t.Fatalf("old account quota = %+v", q)
	}

	// The next run with the same stale log still does not credit it.
	w.now = t0.Add(30 * time.Minute)
	res = run(t, o)
	if q := totalsFor(t, res.State, "codex", "new").Quota; q != nil {
		t.Fatalf("stale log reading credited a run later: %+v", q)
	}

	// New usage writes new limits: those are the new account's.
	w.logs[k] = logs.Result{Limits: codexLimits(t0.Add(40*time.Minute), 3)}
	w.now = t0.Add(45 * time.Minute)
	res = run(t, o)
	if q := totalsFor(t, res.State, "codex", "new").Quota; q == nil || q.Windows[0].Percent != 3 {
		t.Fatalf("fresh log reading not credited: %+v", q)
	}
	if q := totalsFor(t, res.State, "codex", "old").Quota; q.Windows[0].Percent != 70 {
		t.Fatalf("old account quota changed: %+v", q)
	}
}

func TestLogLimitsNeverGoToTheUnknownAccount(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "codex")
	k := state.Key("codex", h)
	w.askErr[k] = errors.New("codex app-server: not answering")
	w.logs[k] = logs.Result{Limits: codexLimits(t0, 50), Sessions: []logs.Session{sess("c1", "/p", 10, t0)}}
	res := run(t, o)
	if q := totalsFor(t, res.State, "codex", UnknownAccount).Quota; q != nil {
		t.Fatalf("unknown account got a quota: %+v", q)
	}
}

// rejectedSession is a Claude session with a request refused at at because
// its 5h window was full until resets.
func rejectedSession(id string, at, resets time.Time) logs.Session {
	s := sess(id, "/p", 100, at)
	s.Rejected = []*logs.Limits{{ObservedAt: at, Windows: []snapshot.Window{{Name: "5h", Percent: 100, ResetsAt: &resets, Minutes: 300}}}}
	return s
}

func TestClaudeRejectionReadsItsWindowFull(t *testing.T) {
	cached := quota(t0.Add(-2*time.Hour), 40, 20)
	tests := []struct {
		name       string
		at, resets time.Time
		full       bool
	}{
		{"in force and newer than the reading", t0.Add(-30 * time.Minute), t0.Add(time.Hour), true},
		{"reset since", t0.Add(-30 * time.Minute), t0.Add(-time.Minute), false},
		{"older than the reading", t0.Add(-3 * time.Hour), t0.Add(time.Hour), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, o := newWorld(t)
			h := w.home(t, "claude")
			w.login("claude", h, "ann", cached)
			w.sessions("claude", h, rejectedSession("s1", tc.at, tc.resets))
			res := run(t, o)
			q := totalsFor(t, res.State, "claude", "ann").Quota
			if !tc.full {
				if q.Source != "harness" || !q.At.Equal(cached.At) || len(q.Windows) != 2 {
					t.Fatalf("quota = %+v, want the cached reading", q)
				}
				return
			}
			// The refused window alone, as of the refusal: the cached 7d
			// reading is older and does not ride along.
			want := snapshot.Window{Name: "5h", Percent: 100, ResetsAt: &tc.resets, Minutes: 300}
			if q.Source != "rejection" || !q.At.Equal(tc.at) || !reflect.DeepEqual(q.Windows, []snapshot.Window{want}) {
				t.Fatalf("quota = %+v, want %+v at %v", q, want, tc.at)
			}
			if d := res.Doc.Accounts[0]; d.QuotaAt == nil || !d.QuotaAt.Equal(tc.at) || d.Windows[0].Percent != 100 {
				t.Fatalf("snapshot = %+v", d)
			}
		})
	}
}

func TestClaudeRejectionHoldsUntilItsWindowResets(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	cached := quota(t0.Add(-2*time.Hour), 40, 20)
	w.login("claude", h, "ann", cached)
	w.sessions("claude", h, rejectedSession("s1", t0.Add(-30*time.Minute), t0.Add(time.Hour)))
	run(t, o)

	// The older cached reading does not replace it while the window is full.
	w.now = t0.Add(15 * time.Minute)
	res := run(t, o)
	if q := totalsFor(t, res.State, "claude", "ann").Quota; q.Source != "rejection" {
		t.Fatalf("quota = %+v, want the rejection", q)
	}

	// Once it resets, the cached reading of the other windows comes back.
	w.now = t0.Add(time.Hour + 15*time.Minute)
	res = run(t, o)
	if q := totalsFor(t, res.State, "claude", "ann").Quota; q.Source != "harness" || !q.At.Equal(cached.At) {
		t.Fatalf("quota = %+v, want the cached reading", q)
	}
}

func TestClaudeRejectionOfAWeeklyWindowOutlastsANewer5h(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", quota(t0.Add(-5*time.Hour), 40, 20))
	s := rejectedSession("s1", t0.Add(-2*time.Hour), t0.Add(time.Hour))
	week := t0.Add(96 * time.Hour)
	opus := &logs.Limits{ObservedAt: t0.Add(-3 * time.Hour), Windows: []snapshot.Window{{Name: "7d Opus", Percent: 100, ResetsAt: &week, Minutes: 10080}}}
	s.Rejected = append(s.Rejected, opus)
	w.sessions("claude", h, s)
	res := run(t, o)
	if q := totalsFor(t, res.State, "claude", "ann").Quota; !q.At.Equal(t0.Add(-2 * time.Hour)) {
		t.Fatalf("quota = %+v, want the newest refusal", q)
	}

	// Once the 5h window resets, the weekly one still holds.
	w.now = t0.Add(90 * time.Minute)
	res = run(t, o)
	if q := totalsFor(t, res.State, "claude", "ann").Quota; q.Source != "rejection" || !q.At.Equal(opus.ObservedAt) || q.Windows[0].Name != "7d Opus" {
		t.Fatalf("quota = %+v, want the 7d Opus refusal", q)
	}
}

func TestClaudeRejectionGoesToTheSessionsAccount(t *testing.T) {
	w, o := newWorld(t)
	def := w.home(t, "claude")
	work := w.extraHome(t, "claude", "claude-work")
	w.login("claude", def, "ann", nil)
	w.login("claude", work, "bob", nil)
	w.sessions("claude", def, sess("s1", "/p", 100, t0))
	w.sessions("claude", work, rejectedSession("s2", t0.Add(-10*time.Minute), t0.Add(time.Hour)))
	res := run(t, o)
	if q := totalsFor(t, res.State, "claude", "ann").Quota; q != nil {
		t.Fatalf("ann got another session's rejection: %+v", q)
	}
	if q := totalsFor(t, res.State, "claude", "bob").Quota; q == nil || q.Source != "rejection" {
		t.Fatalf("bob quota = %+v", q)
	}

	// A session no run read before, as in a folder added since, is all
	// ann's in the ledger, so its refusal from before the previous run is
	// hers. Bob starts a session, is refused, and switches to carl. The
	// ledger gives carl that session, but the refusal is bob's, now and at
	// later runs.
	w.now = t0.Add(15 * time.Minute)
	w.login("claude", work, "carl", nil)
	w.sessions("claude", def, sess("s1", "/p", 100, t0), rejectedSession("s3", t0.Add(-20*time.Minute), t0.Add(time.Hour)))
	w.sessions("claude", work, rejectedSession("s2", t0.Add(-10*time.Minute), t0.Add(time.Hour)), rejectedSession("s4", t0.Add(5*time.Minute), t0.Add(time.Hour)))
	for range 2 {
		res = run(t, o)
		if q := totalsFor(t, res.State, "claude", "carl").Quota; q != nil {
			t.Fatalf("carl got bob's refusal at %v: %+v", w.now, q)
		}
		w.now = w.now.Add(15 * time.Minute)
	}
	if q := totalsFor(t, res.State, "claude", "ann").Quota; q == nil || !q.At.Equal(t0.Add(-20*time.Minute)) {
		t.Fatalf("ann quota = %+v, want the refusal in s3", q)
	}
}

func TestHermesAttributesByBillingProvider(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "hermes")
	bill := func(id, acct string, in int64, at time.Time) logs.Session {
		s := sess(id, "/h", in, at)
		s.Account = acct
		return s
	}
	w.sessions("hermes", h,
		bill("h1", "openrouter", 100, t0.Add(-3*time.Hour)),
		bill("h2", "anthropic", 50, t0.Add(-2*time.Hour)),
		bill("h3", "", 7, t0.Add(-4*time.Hour)),
	)
	res := run(t, o)
	for label, want := range map[string]snapshot.Tokens{"openrouter": tok(100), "anthropic": tok(50), UnknownAccount: tok(7)} {
		if got := totalsFor(t, res.State, "hermes", label).Tokens; got != want {
			t.Fatalf("hermes %s tokens = %+v, want %+v", label, got, want)
		}
	}
	if !IsCurrent(res.State, "hermes", "anthropic") || IsCurrent(res.State, "hermes", "openrouter") {
		t.Fatalf("current = %v", res.State.Current)
	}

	// h1 switches billing to anthropic and grows; it becomes the newest.
	w.now = t0.Add(15 * time.Minute)
	w.sessions("hermes", h,
		bill("h1", "anthropic", 160, w.now.Add(-time.Minute)),
		bill("h2", "anthropic", 50, t0.Add(-2*time.Hour)),
		bill("h3", "", 7, t0.Add(-4*time.Hour)),
	)
	res = run(t, o)
	if got := res.State.Sessions[state.Key("hermes", "h1")].By; got["openrouter"] != tok(100) || got["anthropic"] != tok(60) {
		t.Fatalf("h1 split = %+v", got)
	}
	// The newest session bills anthropic now. Its earlier openrouter share
	// must not make openrouter current, on any run.
	for i := 0; i < 10; i++ {
		w.now = w.now.Add(15 * time.Minute)
		res = run(t, o)
		if !IsCurrent(res.State, "hermes", "anthropic") || IsCurrent(res.State, "hermes", "openrouter") {
			t.Fatalf("run %d: current = %v", i, res.State.Current)
		}
	}
	if src := res.State.Sources["hermes"]; src.Status != "ok" {
		t.Fatalf("hermes source = %+v", src)
	}
}

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
func TestPanicInOneSourceDoesNotStopOthers(t *testing.T) {
	w, o := newWorld(t)
	ch := w.home(t, "claude")
	xh := w.home(t, "codex")
	w.login("claude", ch, "ann", nil)
	w.login("codex", xh, "bob", nil)
	w.sessions("codex", xh, sess("c1", "/p", 10, t0))
	read := o.ReadLogs
	o.ReadLogs = func(p string, homes []string, since time.Time) logs.Result {
		if p == "claude" {
			var m map[string]int
			m["boom"]++ // a nil map write, as a parser bug would
		}
		return read(p, homes, since)
	}
	res := run(t, o)
	if src := res.State.Sources["claude"]; src.Status != "error" || !strings.Contains(src.Error, "stopped by a bug") {
		t.Fatalf("claude source = %+v", src)
	}
	if src := res.State.Sources["codex"]; src.Status != "ok" || totalsFor(t, res.State, "codex", "bob").Tokens != tok(10) {
		t.Fatalf("codex source = %+v", src)
	}
	if !strings.Contains(res.State.LastError, "stopped by a bug") {
		t.Fatalf("last error = %q", res.State.LastError)
	}
	st, err := o.Dir.LoadState()
	if err != nil || st.Sources["codex"].Status != "ok" {
		t.Fatalf("saved state = %+v, %v", st, err)
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

func TestIncompleteReadDoesNotCountAgainLater(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	k := state.Key("claude", h)
	w.login("claude", h, "ann", nil)
	w.sessions("claude", h, sess("s1", "/p", 150, t0))
	run(t, o)

	// A sub-agent file cannot be opened this run, so s1 reads lower.
	w.now = t0.Add(15 * time.Minute)
	w.logs[k] = logs.Result{Sessions: []logs.Session{sess("s1", "/p", 100, t0)}, Unreadable: 1}
	run(t, o)

	w.now = t0.Add(30 * time.Minute)
	w.logs[k] = logs.Result{Sessions: []logs.Session{sess("s1", "/p", 150, t0)}}
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
	if o.Probe.Find("grok") {
		t.Skip("a grok binary is installed system-wide on this machine")
	}
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
	for _, p := range Providers {
		if o.Probe.Find(p) {
			t.Skipf("a %s binary is installed system-wide on this machine", p)
		}
	}
	res := run(t, o)
	for _, p := range Providers {
		if res.State.Sources[p].Status != "skipped" {
			t.Fatalf("%s = %+v", p, res.State.Sources[p])
		}
	}
	if !res.State.LastSuccessAt.Equal(t0) || res.State.LastError != "" {
		t.Fatalf("state = %+v", res.State)
	}
	if len(res.Doc.Accounts) != 0 || len(res.Doc.Sources) != len(Providers) {
		t.Fatalf("doc = %+v", res.Doc)
	}
}

func TestInstalledBinaryWithoutHomeIsStillAsked(t *testing.T) {
	w, o := newWorld(t)
	bin := filepath.Join(w.userHome, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	def := filepath.Join(w.userHome, ".claude") // not created
	w.login("claude", def, "ann", quota(t0, 5))
	res := run(t, o)
	a := totalsFor(t, res.State, "claude", "ann")
	if !a.Current || a.Quota == nil {
		t.Fatalf("totals = %+v", a)
	}
	if res.State.Sources["claude"].Status != "ok" {
		t.Fatalf("source = %+v", res.State.Sources["claude"])
	}

	// It says nobody is logged in: ann is no longer current.
	w.now = t0.Add(15 * time.Minute)
	w.readings[state.Key("claude", def)] = probe.Reading{}
	w.askErr[state.Key("claude", def)] = notLoggedIn("claude")
	res = run(t, o)
	if IsCurrent(res.State, "claude", "ann") {
		t.Fatalf("current = %v", res.State.Current)
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

// Windows out of range, or more of them than a snapshot holds, are kept to
// what the state and the snapshot can hold.
func TestBadWindowValuesAreClamped(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	q := &probe.Quota{At: t0, Source: "harness", Windows: []snapshot.Window{
		{Name: "", Percent: math.NaN()},
		{Name: "weekly ✓", Percent: math.Inf(1), Minutes: -3},
		{Name: "x", Percent: -5, Minutes: snapshot.MaxWindowMinutes + 1},
	}}
	for len(q.Windows) <= snapshot.MaxWindows {
		q.Windows = append(q.Windows, snapshot.Window{Name: "w", Percent: 1})
	}
	w.login("claude", h, "ann", q)
	res := run(t, o) // a NaN would fail to save the state
	if got := totalsFor(t, res.State, "claude", "ann").Quota.Windows; len(got) != snapshot.MaxWindows {
		t.Fatalf("%d windows kept", len(got))
	}
	if err := res.Doc.Validate(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Dir.LoadState(); err != nil {
		t.Fatal(err)
	}
}

// A clock that ran ahead and was put back must not freeze the quota.
func TestQuotaFromAClockThatRanAheadIsReplaced(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "codex")
	w.now = t0.Add(24 * time.Hour)
	w.login("codex", h, "ann", quota(w.now, 90))
	run(t, o)

	w.now = t0.Add(15 * time.Minute)
	w.login("codex", h, "ann", quota(w.now, 5))
	res := run(t, o)
	q := totalsFor(t, res.State, "codex", "ann").Quota
	if q.Windows[0].Percent != 5 || !q.At.Equal(w.now) {
		t.Fatalf("quota after the clock was put back = %+v", q)
	}

	// A reading stamped ahead of this clock counts as taken now.
	w.now = t0.Add(30 * time.Minute)
	w.login("codex", h, "ann", quota(w.now.Add(time.Hour), 7))
	res = run(t, o)
	if q := totalsFor(t, res.State, "codex", "ann").Quota; q.Windows[0].Percent != 7 || !q.At.Equal(w.now) {
		t.Fatalf("future reading = %+v", q)
	}
}

// A unit mix-up upstream (milliseconds read as seconds) must not make the
// state unsavable and stop every run.
func TestUnbelievableQuotaTimesDoNotStopRuns(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "codex")
	far := time.Unix(1_800_000_000_000, 0) // year ~59000: JSON cannot hold it
	past := time.Date(1970, 1, 1, 0, 0, 1, 0, time.UTC)
	soon := t0.Add(5 * time.Hour)
	w.login("codex", h, "ann", &probe.Quota{At: t0, Source: "harness", Windows: []snapshot.Window{
		{Name: "5h", Percent: 10, ResetsAt: &far},
		{Name: "7d", Percent: 20, ResetsAt: &past},
		{Name: "ok", Percent: 30, ResetsAt: &soon},
	}})
	gh := w.home(t, "grok")
	w.login("grok", gh, "gina", &probe.Quota{At: time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC), Source: "log", Windows: []snapshot.Window{{Name: "5h", Percent: 1}}})
	for i := 0; i < 2; i++ {
		w.now = t0.Add(time.Duration(i) * 15 * time.Minute)
		res, err := Run(context.Background(), o)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		ws := totalsFor(t, res.State, "codex", "ann").Quota.Windows
		if ws[0].ResetsAt != nil || ws[1].ResetsAt != nil || ws[2].ResetsAt == nil || !ws[2].ResetsAt.Equal(soon) {
			t.Fatalf("windows = %+v", ws)
		}
		if q := totalsFor(t, res.State, "grok", "gina").Quota; q != nil {
			t.Fatalf("a reading older than retention was kept: %+v", q)
		}
	}
	if ss, err := o.Dir.LoadSamples(time.Time{}); err != nil || len(ss) != 2 {
		t.Fatalf("samples = %d, %v", len(ss), err)
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
		}},
	}
	prune(st, now)
	s := st.Sessions[state.Key("claude", "s1")]
	if s == nil || s.Seen != tok(150) || !reflect.DeepEqual(s.By, map[string]snapshot.Tokens{"new": tok(50)}) {
		t.Fatalf("session = %+v", s)
	}
	if _, ok := st.Accounts[state.Key("claude", "old")]; ok {
		t.Fatal("idle account kept by a session another account continued")
	}
}

// A run someone started reads the team, so it does not reuse the result of a
// scheduled run that skipped the read.
func TestWaitedRunThatSkippedTheTeamRead(t *testing.T) {
	for _, readToo := range []bool{false, true} {
		w, o := newWorld(t)
		h := w.home(t, "claude")
		w.sessions("claude", h, sess("s1", "/p", 100, t0))
		r := newRelay(t, w)
		o.Relay = r.client(w)
		run(t, o)
		held, err := o.Dir.Lock()
		if err != nil {
			t.Fatal(err)
		}
		reads := 0
		read := o.ReadLogs
		o.ReadLogs = func(p string, homes []string, since time.Time) logs.Result {
			reads++
			return read(p, homes, since)
		}
		w.now = t0.Add(30 * time.Minute)
		o.Wait = 10 * time.Second
		o.Waiting = func() {
			go func() {
				// A scheduled run at t0+15m collected; it read the team only
				// when readToo.
				at := t0.Add(15 * time.Minute)
				st, _ := o.Dir.LoadState()
				st.LastRunAt = at
				if err := o.Dir.SaveState(st); err != nil {
					t.Error(err)
				}
				if readToo {
					c, _ := LoadTeamCache(o.Dir)
					c.PulledAt = at
					if err := saveTeamCache(o.Dir, c); err != nil {
						t.Error(err)
					}
				}
				held()
			}()
		}
		res := run(t, o)
		if res.Waited != readToo || (reads == 0) != readToo {
			t.Fatalf("read too %v: waited %v, %d reads", readToo, res.Waited, reads)
		}
		if want := map[bool]time.Time{false: w.now, true: t0.Add(15 * time.Minute)}[readToo]; !res.Team.PulledAt.Equal(want) {
			t.Fatalf("read too %v: team read at %s, want %s", readToo, res.Team.PulledAt, want)
		}
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
// environment names.
func TestRunWaitsForTheRunItOverlaps(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		collects, newEnv, newVersion bool
		waited                       bool
	}{
		{"collected", true, false, false, true},
		{"did not collect", false, false, false, false},
		{"new folder", true, true, false, false},
		{"other release", true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, o := newWorld(t)
			h := w.home(t, "claude")
			w.sessions("claude", h, sess("s1", "/p", 100, t0))
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

func TestDiscoverAndRemember(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	defCodex := mk("home", ".codex")
	envCodex := mk("codex-env")
	oldCodex := mk("codex-old")
	env := map[string]string{
		"CODEX_HOME":        envCodex + string(filepath.Separator), // cleaned
		"CLAUDE_CONFIG_DIR": filepath.Join(root, "missing"),        // not a directory: left out
	}
	remembered := map[string][]string{"codex": {oldCodex, envCodex, filepath.Join(root, "gone")}}
	got := Discover(home, func(k string) string { return env[k] }, remembered)
	want := map[string][]string{"codex": {defCodex, envCodex, oldCodex}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover = %v, want %v", got, want)
	}

	next, changed := Remember(nil, home, got)
	if !changed || !reflect.DeepEqual(next, map[string][]string{"codex": {envCodex, oldCodex}}) {
		t.Fatalf("Remember = %v, %v", next, changed)
	}
	if _, changed := Remember(next, home, got); changed {
		t.Fatal("remembering the same homes again reported a change")
	}
}

func TestDiscoverFindsHermesProfiles(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	db := func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "state.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	def := mk("home", ".hermes")
	work := mk("home", ".hermes", "profiles", "work")
	db(work)
	mk("home", ".hermes", "profiles", "empty") // no usage yet
	deleted := mk("home", ".hermes", "profiles", ".deleted")
	db(deleted)
	other := mk("hermes-root")
	otherProf := mk("hermes-root", "profiles", "lab")
	db(otherProf)

	env := map[string]string{"HERMES_HOME": other}
	got := Discover(home, func(k string) string { return env[k] }, nil)
	if want := []string{def, other, otherProf, work}; !reflect.DeepEqual(got["hermes"], want) {
		t.Fatalf("hermes homes = %v, want %v", got["hermes"], want)
	}
	// Profiles are found again through their root and are not remembered.
	next, _ := Remember(nil, home, got)
	if want := []string{other}; !reflect.DeepEqual(next["hermes"], want) {
		t.Fatalf("remembered = %v, want %v", next["hermes"], want)
	}
	// HERMES_HOME naming the profile itself: found, and not remembered either.
	env["HERMES_HOME"] = work
	got = Discover(home, func(k string) string { return env[k] }, nil)
	if want := []string{def, work}; !reflect.DeepEqual(got["hermes"], want) {
		t.Fatalf("hermes homes = %v, want %v", got["hermes"], want)
	}
	if next, changed := Remember(nil, home, got); changed {
		t.Fatalf("remembered %v", next)
	}
}
