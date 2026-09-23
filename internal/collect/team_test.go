package collect

import (
	"context"
	"encoding/json"
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
	doc := BuildDoc(st, key, device, "otherbox", "kim", "v1.2.3", at)
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
}

func TestRelayDocsThatDoNotVerifyAreReported(t *testing.T) {
	w, o := newWorld(t)
	r := newRelay(t, w)
	key, _, _ := LoadKey(o.Dir)
	body, _ := json.Marshal(BuildDoc(ledger(), key, "d-forged-device", "x", "y", "v1", t0))
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
	good, _ := json.Marshal(BuildDoc(ledger(), key, "d-0123456789", "a", "b", "v1", t0))
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
	older, _ := json.Marshal(BuildDoc(ledger(), key, "d-0123456789", "a", "b", "v1", t0.Add(-time.Hour)))
	newer, _ := json.Marshal(BuildDoc(ledger(), key, "d-0123456789", "a", "b", "v1", t0.Add(time.Hour)))
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
