package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
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
	s := relay.NewServer(store)
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
	if selfupdate.Newer(ExtSince, version) {
		// A release before v0.4.0 sends none of the fields it added.
		doc = doc.Legacy()
	}
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

	key, err := LoadKey(o.Dir)
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
	if info, err := os.Stat(o.Dir.TeamCacheFile()); err != nil || info.Size() == 0 {
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
	key, err := LoadKey(o.Dir)
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
	key, _ := LoadKey(o.Dir)
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
	key, _ := LoadKey(o.Dir)
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
	key, _ := LoadKey(o.Dir)
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

// A document signed with the team key that this release cannot read comes
// from a newer release: the fix is to update, not to check the team key.
func TestRelayDocsFromANewerReleaseAreReported(t *testing.T) {
	w, o := newWorld(t)
	r := newRelay(t, w)
	key, _ := LoadKey(o.Dir)
	body, _ := json.Marshal(BuildDoc(ledger(), key, state.Config{Device: "d-newer-device"}, "x", "y", "v9.0.0", t0))
	body = bytes.Replace(body, []byte(`{"v":1,`), []byte(`{"v":1,"pace":[2],`), 1)
	_ = r.store.Put(context.Background(), key.Fingerprint(), "d-newer-device", relay.Record{Body: body, Sig: key.Sign(relay.SnapshotMessage(body))}, time.Hour)

	o.Relay = r.client(w)
	res := run(t, o)
	if got := res.State.Relay.LastError; got != "1 device sends a newer snapshot format: update ai-usage" {
		t.Fatalf("relay error = %q", got)
	}
	if res.Team.ReadError != res.State.Relay.LastError || len(res.Team.Docs) != 1 {
		t.Fatalf("team = %v, read error %q", deviceIDs(res.Team.Docs), res.Team.ReadError)
	}
	// Such a device reads what this one sends.
	if g := ExtendedWire(res.Team, key.Fingerprint(), res.Config.Device, time.Time{}, w.now); !g.Open {
		t.Fatalf("gate = %+v", g)
	}
}

// Each read remembers the release of every other device it found, and
// forgets a device the relay no longer lists within the time the relay would
// have kept its document.
func TestTeamReadsRememberReleases(t *testing.T) {
	w, o := newWorld(t)
	r := newRelay(t, w)
	o.Relay = r.client(w)
	key, _ := LoadKey(o.Dir)
	publishRelease(t, r, w, key, "d-leoair-0003", "v0.3.4", ledger(), t0)
	res := run(t, o)
	if k := res.Team.Known["d-leoair-0003"]; k.Version != "v0.3.4" || !k.First.Equal(t0) || !k.Last.Equal(t0) || len(res.Team.Known) != 1 {
		t.Fatalf("known = %+v", res.Team.Known)
	}
	cache, err := LoadTeamCache(o.Dir)
	if err != nil || !reflect.DeepEqual(cache.Known, res.Team.Known) {
		t.Fatalf("saved known = %+v, %v", cache.Known, err)
	}
	if g := ExtendedWire(cache, key.Fingerprint(), res.Config.Device, time.Time{}, w.now); g.Open || !slices.Equal(g.HeldBack, []string{"d-leoair-0003"}) {
		t.Fatalf("gate = %+v", g)
	}

	// Forgotten on another device: gone from the relay long before it
	// would have expired.
	if err := r.store.Delete(context.Background(), key.Fingerprint(), "d-leoair-0003"); err != nil {
		t.Fatal(err)
	}
	w.now = t0.Add(time.Hour)
	res = run(t, o)
	if res.Team.Known != nil {
		t.Fatalf("known after the device was forgotten = %+v", res.Team.Known)
	}
	if g := ExtendedWire(res.Team, key.Fingerprint(), res.Config.Device, time.Time{}, w.now); !g.Open {
		t.Fatalf("gate = %+v", g)
	}
}

func TestReadError(t *testing.T) {
	for _, c := range []struct {
		res  relay.PullResult
		want string
	}{
		{relay.PullResult{}, ""},
		{relay.PullResult{Undecodable: 1}, "1 device sends a newer snapshot format: update ai-usage"},
		{relay.PullResult{Undecodable: 3}, "3 devices send a newer snapshot format: update ai-usage"},
		{relay.PullResult{Unverified: 2}, "relay returned documents that do not verify with the team key"},
		{relay.PullResult{Unverified: 1, Undecodable: 2}, "2 devices send a newer snapshot format: update ai-usage; relay returned documents that do not verify with the team key"},
	} {
		if got := readError(c.res); got != c.want {
			t.Errorf("%+v: %q, want %q", c.res, got, c.want)
		}
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

	if err := fsutil.WriteFile(d.TeamCacheFile(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTeamCache(d); err == nil {
		t.Fatal("a damaged cache loaded without error")
	}
}

// putRecorder records the snapshots put to a relay, and refuses those with
// the fields v0.4.0 added, as an older relay does, while reject is set.
type putRecorder struct {
	next   http.Handler
	reject bool
	mu     sync.Mutex
	puts   []snapshot.Doc
}

func (p *putRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		doc, err := snapshot.Decode(body)
		p.mu.Lock()
		p.puts = append(p.puts, doc)
		reject := p.reject
		p.mu.Unlock()
		if err == nil && reject && doc.Extended() {
			http.Error(w, "snapshot: unknown field", http.StatusUnprocessableEntity)
			return
		}
	}
	p.next.ServeHTTP(w, r)
}

func (p *putRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.puts)
}

// sent is whether each snapshot device put since from was extended.
func (p *putRecorder) sent(device string, from int) []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []bool
	for _, d := range p.puts[from:] {
		if d.Device == device {
			out = append(out, d.Extended())
		}
	}
	return out
}

func newRecordingRelay(t *testing.T, w *world) (*testRelay, *putRecorder) {
	t.Helper()
	store := relay.NewMemory()
	s := relay.NewServer(store)
	s.Now = func() time.Time { return w.now }
	rec := &putRecorder{next: s}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return &testRelay{srv: srv, store: store}, rec
}

// A device sends the fields v0.4.0 added once a read shows every teammate
// reads them, and goes without them for a day when the relay refuses them.
func TestPublishSendsExtendedFieldsBehindTheGate(t *testing.T) {
	for _, c := range []struct {
		name     string
		teammate string
		reject   bool
		second   []bool // what the second run put, extended or not
	}{
		{"a teammate on v0.3.4", "v0.3.4", false, []bool{false}},
		{"everyone on v0.4.0", "v0.4.0", false, []bool{true}},
		{"a relay that refuses the fields", "v0.4.0", true, []bool{true, false}},
	} {
		w, o := newWorld(t)
		o.Version = "v0.4.0"
		h := w.home(t, "claude")
		w.login("claude", h, "ann@acme.dev", quota(t0, 10))
		w.sessions("claude", h, sess("a4c2e917", "/p", 100, t0))
		r, rec := newRecordingRelay(t, w)
		o.Relay = r.client(w)
		key, _ := LoadKey(o.Dir)
		publishRelease(t, r, w, key, "d-srv1-a4c2e917", c.teammate, ledger(), t0)
		rec.mu.Lock()
		rec.reject = c.reject
		rec.mu.Unlock()

		// No read of the team yet: nothing vouches for the teammates.
		res := run(t, o)
		self := res.Config.Device
		if got := rec.sent(self, 0); !slices.Equal(got, []bool{false}) {
			t.Fatalf("%s: the first run put %v", c.name, got)
		}

		w.now = t0.Add(15 * time.Minute)
		from := rec.count()
		res = run(t, o)
		if got := rec.sent(self, from); !slices.Equal(got, c.second) {
			t.Fatalf("%s: the second run put %v, want %v", c.name, got, c.second)
		}
		rs := res.State.Relay
		if rs.LastError != "" || rs.Pending || !rs.LastPushAt.Equal(w.now) {
			t.Fatalf("%s: relay state = %+v", c.name, rs)
		}
		if want := (time.Time{}); c.reject {
			want = w.now.Add(24 * time.Hour)
			if !rs.NoExtUntil.Equal(want) {
				t.Fatalf("%s: no extended fields until %v", c.name, rs.NoExtUntil)
			}
		} else if !rs.NoExtUntil.Equal(want) {
			t.Fatalf("%s: no extended fields until %v", c.name, rs.NoExtUntil)
		}
		if !res.Doc.Extended() {
			t.Fatalf("%s: the report's own snapshot lost its fields", c.name)
		}

		// A day without them after a refusal.
		w.now = t0.Add(30 * time.Minute)
		from = rec.count()
		run(t, o)
		if got, want := rec.sent(self, from), []bool{c.second[len(c.second)-1]}; !slices.Equal(got, want) {
			t.Fatalf("%s: the third run put %v, want %v", c.name, got, want)
		}
	}
}

// Each read adds the newest quota reading of every account, with what the
// team burned in each window, and when each device reported.
func TestTeamReadsRememberReadingsAndReports(t *testing.T) {
	w, o := newWorld(t)
	o.Version = "v0.4.0"
	h := w.home(t, "claude")
	w.login("claude", h, "ann@acme.dev", quota(t0, 10))
	w.sessions("claude", h, sess("a4c2e917", "/p", 100, t0))
	r := newRelay(t, w)
	o.Relay = r.client(w)
	key, _ := LoadKey(o.Dir)

	// A teammate on the same login, with an older reading of the same
	// window, spelled in another case.
	other := func(at time.Time, pct float64) *state.State {
		reset := t0.Add(5 * time.Hour)
		return &state.State{
			Accounts: map[string]*state.Account{state.Key("claude", "Ann@Acme.dev"): {Provider: "claude", Label: "Ann@Acme.dev",
				Quota: &state.Quota{At: at, Windows: []snapshot.Window{{Name: "5h", Percent: pct, ResetsAt: &reset}}}}},
			Sessions: map[string]*state.Session{state.Key("claude", "b7d1f3a0"): {Provider: "claude", Project: "/q", Updated: t0,
				By: map[string]snapshot.Tokens{"Ann@Acme.dev": {Input: 1000, Output: 100}},
				U:  meter.Hours{t0.Unix() / 3600: {Input: 1000, Output: 100}}}},
		}
	}
	publishRelease(t, r, w, key, "d-srv1-a4c2e917", "v0.4.0", other(t0.Add(-30*time.Minute), 5), t0)
	res := run(t, o)
	self := res.Config.Device
	k := HistoryKey("claude", "ann@acme.dev")
	// 100 + 10·5 here and 1000 + 100·5 there since the 5h window began.
	if got := res.Team.History[k]; len(got) != 1 || !got[0].At.Equal(t0) || got[0].Window != "5h" || got[0].Percent != 10 || got[0].TeamBurn != 1650 {
		t.Fatalf("history = %+v", res.Team.History)
	}

	// The teammate reads the window later; this device's reading stays.
	w.now = t0.Add(20 * time.Minute)
	publishRelease(t, r, w, key, "d-srv1-a4c2e917", "v0.4.0", other(t0.Add(15*time.Minute), 12), w.now)
	run(t, o)
	// Nothing newer on the third read.
	w.now = t0.Add(40 * time.Minute)
	res = run(t, o)
	got := res.Team.History[k]
	if len(got) != 2 || !got[1].At.Equal(t0.Add(15*time.Minute)) || got[1].Percent != 12 || got[1].TeamBurn != 1650 || len(res.Team.History) != 1 {
		t.Fatalf("history = %+v", res.Team.History)
	}
	want := map[string][]time.Time{
		self:              {t0, t0.Add(20 * time.Minute), t0.Add(40 * time.Minute)},
		"d-srv1-a4c2e917": {t0, t0.Add(20 * time.Minute)},
	}
	if !reflect.DeepEqual(res.Team.Seen, want) {
		t.Fatalf("seen = %v", res.Team.Seen)
	}
	cache, err := LoadTeamCache(o.Dir)
	if err != nil || !reflect.DeepEqual(cache.Seen, want) || len(cache.History[k]) != 2 {
		t.Fatalf("saved cache: %+v, %v", cache, err)
	}
	if err := ForgetCachedDevice(o.Dir, "d-srv1-a4c2e917"); err != nil {
		t.Fatal(err)
	}
	if cache, _ := LoadTeamCache(o.Dir); len(cache.Seen) != 1 {
		t.Fatalf("seen after the device was forgotten = %v", cache.Seen)
	}
}

func TestHistoryAndSeenKeepTwoDays(t *testing.T) {
	key := mustKey(t)
	now := t0
	var old []Point
	for i := range maxHistory + 10 {
		old = append(old, Point{At: now.Add(-49*time.Hour + time.Duration(i)*time.Minute), Window: "5h"})
	}
	prev := map[string][]Point{"claude/gone": old[:5], HistoryKey("claude", "ann@acme.dev"): old}
	got := historyAfter(prev, nil, key, now)
	if len(got) != 1 || len(got[HistoryKey("claude", "ann@acme.dev")]) != maxHistory-50 {
		t.Fatalf("%d keys, %d points", len(got), len(got[HistoryKey("claude", "ann@acme.dev")]))
	}
	for i := range maxHistory + 10 {
		old[i].At = now.Add(-time.Duration(i) * time.Minute)
	}
	if got := historyAfter(map[string][]Point{"k": old}, nil, key, now)["k"]; len(got) != maxHistory || !got[len(got)-1].At.Equal(now) {
		t.Fatalf("%d points, the newest %v", len(got), got[len(got)-1].At)
	}

	var ts []time.Time
	for i := range maxSeenPerDev + 10 {
		ts = append(ts, now.Add(-time.Duration(i)*time.Minute))
	}
	seen := seenAfter(map[string][]time.Time{"d-srv1-a4c2e917": ts, "d-gone-0001": {now.Add(-49 * time.Hour)}},
		[]snapshot.Doc{{Device: "d-srv1-a4c2e917", CollectedAt: now}}, now)
	if len(seen) != 1 || len(seen["d-srv1-a4c2e917"]) != maxSeenPerDev || !slices.IsSortedFunc(seen["d-srv1-a4c2e917"], time.Time.Compare) {
		t.Fatalf("seen = %v", seen)
	}
}
