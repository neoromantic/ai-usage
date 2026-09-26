package collect

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

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
func notLoggedIn(p string) error { return fmt.Errorf("%s: %w", p, probe.ErrNotLoggedIn) }

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
	w.logs[k] = homeLogs{Limits: codexLimits(t0.Add(time.Minute), 99)}
	res := run(t, o)
	q := totalsFor(t, res.State, "codex", "ann").Quota
	if q.Source != "harness" || q.Windows[0].Percent != 10 {
		t.Fatalf("probe quota replaced by log: %+v", q)
	}

	// No quota from the probe: a log reading newer than the last run is used.
	w.now = t0.Add(15 * time.Minute)
	w.login("codex", h, "ann", nil)
	w.logs[k] = homeLogs{Limits: codexLimits(t0.Add(10*time.Minute), 20)}
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
	w.logs[k] = homeLogs{Limits: codexLimits(t0.Add(-time.Hour), 70)}
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
	w.logs[k] = homeLogs{Limits: codexLimits(t0.Add(40*time.Minute), 3)}
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
	w.logs[k] = homeLogs{Limits: codexLimits(t0, 50), Sessions: []logs.Session{sess("c1", "/p", 10, t0)}}
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

func TestClaudeRejectionsInForceReadTogether(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", quota(t0.Add(-5*time.Hour), 40, 20))
	s := rejectedSession("s1", t0.Add(-2*time.Hour), t0.Add(time.Hour))
	week := t0.Add(96 * time.Hour)
	opus := &logs.Limits{ObservedAt: t0.Add(-3 * time.Hour), Windows: []snapshot.Window{{Name: "7d Opus", Percent: 100, ResetsAt: &week, Minutes: 10080}}}
	s.Rejected = append(s.Rejected, opus)
	w.sessions("claude", h, s)
	res := run(t, o)
	// Both are in force: one reading of the two windows, as of the newer.
	if q := totalsFor(t, res.State, "claude", "ann").Quota; !q.At.Equal(t0.Add(-2*time.Hour)) || len(q.Windows) != 2 || q.Windows[0].Name != "5h" || q.Windows[1].Name != "7d Opus" {
		t.Fatalf("quota = %+v, want the 5h and 7d Opus refusals", q)
	}

	// Once the 5h window resets, the weekly one still holds.
	w.now = t0.Add(90 * time.Minute)
	res = run(t, o)
	if q := totalsFor(t, res.State, "claude", "ann").Quota; q.Source != "rejection" || !q.At.Equal(opus.ObservedAt) || len(q.Windows) != 1 || q.Windows[0].Name != "7d Opus" {
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
	if err := res.Doc.Validate(); err != nil {
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
	for i := range 2 {
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
