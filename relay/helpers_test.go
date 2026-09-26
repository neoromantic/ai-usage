package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/team"
)

// t0 sits on an hour boundary, so a one-hour rate window starts at t0.
var t0 = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// relayEnv is a relay over a memory store, served by httptest, on a fake
// clock. It believes X-Real-Ip from loopback, as `relay serve` behind nginx.
type relayEnv struct {
	clock *clock
	mem   *Memory
	srv   *Server
	ts    *httptest.Server
}

func newRelay(t *testing.T) *relayEnv {
	t.Helper()
	c := &clock{t: t0}
	mem := NewMemory()
	mem.now = c.Now
	srv := NewServer(mem)
	srv.Now = c.Now
	srv.ClientIPHeader = "X-Real-Ip"
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &relayEnv{clock: c, mem: mem, srv: srv, ts: ts}
}

func (e *relayEnv) client(k *team.Key) *Client {
	return &Client{BaseURL: e.ts.URL, Key: k, HTTP: e.ts.Client(), Now: e.clock.Now}
}

// clientFrom is a client whose requests arrive from ip, as a proxy would say.
func (e *relayEnv) clientFrom(k *team.Key, ip string) *Client {
	c := e.client(k)
	c.HTTP = &http.Client{Transport: headerTransport{"X-Real-Ip": ip}}
	return c
}

// publish publishes dev's snapshot as of the relay's clock.
func (e *relayEnv) publish(t *testing.T, c *Client, k *team.Key, dev string) error {
	t.Helper()
	return c.Publish(context.Background(), dev, marshal(t, docFor(k, dev, e.clock.Now())))
}

// join publishes each device a minute after the one before, and fails the
// test on any error.
func (e *relayEnv) join(t *testing.T, c *Client, k *team.Key, devs ...string) {
	t.Helper()
	for _, dev := range devs {
		e.clock.Add(time.Minute)
		if err := e.publish(t, c, k, dev); err != nil {
			t.Fatal(err)
		}
	}
}

type headerTransport map[string]string

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func newKey(t *testing.T) *team.Key {
	t.Helper()
	k, err := team.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// docFor is a valid snapshot the way the collector builds one, using every
// member: its Hermes account shows the quota of the Codex account it bills
// through, and the Codex account says what Hermes spent through it.
func docFor(k *team.Key, device string, at time.Time) snapshot.Doc {
	quotaAt := at.Add(-time.Minute)
	resets := at.Add(2 * time.Hour)
	window := snapshot.Window{Name: "5h", Percent: 61.5, ResetsAt: &resets, Minutes: 300}
	tok := snapshot.Tokens{Input: 1200, Output: 300, CacheRead: 90000, CacheWrite: 4000}
	return snapshot.Doc{
		V:                snapshot.Version,
		Team:             k.Fingerprint(),
		Device:           device,
		DeviceLabel:      k.Seal("host " + device),
		OSUser:           k.Seal("me"),
		CollectorVersion: "v1.0.0",
		CollectedAt:      at,
		LastSuccessAt:    at,
		LastError:        k.Seal("hermes: state.db is locked"),
		Accounts: []snapshot.Account{{
			Provider:     "codex",
			Label:        k.Seal("me@example.com"),
			Current:      true,
			Plan:         "pro",
			QuotaAt:      &quotaAt,
			Windows:      []snapshot.Window{window},
			Sessions:     4,
			Tokens:       tok,
			LastActiveAt: &quotaAt,
			Projects:     []snapshot.Project{{Path: k.Seal("/Users/me/src/app"), Sessions: 4, Tokens: tok}},
			Linked:       []snapshot.Linked{{Provider: "hermes", Label: k.Seal("openai-codex"), Sessions: 2, Tokens: snapshot.Tokens{Input: 7}}},
			Days:         []int64{1200, 0, 300},
			Recent:       []snapshot.Recent{{Window: "5h", Start: at.Add(-2 * time.Hour), Tokens: 900}},
		}, {
			Provider:  "hermes",
			Label:     k.Seal("openai-codex"),
			QuotaAt:   &quotaAt,
			QuotaFrom: "codex",
			Windows:   []snapshot.Window{window},
			Projects:  []snapshot.Project{},
		}},
		Sources: []snapshot.Source{{Provider: "codex", Status: "ok"}, {Provider: "hermes", Status: "error", Error: k.Seal("state.db is locked")}},
		Aliases: []snapshot.Alias{{Provider: "codex", Label: k.Seal("me@example.com"), Name: k.Seal("work"), At: at}},
	}
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func statusOf(err error) int {
	var s *ErrStatus
	if errors.As(err, &s) {
		return s.Code
	}
	return 0
}

func putRequest(t *testing.T, base, teamFP, device string, body []byte, key, sig string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/v1/teams/"+teamFP+"/devices/"+device, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set(HeaderKey, key)
	}
	if sig != "" {
		req.Header.Set(HeaderSig, sig)
	}
	return req
}

// signedRequest builds a read or delete by hand, signed by k over the given fields.
func signedRequest(t *testing.T, base, method, path string, k *team.Key, signMethod, signTeam, signDevice string, at time.Time) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(HeaderKey, encode(k.Public()))
	req.Header.Set(HeaderTime, strconv.FormatInt(at.Unix(), 10))
	req.Header.Set(HeaderSig, encode(k.Sign(RequestMessage(signMethod, signTeam, signDevice, at))))
	return req
}

func send(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func stored(t *testing.T, e *relayEnv, teamFP string) map[string]Record {
	t.Helper()
	recs, err := e.mem.List(context.Background(), teamFP)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

var errBroken = errors.New("store is down")

// testStore fails the operations fail names, as a store error or a client
// that hung up would, and counts rate-limit commands. With hideFirstList,
// its first List finds the team empty, as a first write racing other
// devices' first writes sees it.
type testStore struct {
	Store
	hideFirstList bool
	lists         int
	fail          map[string]bool
	counts        atomic.Int64
}

func (s *testStore) Get(ctx context.Context, teamFP, device string) (*Record, error) {
	if s.fail["get"] {
		return nil, errBroken
	}
	return s.Store.Get(ctx, teamFP, device)
}

func (s *testStore) Put(ctx context.Context, teamFP, device string, rec Record, ttl time.Duration) error {
	if s.fail["put"] {
		return errBroken
	}
	return s.Store.Put(ctx, teamFP, device, rec, ttl)
}

func (s *testStore) Delete(ctx context.Context, teamFP, device string) error {
	if s.fail["delete"] {
		return errBroken
	}
	return s.Store.Delete(ctx, teamFP, device)
}

func (s *testStore) List(ctx context.Context, teamFP string) (map[string]Record, error) {
	s.lists++
	if s.hideFirstList && s.lists == 1 {
		return map[string]Record{}, nil
	}
	if s.fail["list"] {
		return nil, errBroken
	}
	return s.Store.List(ctx, teamFP)
}

func (s *testStore) Size(ctx context.Context, teamFP string) (int, error) {
	if s.fail["size"] {
		return 0, errBroken
	}
	return s.Store.Size(ctx, teamFP)
}

func (s *testStore) Count(ctx context.Context, key string, window time.Duration) (int64, error) {
	s.counts.Add(1)
	if s.fail["count"] {
		return 0, errBroken
	}
	return s.Store.Count(ctx, key, window)
}
