package collect

import (
	"slices"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
)

const poolURL = "http://127.0.0.1:4100/api/v1/plugins/account-pool/http"

// A device says which providers' sessions go through BB's Account Pooler:
// Claude Code's when a run's own environment routes it there, Codex's when
// a session of the last 7 days says so in its log. A scheduled run, which
// sees neither, still says so for 7 days after the last run that did.
func TestPooledDevice(t *testing.T) {
	w, o := newWorld(t)
	ch, xh := w.home(t, "claude"), w.home(t, "codex")
	w.login("claude", ch, "ann@acme.dev", quota(t0, 10))
	w.login("codex", xh, "ann@acme.dev", quota(t0, 20))
	w.sessions("claude", ch, sess("a4c2e917", "/p", 100, t0))
	pooled := sess("019a0000-0000-7000-8000-00000000000c", "/p", 300, t0.Add(-8*24*time.Hour))
	pooled.Pooled = true
	w.sessions("codex", xh, sess("019a0000-0000-7000-8000-000000000001", "/p", 200, t0), pooled)

	// A session the pool served 8 days ago is too old to say so now.
	res := run(t, o)
	if res.Doc.Pooled != nil {
		t.Fatalf("pooled = %q, want none", res.Doc.Pooled)
	}

	pooled.Updated = t0.Add(-time.Hour)
	w.sessions("codex", xh, sess("019a0000-0000-7000-8000-000000000001", "/p", 200, t0), pooled)
	o.Probe.Environ = []string{"ANTHROPIC_BASE_URL=" + poolURL, "ANTHROPIC_AUTH_TOKEN=made-up-hub-token"}
	w.now = t0.Add(15 * time.Minute)
	res = run(t, o)
	if !slices.Equal(res.Doc.Pooled, []string{"claude", "codex"}) {
		t.Fatalf("pooled = %q", res.Doc.Pooled)
	}
	// The pool's tokens stay with the home's login: nothing says whose they
	// were.
	if tot, ok := findTotals(res.State, "codex", "ann@acme.dev"); !ok || tot.Tokens.Input != 500 {
		t.Fatalf("codex totals = %+v", tot)
	}

	// Scheduled runs, without the environment, until 7 days after.
	o.Probe.Environ = []string{}
	w.now = t0.Add(15*time.Minute + 6*24*time.Hour)
	if res = run(t, o); !slices.Equal(res.Doc.Pooled, []string{"claude", "codex"}) {
		t.Fatalf("6 days on: pooled = %q", res.Doc.Pooled)
	}
	w.now = t0.Add(15*time.Minute + 7*24*time.Hour)
	if res = run(t, o); res.Doc.Pooled != nil || res.State.Pooled != nil {
		t.Fatalf("7 days on: pooled = %q, state %v", res.Doc.Pooled, res.State.Pooled)
	}
}

func TestNotePooledKeepsTheLatest(t *testing.T) {
	_, o := newWorld(t)
	st, _ := o.Dir.LoadState()
	notePooled(st, "codex", false, []logs.Session{{Pooled: true, Updated: t0}}, t0.Add(time.Hour))
	notePooled(st, "codex", false, []logs.Session{{Pooled: true, Updated: t0.Add(-time.Hour)}}, t0.Add(time.Hour))
	if got := st.Pooled["codex"]; !got.Equal(t0) {
		t.Errorf("codex pooled at %v, want %v", got, t0)
	}
}

// A device sends pooled once a read shows every teammate reads it, and goes
// without it for a day when the relay refuses it, still sending its hours.
func TestPublishSendsPooledBehindTheGate(t *testing.T) {
	for _, c := range []struct {
		name         string
		teammate     string
		rejectPooled bool
		second       []string // what the second run put
	}{
		{"a teammate on v0.10.2", "v0.10.2", false, []string{"hours"}},
		{"everyone on v0.11.0", "v0.11.0", false, []string{"pooled"}},
		{"a relay on v0.10.2", "v0.11.0", true, []string{"pooled", "hours"}},
	} {
		w, o := newWorld(t)
		o.Version = "v0.11.0"
		o.Probe.Environ = []string{"ANTHROPIC_BASE_URL=" + poolURL}
		h := w.home(t, "claude")
		w.login("claude", h, "ann@acme.dev", quota(t0, 10))
		w.sessions("claude", h, sess("a4c2e917", "/p", 100, t0))
		r, rec := newRecordingRelay(t, w)
		o.Relay = r.client(w)
		key, _ := LoadKey(o.Dir)
		publishRelease(t, r, w, key, "d-build-01-teammate", c.teammate, ledger(), t0)
		rec.mu.Lock()
		rec.rejectPooled = c.rejectPooled
		rec.mu.Unlock()

		res := run(t, o)
		self := res.Config.Device
		if got := rec.wire(self, 0); !slices.Equal(got, []string{"legacy"}) {
			t.Fatalf("%s: the first run put %v", c.name, got)
		}

		w.now = t0.Add(15 * time.Minute)
		from := rec.count()
		res = run(t, o)
		if got := rec.wire(self, from); !slices.Equal(got, c.second) {
			t.Fatalf("%s: the second run put %v, want %v", c.name, got, c.second)
		}
		rs := res.State.Relay
		if want := (time.Time{}); c.rejectPooled {
			if want = w.now.Add(24 * time.Hour); !rs.NoPoolUntil.Equal(want) {
				t.Fatalf("%s: no pooled until %v", c.name, rs.NoPoolUntil)
			}
		} else if !rs.NoPoolUntil.Equal(want) {
			t.Fatalf("%s: no pooled until %v", c.name, rs.NoPoolUntil)
		}
		if !res.Doc.HasPooled() {
			t.Fatalf("%s: the report's own snapshot lost pooled", c.name)
		}

		w.now = t0.Add(30 * time.Minute)
		from = rec.count()
		run(t, o)
		if got, want := rec.wire(self, from), c.second[len(c.second)-1:]; !slices.Equal(got, want) {
			t.Fatalf("%s: the third run put %v, want %v", c.name, got, want)
		}
	}
}
