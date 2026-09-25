package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/relay"
)

// testRelay is a relay server on the test's clock.
type testRelay struct {
	srv   *httptest.Server
	store *relay.Memory
}

func newRelay(t *testing.T, w *world) *testRelay {
	t.Helper()
	store := relay.NewMemory()
	s := relay.NewServer(store, relay.Limits{})
	s.Now = func() time.Time { return w.now }
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return &testRelay{srv: srv, store: store}
}

// client is the relay client a run would use. Run replaces its key with the
// run's own, so a nil key here is fine.
func (r *testRelay) client(w *world) *relay.Client {
	return &relay.Client{BaseURL: r.srv.URL, HTTP: r.srv.Client(), Now: func() time.Time { return w.now }}
}

// publishOther stores a snapshot for another device of the same team.
func publishOther(t *testing.T, r *testRelay, w *world, key *team.Key, device string, st *state.State, at time.Time) {
	t.Helper()
	publishRelease(t, r, w, key, device, "v1.2.3", st, at)
}

// publishRelease stores a snapshot for another device on a release.
func publishRelease(t *testing.T, r *testRelay, w *world, key *team.Key, device, version string, st *state.State, at time.Time) {
	t.Helper()
	doc := BuildDoc(st, key, state.Config{Device: device}, "otherbox", "kim", version, at)
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	c := &relay.Client{BaseURL: r.srv.URL, HTTP: r.srv.Client(), Key: key, Now: func() time.Time { return w.now }}
	if err := c.Publish(context.Background(), device, body); err != nil {
		t.Fatalf("publish %s: %v", device, err)
	}
}

func deviceIDs(docs []snapshot.Doc) []string {
	var out []string
	for _, d := range docs {
		out = append(out, d.Device)
	}
	return out
}

func TestRelayPushAndPull(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", quota(t0, 10))
	w.sessions("claude", h, sess("s1", "/p", 100, t0))
	r := newRelay(t, w)

	key, _, err := LoadKey(o.Dir)
	if err != nil {
		t.Fatal(err)
	}
	publishOther(t, r, w, key, "d-other-device", ledger(), t0.Add(-time.Minute))

	o.Relay = r.client(w)
	res := run(t, o)
	rs := res.State.Relay
	if rs.Pending || rs.LastError != "" || !rs.LastPushAt.Equal(t0) || !rs.LastPullAt.Equal(t0) {
		t.Fatalf("relay state = %+v", rs)
	}
	if res.Team.Team != key.Fingerprint() || !res.Team.PulledAt.Equal(t0) {
		t.Fatalf("team cache = %+v", res.Team)
	}
	ids := deviceIDs(res.Team.Docs)
	if len(ids) != 2 || !strings.Contains(strings.Join(ids, " "), res.Config.Device) {
		t.Fatalf("pulled devices = %v", ids)
	}

	cache, err := LoadTeamCache(o.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if cache.Team != key.Fingerprint() || !cache.PulledAt.Equal(t0) || len(cache.Docs) != 2 || len(cache.Bodies) != 2 {
		t.Fatalf("saved cache = %+v", cache)
	}
	for _, d := range cache.Docs {
		if d.Device == res.Config.Device && !d.CollectedAt.Equal(t0) {
			t.Fatalf("own doc in cache collected at %v", d.CollectedAt)
		}
	}
	if info, err := os.Stat(o.Dir.Path(teamCacheFile)); err != nil || info.Size() == 0 {
		t.Fatalf("team-cache.json: %v", err)
	}
}

// Each read carries over since when a device has run an older release than
// the team's newest, from its first run a read found new, and starts it
// again once the device runs another, or after it was silent for a day.
func TestBehindSinceCarriesOverReads(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", nil)
	w.sessions("claude", h, sess("s1", "/p", 100, t0))
	r := newRelay(t, w)
	o.Relay = r.client(w)
	key, _, err := LoadKey(o.Dir)
	if err != nil {
		t.Fatal(err)
	}
	// This device runs v1.2.3, the newest. The other runs release at hours
	// after t0, or not at all for "".
	behind := func(hours int, release string) Behind {
		t.Helper()
		w.now = t0.Add(time.Duration(hours) * time.Hour)
		if release != "" {
			publishRelease(t, r, w, key, "d-other-device", release, ledger(), w.now)
		}
		run(t, o)
		cache, err := LoadTeamCache(o.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(cache.Behind) > 1 {
			t.Fatalf("behind = %+v", cache.Behind)
		}
		return cache.Behind["d-other-device"]
	}
	for _, c := range []struct {
		name    string
		hours   int
		release string
		want    Behind
	}{
		{"the first read, which cannot tell a run since", 0, "v1.2.0", Behind{}},
		{"no run since the read before, as a laptop asleep", 1, "", Behind{}},
		{"its first run found", 2, "v1.2.0", Behind{"v1.2.0", t0.Add(2 * time.Hour)}},
		{"a later run on the same release", 4, "v1.2.0", Behind{"v1.2.0", t0.Add(2 * time.Hour)}},
		{"no run since, but found before", 5, "", Behind{"v1.2.0", t0.Add(2 * time.Hour)}},
		{"another older release", 6, "v1.2.1", Behind{"v1.2.1", t0.Add(6 * time.Hour)}},
		{"silent for more than a day", 31, "", Behind{}},
		{"back", 32, "v1.2.1", Behind{"v1.2.1", t0.Add(32 * time.Hour)}},
		{"caught up", 33, "v1.2.3", Behind{}},
	} {
		if b := behind(c.hours, c.release); b != c.want {
			t.Fatalf("%s: %+v, want %+v", c.name, b, c.want)
		}
	}
}

func TestUnreachableRelayKeepsCollecting(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", nil)
	w.sessions("claude", h, sess("s1", "/p", 100, t0))
	r := newRelay(t, w)

	// A good exchange first, so there is a cache to keep.
	o.Relay = r.client(w)
	run(t, o)

	r.srv.Close()
	w.now = t0.Add(15 * time.Minute)
	w.sessions("claude", h, sess("s1", "/p", 180, w.now))
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("an unreachable relay failed the run: %v", err)
	}
	rs := res.State.Relay
	if !rs.Pending || !strings.Contains(rs.LastError, "relay unreachable") || !rs.LastErrorAt.Equal(w.now) {
		t.Fatalf("relay state = %+v", rs)
	}
	if !rs.LastPushAt.Equal(t0) {
		t.Fatalf("last push moved: %v", rs.LastPushAt)
	}
	if !strings.Contains(res.State.LastError, "relay unreachable") {
		t.Fatalf("last error = %q", res.State.LastError)
	}
	if !res.State.LastSuccessAt.Equal(w.now) {
		t.Fatal("a relay problem is not a failed collection")
	}
	if totalsFor(t, res.State, "claude", "ann").Tokens != tok(180) {
		t.Fatal("collection did not continue")
	}
	if len(res.Team.Docs) != 1 || !res.Team.PulledAt.Equal(t0) {
		t.Fatalf("cached team lost: %+v", res.Team)
	}
	saved, _ := o.Dir.LoadState()
	if !saved.Relay.Pending {
		t.Fatal("pending was not saved")
	}
}

func TestRelayBacklogIsSentWhenItReturns(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "claude")
	w.login("claude", h, "ann", nil)
	w.sessions("claude", h, sess("s1", "/p", 100, t0))
	r := newRelay(t, w)
	o.Relay = &relay.Client{BaseURL: "http://127.0.0.1:1", Now: func() time.Time { return w.now }}
	run(t, o)

	w.now = t0.Add(15 * time.Minute)
	w.sessions("claude", h, sess("s1", "/p", 250, w.now))
	o.Relay = r.client(w)
	res := run(t, o)
	if res.State.Relay.Pending || res.State.Relay.LastError != "" {
		t.Fatalf("relay state = %+v", res.State.Relay)
	}
	if len(res.Team.Docs) != 1 {
		t.Fatalf("team = %+v", res.Team)
	}
	// The newest snapshot carries the running totals of the missed runs.
	own := res.Team.Docs[0]
	if len(own.Accounts) != 1 || own.Accounts[0].Tokens != tok(250) {
		t.Fatalf("published accounts = %+v", own.Accounts)
	}
}

// stopTransport stops the run at one step of its exchange with the relay:
// as the push starts, as the pull starts, or once the pull is read in full.
type stopTransport struct {
	next   http.RoundTripper
	cancel context.CancelFunc
	at     string
}

func (s stopTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	step := "push"
	if req.Method == http.MethodGet {
		step = "pull"
	}
	if step == s.at {
		s.cancel()
		return nil, req.Context().Err()
	}
	resp, err := s.next.RoundTrip(req)
	if err != nil || step != "pull" || s.at != "read" {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	s.cancel()
	return resp, err
}

// TestStoppedExchange: a run stopped during its exchange with the relay
// keeps the last run's relay error only while its snapshot has not reached
// the relay.
func TestStoppedExchange(t *testing.T) {
	for _, at := range []string{"push", "pull", "read"} {
		w, o := newWorld(t)
		h := w.home(t, "claude")
		w.login("claude", h, "ann", nil)
		w.sessions("claude", h, sess("s1", "/p", 100, t0))
		r := newRelay(t, w)
		o.Relay = &relay.Client{BaseURL: "http://127.0.0.1:1", Now: func() time.Time { return w.now }}
		last := run(t, o).State.Relay
		if !strings.Contains(last.LastError, "relay unreachable") {
			t.Fatalf("relay state = %+v", last)
		}

		w.now = t0.Add(15 * time.Minute)
		w.sessions("claude", h, sess("s1", "/p", 250, w.now))
		ctx, cancel := context.WithCancel(context.Background())
		o.Relay = r.client(w)
		o.Relay.HTTP = &http.Client{Transport: stopTransport{r.srv.Client().Transport, cancel, at}}
		_, err := Run(ctx, o)
		cancel()
		if err != nil {
			t.Fatalf("stopped at the %s: %v", at, err)
		}
		rs, err := o.Dir.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		pushed := at != "push"
		switch {
		case rs.Relay.Pending == pushed, rs.Relay.LastPushAt.Equal(w.now) != pushed:
			t.Errorf("stopped at the %s, the snapshot is pending %v: %+v", at, rs.Relay.Pending, rs.Relay)
		case pushed && rs.Relay.LastError != "":
			t.Errorf("stopped at the %s after a push, the relay error is %q", at, rs.Relay.LastError)
		case !pushed && (rs.Relay.LastError != last.LastError || !rs.Relay.LastErrorAt.Equal(last.LastErrorAt)):
			t.Errorf("stopped at the push, the relay error is %q at %v", rs.Relay.LastError, rs.Relay.LastErrorAt)
		case rs.Relay.LastPullAt.Equal(w.now) != (at == "read"):
			t.Errorf("stopped at the %s, the last pull is %v", at, rs.Relay.LastPullAt)
		}
	}

	// Once the snapshot is pushed, what was wrong with the cached read
	// stands, as when a run skips the read.
	w, o := newWorld(t)
	r := newRelay(t, w)
	key, _, _ := LoadKey(o.Dir)
	body, _ := json.Marshal(BuildDoc(ledger(), key, state.Config{Device: "d-forged-device"}, "x", "y", "v1", t0))
	_ = r.store.Put(context.Background(), key.Fingerprint(), "d-forged-device", relay.Record{Body: body, Sig: []byte("not a signature")}, time.Hour)
	o.Relay = r.client(w)
	run(t, o)
	w.now = t0.Add(15 * time.Minute)
	o.Relay = &relay.Client{BaseURL: "http://127.0.0.1:1", Now: func() time.Time { return w.now }}
	run(t, o)
	w.now = t0.Add(30 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.Relay = r.client(w)
	o.Relay.HTTP = &http.Client{Transport: stopTransport{r.srv.Client().Transport, cancel, "pull"}}
	res, err := Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if rs := res.State.Relay; !strings.Contains(rs.LastError, "do not verify") || !rs.LastErrorAt.Equal(t0) || !rs.LastPushAt.Equal(w.now) {
		t.Errorf("stopped at the pull after a read that did not verify: %+v", rs)
	}
}

func TestRelayConflictIsReported(t *testing.T) {
	w, o := newWorld(t)
	w.home(t, "claude")
	r := newRelay(t, w)

	// Offline run: pending.
	o.Relay = &relay.Client{BaseURL: "http://127.0.0.1:1", Now: func() time.Time { return w.now }}
	res := run(t, o)
	if !res.State.Relay.Pending {
		t.Fatal("not pending after an offline run")
	}

	// The relay already holds a newer snapshot for this device id, from a
	// machine whose clock runs ahead.
	key, _, _ := LoadKey(o.Dir)
	w.now = t0.Add(time.Hour)
	publishOther(t, r, w, key, res.Config.Device, ledger(), w.now)

	w.now = t0.Add(15 * time.Minute)
	o.Relay = r.client(w)
	res = run(t, o)
	rs := res.State.Relay
	// The team still sees the older snapshot, and status must say so.
	if !rs.Pending || !strings.Contains(rs.LastError, "newer snapshot for this device id") || !rs.LastPullAt.Equal(w.now) {
		t.Fatalf("relay state after 409 = %+v", rs)
	}
	if !strings.Contains(res.State.LastError, "newer snapshot for this device id") {
		t.Fatalf("last error = %q", res.State.LastError)
	}
	if !rs.LastPushAt.IsZero() {
		t.Fatalf("a rejected push counted as pushed: %v", rs.LastPushAt)
	}

	// Once this device's snapshot is the newer one, a run that skips the
	// team read clears the conflict the last read's run met.
	w.now = t0.Add(time.Hour + 5*time.Minute)
	o.PullEvery = 2 * time.Hour
	res = run(t, o)
	if rs := res.State.Relay; rs.Pending || rs.LastError != "" || !rs.LastPullAt.Equal(t0.Add(15*time.Minute)) {
		t.Fatalf("relay state after the conflict passed = %+v", rs)
	}
}

func TestRelayDocsThatDoNotVerifyAreReported(t *testing.T) {
	w, o := newWorld(t)
	r := newRelay(t, w)
	key, _, _ := LoadKey(o.Dir)
	body, _ := json.Marshal(BuildDoc(ledger(), key, state.Config{Device: "d-forged-device"}, "x", "y", "v1", t0))
	_ = r.store.Put(context.Background(), key.Fingerprint(), "d-forged-device", relay.Record{Body: body, Sig: []byte("not a signature")}, time.Hour)

	o.Relay = r.client(w)
	res := run(t, o)
	if !strings.Contains(res.State.Relay.LastError, "do not verify") {
		t.Fatalf("relay error = %q", res.State.Relay.LastError)
	}
	for _, d := range res.Team.Docs {
		if d.Device == "d-forged-device" {
			t.Fatal("an unverified document reached the team cache")
		}
	}
	if len(res.Team.Docs) != 1 {
		t.Fatalf("team = %v", deviceIDs(res.Team.Docs))
	}

	// A run that skips the read still says what was wrong with it.
	w.now = t0.Add(15 * time.Minute)
	o.PullEvery = time.Hour
	res = run(t, o)
	if rs := res.State.Relay; !strings.Contains(rs.LastError, "do not verify") || !rs.LastPushAt.Equal(w.now) {
		t.Fatalf("relay state after a skipped read = %+v", rs)
	}
}

func TestRunUsesItsOwnKeyForTheRelay(t *testing.T) {
	w, o := newWorld(t)
	r := newRelay(t, w)
	stale := mustKey(t) // a key from before `team join`
	c := r.client(w)
	c.Key = stale
	o.Relay = c
	res := run(t, o)
	if res.State.Relay.LastError != "" || res.Team.Team != res.Key.Fingerprint() {
		t.Fatalf("relay state %+v, team %q", res.State.Relay, res.Team.Team)
	}
	if o.Relay.Key != stale {
		t.Fatal("Run changed the caller's client")
	}
}

func TestLoadTeamCache(t *testing.T) {
	d := state.Dir(t.TempDir())
	c, err := LoadTeamCache(d)
	if err != nil || c.Team != "" || c.Docs != nil {
		t.Fatalf("missing cache = %+v, %v", c, err)
	}

	key := mustKey(t)
	good, _ := json.Marshal(BuildDoc(ledger(), key, state.Config{Device: "d-0123456789"}, "a", "b", "v1", t0))
	want := TeamCache{PulledAt: t0, Team: key.Fingerprint(), Bodies: [][]byte{good, []byte(`{"v":99}`)}}
	if err := saveTeamCache(d, want); err != nil {
		t.Fatal(err)
	}
	c, err = LoadTeamCache(d)
	if err != nil {
		t.Fatal(err)
	}
	if c.Team != want.Team || !c.PulledAt.Equal(t0) || len(c.Bodies) != 2 {
		t.Fatalf("cache = %+v", c)
	}
	if len(c.Docs) != 1 || c.Docs[0].Device != "d-0123456789" {
		t.Fatalf("a body that does not decode was kept: %+v", c.Docs)
	}

	// A cache written before pulls kept one document per device may list a
	// device twice. Its newest document is the one read.
	older, _ := json.Marshal(BuildDoc(ledger(), key, state.Config{Device: "d-0123456789"}, "a", "b", "v1", t0.Add(-time.Hour)))
	newer, _ := json.Marshal(BuildDoc(ledger(), key, state.Config{Device: "d-0123456789"}, "a", "b", "v1", t0.Add(time.Hour)))
	dup := TeamCache{PulledAt: t0, Team: key.Fingerprint(), Bodies: [][]byte{older, good, newer, older}}
	if err := saveTeamCache(d, dup); err != nil {
		t.Fatal(err)
	}
	c, err = LoadTeamCache(d)
	if err != nil || len(c.Docs) != 1 || !c.Docs[0].CollectedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("duplicate device: %+v, %v", c.Docs, err)
	}

	if err := state.WriteFile(d.Path(teamCacheFile), []byte("{broken")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTeamCache(d); err == nil {
		t.Fatal("a damaged cache loaded without error")
	}
}
