package relay

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

func newRelay(t *testing.T, limits Limits) *relayEnv {
	t.Helper()
	c := &clock{t: t0}
	mem := NewMemory()
	mem.now = c.Now
	srv := NewServer(mem, limits)
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

// docFor is a valid snapshot the way the collector builds one.
func docFor(k *team.Key, device string, at time.Time) snapshot.Doc {
	quotaAt := at.Add(-time.Minute)
	resets := at.Add(2 * time.Hour)
	tok := snapshot.Tokens{Input: 1200, Output: 300, CacheRead: 90000, CacheWrite: 4000}
	return snapshot.Doc{
		V:                snapshot.Version,
		Team:             k.Fingerprint(),
		Device:           device,
		DeviceLabel:      k.Seal("host " + device),
		OSUser:           k.Seal("ann"),
		CollectorVersion: "v1.0.0",
		CollectedAt:      at,
		LastSuccessAt:    at,
		Accounts: []snapshot.Account{{
			Provider: "codex",
			Label:    k.Seal("me@example.com"),
			Current:  true,
			Plan:     "pro",
			QuotaAt:  &quotaAt,
			Windows:  []snapshot.Window{{Name: "5h", Percent: 61.5, ResetsAt: &resets, Minutes: 300}},
			Sessions: 4,
			Tokens:   tok,
			Projects: []snapshot.Project{{Path: k.Seal("/Users/ann/src/app"), Sessions: 4, Tokens: tok}},
		}},
		Sources: []snapshot.Source{{Provider: "codex", Status: "ok"}},
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

func TestPublishPullRoundTrip(t *testing.T) {
	e := newRelay(t, Limits{})
	k := newKey(t)
	ctx := context.Background()
	bodies := map[string][]byte{}
	for _, dev := range []string{"work-laptop", "home-desktop"} {
		body := marshal(t, docFor(k, dev, t0))
		bodies[dev] = body
		if err := e.client(k).Publish(ctx, dev, body); err != nil {
			t.Fatalf("Publish %s: %v", dev, err)
		}
	}
	// Another team's device must not show up.
	other := newKey(t)
	if err := e.client(other).Publish(ctx, "work-laptop", marshal(t, docFor(other, "work-laptop", t0))); err != nil {
		t.Fatal(err)
	}

	c := e.client(k)
	c.BaseURL += "/" // a configured trailing slash is fine
	devices, bad, err := c.Pull(ctx)
	if err != nil || bad != 0 {
		t.Fatalf("Pull = %d bad, %v", bad, err)
	}
	if len(devices) != 2 || devices[0].Doc.Device != "home-desktop" || devices[1].Doc.Device != "work-laptop" {
		t.Fatalf("Pull returned %+v, want both devices sorted", devices)
	}
	for _, d := range devices {
		if !bytes.Equal(d.Body, bodies[d.Doc.Device]) {
			t.Errorf("%s: body changed on the relay", d.Doc.Device)
		}
		if host, err := k.Open(d.Doc.DeviceLabel); err != nil || host != "host "+d.Doc.Device {
			t.Errorf("%s: device label opens to %q, %v", d.Doc.Device, host, err)
		}
	}
}

// A Hermes account that shows the quota of the Codex account it bills
// through names that provider. The relay stores it and hands it back as is.
func TestPublishLinkedQuota(t *testing.T) {
	e := newRelay(t, Limits{})
	k := newKey(t)
	ctx := context.Background()
	d := docFor(k, "work-laptop", t0)
	linked := d.Accounts[0]
	linked.Provider, linked.Label, linked.QuotaFrom, linked.Plan = "hermes", k.Seal("openai-codex"), "codex", ""
	d.Accounts = append(d.Accounts, linked)
	body := marshal(t, d)
	if err := e.client(k).Publish(ctx, "work-laptop", body); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	devices, bad, err := e.client(k).Pull(ctx)
	if err != nil || bad != 0 || len(devices) != 1 || !bytes.Equal(devices[0].Body, body) {
		t.Fatalf("Pull = %+v, %d bad, %v", devices, bad, err)
	}
	if got := devices[0].Doc.Accounts[1]; got.QuotaFrom != "codex" || !got.QuotaAt.Equal(*d.Accounts[0].QuotaAt) {
		t.Fatalf("linked account = %+v", got)
	}
}

func TestPullCountsDocumentsThatDoNotVerify(t *testing.T) {
	e := newRelay(t, Limits{})
	k, other := newKey(t), newKey(t)
	ctx := context.Background()
	fp := k.Fingerprint()
	good := marshal(t, docFor(k, "good-device", t0))
	if err := e.client(k).Publish(ctx, "good-device", good); err != nil {
		t.Fatal(err)
	}
	sign := func(key *team.Key, body []byte) []byte { return key.Sign(SnapshotMessage(body)) }
	altered := bytes.Replace(good, []byte(`"sessions":4`), []byte(`"sessions":5`), 1)
	foreign := marshal(t, docFor(other, "foreign-device", t0))
	foreign = bytes.Replace(foreign, []byte(other.Fingerprint()), []byte(fp), 1)
	junk := []byte(`{"hello":"world"}`)
	// The relay, or whoever controls its store, writes these directly.
	forged := map[string]Record{
		"altered-body":   {Body: altered, Sig: sign(k, good)},
		"moved-device":   {Body: good, Sig: sign(k, good)},
		"other-team-key": {Body: foreign, Sig: sign(other, foreign)},
		"short-sig":      {Body: good, Sig: []byte("abc")},
		"signed-junk":    {Body: junk, Sig: sign(k, junk)},
		"no-sig":         {Body: good},
	}
	for dev, rec := range forged {
		if err := e.mem.Put(ctx, fp, dev, rec, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	devices, bad, err := e.client(k).Pull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Doc.Device != "good-device" {
		t.Fatalf("Pull kept %d documents, want only good-device", len(devices))
	}
	if bad != len(forged) {
		t.Fatalf("bad = %d, want %d", bad, len(forged))
	}
}

// Each device has one document. A relay that lists one again, even with a
// genuine older snapshot, would have its tokens counted twice.
func TestPullKeepsOneDocumentPerDevice(t *testing.T) {
	k := newKey(t)
	older := marshal(t, docFor(k, "device-bbbb", t0.Add(-time.Hour)))
	newer := marshal(t, docFor(k, "device-bbbb", t0))
	other := marshal(t, docFor(k, "device-aaaa", t0))
	entry := func(body []byte) ListedDevice {
		var d struct{ Device string }
		_ = json.Unmarshal(body, &d)
		return ListedDevice{Device: d.Device, Body: encode(body), Sig: encode(k.Sign(SnapshotMessage(body)))}
	}
	list := ListResponse{Devices: []ListedDevice{entry(older), entry(newer), entry(other), entry(newer), entry(older)}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(list)
	}))
	defer ts.Close()
	devices, bad, err := (&Client{BaseURL: ts.URL, Key: k}).Pull(context.Background())
	if err != nil || bad != 3 || len(devices) != 2 {
		t.Fatalf("Pull = %d devices, %d bad, %v; want 2 devices and 3 bad", len(devices), bad, err)
	}
	if devices[0].Doc.Device != "device-aaaa" || !bytes.Equal(devices[1].Body, newer) {
		t.Fatal("Pull did not keep the newest snapshot of each device")
	}
}

func TestPullRejectsAMalformedTeamRead(t *testing.T) {
	k := newKey(t)
	fp := k.Fingerprint()
	for name, body := range map[string]string{
		"not json":       "<html>",
		"bad base64":     `{"devices":[{"device":"good-device","body":"!!","sig":"!!"}]}`,
		"empty response": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/teams/"+fp {
					t.Errorf("path = %s", r.URL.Path)
				}
				io.WriteString(w, body)
			}))
			defer ts.Close()
			devices, bad, err := (&Client{BaseURL: ts.URL, Key: k}).Pull(context.Background())
			switch name {
			case "not json":
				if err == nil {
					t.Fatal("Pull accepted HTML")
				}
			case "bad base64":
				if err != nil || bad != 1 || len(devices) != 0 {
					t.Fatalf("Pull = %d devices, %d bad, %v", len(devices), bad, err)
				}
			default:
				if err != nil || bad != 0 || len(devices) != 0 {
					t.Fatalf("Pull = %d devices, %d bad, %v", len(devices), bad, err)
				}
			}
		})
	}
}

func TestPutRejects(t *testing.T) {
	e := newRelay(t, Limits{})
	k, other := newKey(t), newKey(t)
	fp, dev := k.Fingerprint(), "work-laptop"
	body := marshal(t, docFor(k, dev, t0))
	pub := encode(k.Public())
	sig := func(key *team.Key, b []byte) string { return encode(key.Sign(SnapshotMessage(b))) }
	signedBy := func(b []byte) (string, []byte, string) { return pub, b, sig(k, b) }

	withDoc := func(edit func(*snapshot.Doc)) []byte {
		d := docFor(k, dev, t0)
		edit(&d)
		return marshal(t, d)
	}
	str := string(body)
	// A repeated key: the first value is free text that Validate never sees.
	smuggled := []byte(`{"os_user":"free text, not sealed <b>hi</b>",` + str[1:])
	caseVariant := []byte(strings.Replace(str, `"device_label":`, `"Device_Label":`, 1))
	pretty, _ := json.MarshalIndent(docFor(k, dev, t0), "", "  ")
	unknown := []byte(strings.Replace(str, `{"v":1,`, `{"v":1,"note":"x",`, 1))
	big := append(bytes.Clone(body), bytes.Repeat([]byte(" "), snapshot.MaxBytes-len(body)+1)...)

	type req struct {
		team, device string
		key          string
		body         []byte
		sig          string
	}
	cases := []struct {
		name string
		req  req
		code int
	}{
		{"missing key", req{fp, dev, "", body, sig(k, body)}, http.StatusForbidden},
		{"malformed key", req{fp, dev, "not a key!", body, sig(k, body)}, http.StatusForbidden},
		{"short key", req{fp, dev, encode(k.Public()[:16]), body, sig(k, body)}, http.StatusForbidden},
		{"another team's key", req{fp, dev, encode(other.Public()), body, sig(other, body)}, http.StatusForbidden},
		{"missing signature", req{fp, dev, pub, body, ""}, http.StatusUnauthorized},
		{"signature not base64", req{fp, dev, pub, body, "***"}, http.StatusUnauthorized},
		{"signed by another key", req{fp, dev, pub, body, sig(other, body)}, http.StatusUnauthorized},
		{"signature over another body", req{fp, dev, pub, body, sig(k, withDoc(func(d *snapshot.Doc) { d.Accounts[0].Sessions = 9 }))}, http.StatusUnauthorized},
		{"request signature", req{fp, dev, pub, body, encode(k.Sign(RequestMessage("PUT", fp, dev, t0)))}, http.StatusUnauthorized},
		{"bare body signature", req{fp, dev, pub, body, encode(k.Sign(body))}, http.StatusUnauthorized},
		{"names another device", func() req {
			p, b, s := signedBy(withDoc(func(d *snapshot.Doc) { d.Device = "other-device" }))
			return req{fp, dev, p, b, s}
		}(), http.StatusUnprocessableEntity},
		{"names another team", func() req {
			p, b, s := signedBy(withDoc(func(d *snapshot.Doc) { d.Team = other.Fingerprint() }))
			return req{fp, dev, p, b, s}
		}(), http.StatusUnprocessableEntity},
		{"unknown field", func() req { p, b, s := signedBy(unknown); return req{fp, dev, p, b, s} }(), http.StatusUnprocessableEntity},
		{"repeated key", func() req { p, b, s := signedBy(smuggled); return req{fp, dev, p, b, s} }(), http.StatusUnprocessableEntity},
		{"case variant key", func() req { p, b, s := signedBy(caseVariant); return req{fp, dev, p, b, s} }(), http.StatusUnprocessableEntity},
		{"pretty printed", func() req { p, b, s := signedBy(pretty); return req{fp, dev, p, b, s} }(), http.StatusUnprocessableEntity},
		{"trailing newline", func() req { p, b, s := signedBy(append(bytes.Clone(body), '\n')); return req{fp, dev, p, b, s} }(), http.StatusUnprocessableEntity},
		{"wrong version", func() req {
			p, b, s := signedBy(withDoc(func(d *snapshot.Doc) { d.V = 2 }))
			return req{fp, dev, p, b, s}
		}(), http.StatusUnprocessableEntity},
		{"plain text label", func() req {
			p, b, s := signedBy(withDoc(func(d *snapshot.Doc) { d.Accounts[0].Label = "me@example.com" }))
			return req{fp, dev, p, b, s}
		}(), http.StatusUnprocessableEntity},
		{"quota_from its own provider", func() req {
			p, b, s := signedBy(withDoc(func(d *snapshot.Doc) { d.Accounts[0].QuotaFrom = "codex" }))
			return req{fp, dev, p, b, s}
		}(), http.StatusUnprocessableEntity},
		{"quota_from not a provider", func() req {
			p, b, s := signedBy(withDoc(func(d *snapshot.Doc) { d.Accounts[0].QuotaFrom = "free text" }))
			return req{fp, dev, p, b, s}
		}(), http.StatusUnprocessableEntity},
		{"collected in the future", func() req {
			p, b, s := signedBy(withDoc(func(d *snapshot.Doc) { d.CollectedAt = t0.Add(11 * time.Minute) }))
			return req{fp, dev, p, b, s}
		}(), http.StatusUnprocessableEntity},
		{"over 32 KB", func() req { p, b, s := signedBy(big); return req{fp, dev, p, b, s} }(), http.StatusRequestEntityTooLarge},
		{"team path not a fingerprint", req{"not-a-team", dev, pub, body, sig(k, body)}, http.StatusNotFound},
		{"device path bad", req{fp, "UPPER_CASE", pub, body, sig(k, body)}, http.StatusNotFound},
		{"device path escaped slash", req{fp, "a%2Fbcdefgh", pub, body, sig(k, body)}, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, msg := send(t, putRequest(t, e.ts.URL, c.req.team, c.req.device, c.req.body, c.req.key, c.req.sig))
			if resp.StatusCode != c.code {
				t.Fatalf("status = %d (%s), want %d", resp.StatusCode, strings.TrimSpace(msg), c.code)
			}
			var e struct{ Error string }
			if json.Unmarshal([]byte(msg), &e) != nil || e.Error == "" {
				t.Fatalf("error body = %q", msg)
			}
		})
	}
	if n := len(stored(t, e, fp)); n != 0 {
		t.Fatalf("%d documents stored after rejected writes", n)
	}

	t.Run("method not allowed", func(t *testing.T) {
		req := putRequest(t, e.ts.URL, fp, dev, body, pub, sig(k, body))
		req.Method = http.MethodPost
		if resp, _ := send(t, req); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST status = %d", resp.StatusCode)
		}
	})
	t.Run("the same body is accepted", func(t *testing.T) {
		if resp, msg := send(t, putRequest(t, e.ts.URL, fp, dev, body, pub, sig(k, body))); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d (%s)", resp.StatusCode, msg)
		}
	})
}

// A key holder can write only the team its key names.
func TestCannotWriteAnotherTeam(t *testing.T) {
	e := newRelay(t, Limits{})
	victim, attacker := newKey(t), newKey(t)
	ctx := context.Background()
	dev := "victim-laptop"
	orig := marshal(t, docFor(victim, dev, t0))
	if err := e.client(victim).Publish(ctx, dev, orig); err != nil {
		t.Fatal(err)
	}
	vfp := victim.Fingerprint()
	forged := docFor(attacker, dev, t0.Add(time.Minute))
	forged.Team = vfp
	body := marshal(t, forged)
	sig := encode(attacker.Sign(SnapshotMessage(body)))

	// The attacker's own key on the victim's path.
	if resp, _ := send(t, putRequest(t, e.ts.URL, vfp, dev, body, encode(attacker.Public()), sig)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	// The victim's public key, which is not secret, with the attacker's signature.
	if resp, _ := send(t, putRequest(t, e.ts.URL, vfp, dev, body, encode(victim.Public()), sig)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	// Signed correctly for the attacker's team but naming the victim's.
	if resp, _ := send(t, putRequest(t, e.ts.URL, attacker.Fingerprint(), dev, body, encode(attacker.Public()), sig)); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	// Reading or deleting the victim's team needs the victim's key too.
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		path, device := "/v1/teams/"+vfp, ""
		if method == http.MethodDelete {
			path, device = path+"/devices/"+dev, dev
		}
		req := signedRequest(t, e.ts.URL, method, path, attacker, method, vfp, device, t0)
		if resp, _ := send(t, req); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", method, resp.StatusCode)
		}
		req = signedRequest(t, e.ts.URL, method, path, attacker, method, vfp, device, t0)
		req.Header.Set(HeaderKey, encode(victim.Public()))
		if resp, _ := send(t, req); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s with the victim's public key: status = %d, want 401", method, resp.StatusCode)
		}
	}
	if got := stored(t, e, vfp)[dev].Body; !bytes.Equal(got, orig) {
		t.Fatal("the victim's document changed")
	}
	if n := len(stored(t, e, attacker.Fingerprint())); n != 0 {
		t.Fatalf("attacker team holds %d documents", n)
	}
}

func TestStaleAndRepeatedWrites(t *testing.T) {
	e := newRelay(t, Limits{})
	k := newKey(t)
	c := e.client(k)
	ctx := context.Background()
	dev := "work-laptop"
	cur := func() []byte { return stored(t, e, k.Fingerprint())[dev].Body }

	first := marshal(t, docFor(k, dev, t0))
	if err := c.Publish(ctx, dev, first); err != nil {
		t.Fatal(err)
	}
	if err := c.Publish(ctx, dev, first); err != nil {
		t.Fatalf("repeating the stored body: %v", err)
	}
	older := marshal(t, docFor(k, dev, t0.Add(-time.Minute)))
	if err := c.Publish(ctx, dev, older); statusOf(err) != http.StatusConflict {
		t.Fatalf("older snapshot: err = %v, want 409", err)
	}
	if !bytes.Equal(cur(), first) {
		t.Fatal("an older snapshot replaced a newer one")
	}
	sameTime := docFor(k, dev, t0)
	sameTime.Accounts[0].Sessions++
	sameBody := marshal(t, sameTime)
	if err := c.Publish(ctx, dev, sameBody); err != nil {
		t.Fatalf("same collected_at, new body: %v", err)
	}
	e.clock.Add(15 * time.Minute)
	newer := marshal(t, docFor(k, dev, e.clock.Now()))
	if err := c.Publish(ctx, dev, newer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cur(), newer) {
		t.Fatal("the newer snapshot was not stored")
	}
	// A replay of an earlier signed body cannot roll the device back.
	if err := c.Publish(ctx, dev, first); statusOf(err) != http.StatusConflict {
		t.Fatalf("replayed body: err = %v, want 409", err)
	}
}

func TestDeviceCap(t *testing.T) {
	e := newRelay(t, Limits{DevicesPerTeam: 2, RecordTTL: 24 * time.Hour})
	k := newKey(t)
	c := e.client(k)
	ctx := context.Background()
	publish := func(dev string) error { return c.Publish(ctx, dev, marshal(t, docFor(k, dev, e.clock.Now()))) }
	for _, dev := range []string{"device-one", "device-two"} {
		if err := publish(dev); err != nil {
			t.Fatal(err)
		}
	}
	if err := publish("device-three"); statusOf(err) != http.StatusForbidden {
		t.Fatalf("third device: err = %v, want 403", err)
	}
	e.clock.Add(time.Minute)
	if err := publish("device-one"); err != nil {
		t.Fatalf("a known device must still update: %v", err)
	}
	if err := c.Remove(ctx, "device-two"); err != nil {
		t.Fatal(err)
	}
	if err := publish("device-three"); err != nil {
		t.Fatalf("after removing a device: %v", err)
	}
	// Expired documents free their slot.
	e.clock.Add(25 * time.Hour)
	if err := publish("device-four"); err != nil {
		t.Fatalf("after the others expired: %v", err)
	}
}

func TestTeamWriteLimit(t *testing.T) {
	e := newRelay(t, Limits{WritesPerTeam: 2})
	k, other := newKey(t), newKey(t)
	ctx := context.Background()
	publish := func(k *team.Key) error {
		return e.client(k).Publish(ctx, "work-laptop", marshal(t, docFor(k, "work-laptop", e.clock.Now())))
	}
	for range 2 {
		if err := publish(k); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.Add(10 * time.Minute)
	resp, _ := send(t, func() *http.Request {
		body := marshal(t, docFor(k, "work-laptop", e.clock.Now()))
		return putRequest(t, e.ts.URL, k.Fingerprint(), "work-laptop", body, encode(k.Public()), encode(k.Sign(SnapshotMessage(body))))
	}())
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third write: status = %d, want 429", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "3000" {
		t.Fatalf("Retry-After = %q, want the 50 minutes left in the window", got)
	}
	if err := publish(other); err != nil {
		t.Fatalf("another team is limited too: %v", err)
	}
	e.clock.Add(50 * time.Minute)
	if err := publish(k); err != nil {
		t.Fatalf("next window: %v", err)
	}
}

func TestPerIPLimit(t *testing.T) {
	e := newRelay(t, Limits{RequestsPerIP: 3})
	health := func(ip string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/health", nil)
		if ip != "" {
			req.Header.Set("X-Real-Ip", ip)
		}
		resp, _ := send(t, req)
		return resp
	}
	for range 3 {
		if resp := health("203.0.113.7"); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}
	resp := health("203.0.113.7")
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "3600" {
		t.Fatalf("fourth request: status = %d, Retry-After = %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if resp := health("203.0.113.8"); resp.StatusCode != http.StatusOK {
		t.Fatalf("another client: status = %d", resp.StatusCode)
	}
	// IPv6 clients are counted per /64.
	for i := range 3 {
		if resp := health("2001:db8:1:2::" + strconv.Itoa(i+1)); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}
	if resp := health("2001:db8:1:2:ffff::9"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("same /64: status = %d, want 429", resp.StatusCode)
	}
	e.clock.Add(time.Hour)
	if resp := health("203.0.113.7"); resp.StatusCode != http.StatusOK {
		t.Fatalf("next window: status = %d", resp.StatusCode)
	}
}

func TestNewTeamsPerIP(t *testing.T) {
	e := newRelay(t, Limits{NewTeamsPerIP: 1})
	a, b := newKey(t), newKey(t)
	ctx := context.Background()
	publish := func(k *team.Key, ip, dev string) error {
		return e.clientFrom(k, ip).Publish(ctx, dev, marshal(t, docFor(k, dev, t0)))
	}
	if err := publish(a, "198.51.100.1", "device-one"); err != nil {
		t.Fatal(err)
	}
	if err := publish(b, "198.51.100.1", "device-one"); statusOf(err) != http.StatusTooManyRequests {
		t.Fatalf("second new team from one IP: err = %v, want 429", err)
	}
	if err := publish(a, "198.51.100.1", "device-two"); err != nil {
		t.Fatalf("a new device in an existing team: %v", err)
	}
	if err := publish(b, "198.51.100.2", "device-one"); err != nil {
		t.Fatalf("new team from another IP: %v", err)
	}
	// A /48 is one customer or one free tunnel, with 65,536 /64s in it.
	c, d := newKey(t), newKey(t)
	if err := publish(c, "2001:db8:aa:1::1", "device-one"); err != nil {
		t.Fatal(err)
	}
	if err := publish(d, "2001:db8:aa:2::1", "device-one"); statusOf(err) != http.StatusTooManyRequests {
		t.Fatalf("second new team from one /48: err = %v, want 429", err)
	}
	// The budget is per day, not per hour.
	e.clock.Add(time.Hour)
	if err := publish(d, "198.51.100.1", "device-one"); statusOf(err) != http.StatusTooManyRequests {
		t.Fatalf("an hour later: err = %v, want 429", err)
	}
	e.clock.Add(23 * time.Hour)
	if err := publish(d, "198.51.100.1", "device-one"); err != nil {
		t.Fatalf("next day: %v", err)
	}
}

// However many keys one address makes, it adds one full team's worth of
// devices a day, so it cannot fill the store.
func TestNewDevicesPerIP(t *testing.T) {
	e := newRelay(t, Limits{})
	ctx := context.Background()
	stored, limited := 0, 0
	var keys []*team.Key
	for range 10 {
		k := newKey(t)
		keys = append(keys, k)
		for i := range 32 {
			dev := fmt.Sprintf("device-%02d", i)
			switch err := e.client(k).Publish(ctx, dev, marshal(t, docFor(k, dev, t0))); {
			case err == nil:
				stored++
			case statusOf(err) == http.StatusTooManyRequests:
				limited++
			default:
				t.Fatal(err)
			}
		}
	}
	if want := int(DefaultLimits().NewDevicesPerIP); stored != want || limited != 320-want {
		t.Fatalf("stored %d, limited %d; want %d stored", stored, limited, want)
	}
	// Devices already stored keep updating.
	e.clock.Add(time.Hour)
	k := keys[0]
	if err := e.client(k).Publish(ctx, "device-00", marshal(t, docFor(k, "device-00", e.clock.Now()))); err != nil {
		t.Fatalf("a known device: %v", err)
	}
	e.clock.Add(23 * time.Hour)
	k = keys[1]
	if err := e.client(k).Publish(ctx, "device-00", marshal(t, docFor(k, "device-00", e.clock.Now()))); err != nil {
		t.Fatalf("next day: %v", err)
	}
}

// A snapshot is kept for as long as its device has been writing, at least a
// week and at most RecordTTL, so a key made to fill the store and dropped
// leaves its snapshots for a week, not 90 days.
func TestRecordLifetimeGrowsWithTheDevice(t *testing.T) {
	e := newRelay(t, Limits{})
	k := newKey(t)
	c := e.client(k)
	ctx := context.Background()
	publish := func(dev string) {
		t.Helper()
		if err := c.Publish(ctx, dev, marshal(t, docFor(k, dev, e.clock.Now()))); err != nil {
			t.Fatal(err)
		}
	}
	has := func(dev string) bool { _, ok := stored(t, e, k.Fingerprint())[dev]; return ok }
	day := 24 * time.Hour
	publish("one-off-device")
	publish("steady-device")
	e.clock.Add(6 * day)
	publish("steady-device")
	e.clock.Add(day)
	if !has("one-off-device") {
		t.Fatal("a new device's snapshot expired within a week")
	}
	e.clock.Add(time.Second)
	if has("one-off-device") {
		t.Fatal("a device written once is kept past a week")
	}
	// Written again at 12 days and at 20: each write keeps it for as long as
	// it has existed.
	e.clock.Add(5*day - time.Second)
	publish("steady-device")
	e.clock.Add(8 * day)
	publish("steady-device")
	e.clock.Add(20 * day)
	if !has("steady-device") {
		t.Fatal("a device 20 days old expired within 20 days of its last write")
	}
	e.clock.Add(time.Second)
	if has("steady-device") {
		t.Fatal("a device 20 days old is kept past 20 days")
	}
	// The lifetime never passes RecordTTL.
	if got := e.srv.recordTTL(time.Time{}); got != DefaultLimits().RecordTTL {
		t.Fatalf("recordTTL(zero since) = %v", got)
	}
}

func TestClientAddr(t *testing.T) {
	cases := []struct {
		name   string
		header string
		remote string
		real   string
		xff    []string
		want   string
	}{
		{"direct client", "", "198.51.100.7:5555", "", nil, "198.51.100.7"},
		// Caddy, cloudflared, and load balancers pass the client's own
		// X-Real-Ip through, so no header is believed unless it is named.
		{"proxy, no header named", "", "127.0.0.1:40000", "1.2.3.4", []string{"5.6.7.8"}, "127.0.0.1"},
		{"direct client cannot pick its key", "X-Real-Ip", "198.51.100.7:5555", "1.2.3.4", []string{"5.6.7.8"}, "198.51.100.7"},
		{"proxy on loopback, X-Real-Ip", "X-Real-Ip", "127.0.0.1:40000", "203.0.113.5", []string{"9.9.9.9"}, "203.0.113.5"},
		{"X-Forwarded-For named, client's X-Real-Ip ignored", "X-Forwarded-For", "127.0.0.1:40000", "6.6.6.6", []string{"203.0.113.5"}, "203.0.113.5"},
		{"header name in any case", "x-real-ip", "127.0.0.1:40000", "203.0.113.5", nil, "203.0.113.5"},
		{"proxy on private network", "X-Real-Ip", "10.1.2.3:40000", "203.0.113.5", nil, "203.0.113.5"},
		{"proxy on IPv6 loopback", "X-Real-Ip", "[::1]:40000", "203.0.113.5", nil, "203.0.113.5"},
		{"forwarded-for is read from the right", "X-Forwarded-For", "127.0.0.1:1", "", []string{"1.1.1.1, 203.0.113.5"}, "203.0.113.5"},
		{"last forwarded-for line wins", "X-Forwarded-For", "127.0.0.1:1", "", []string{"1.1.1.1", "9.9.9.9, 203.0.113.5 "}, "203.0.113.5"},
		{"empty forwarded-for", "X-Forwarded-For", "127.0.0.1:1", "", []string{""}, "127.0.0.1"},
		{"named header missing", "X-Real-Ip", "127.0.0.1:1", "", []string{"203.0.113.5"}, "127.0.0.1"},
		{"no peer address", "X-Real-Ip", "", "203.0.113.5", nil, "203.0.113.5"},
		{"no peer, no header named", "", "", "203.0.113.5", nil, ""},
		{"Vercel bridge: bare client address", "X-Real-Ip", "203.0.113.5", "6.6.6.6", nil, "203.0.113.5"},
		{"Vercel bridge: bare IPv6 client", "X-Real-Ip", "2001:db8:1:2::5", "2001:db8:1:2::5", nil, "2001:db8:1:2::/64"},
		{"IPv6 client per /64", "", "[2001:db8:1:2:3:4:5:6]:443", "", nil, "2001:db8:1:2::/64"},
		{"IPv4-mapped IPv6 client", "", "[::ffff:198.51.100.7]:443", "", nil, "198.51.100.7"},
		{"link-local proxy", "X-Real-Ip", "[fe80::1%en0]:1", "203.0.113.9", nil, "203.0.113.9"},
		{"proxy passes an IPv6 client", "X-Real-Ip", "127.0.0.1:1", "2001:db8:aa:bb::1", nil, "2001:db8:aa:bb::/64"},
		{"proxy passes junk", "X-Real-Ip", "127.0.0.1:1", strings.Repeat("x", 100), nil, strings.Repeat("x", 64)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
			r.RemoteAddr = c.remote
			if c.real != "" {
				r.Header.Set("X-Real-Ip", c.real)
			}
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := rateKey(clientAddr(r, c.header), 64); got != c.want {
				t.Fatalf("key = %q, want %q", got, c.want)
			}
		})
	}
	if got := rateKey("2001:db8:aa:bb::1", 48); got != "2001:db8:aa::/48" {
		t.Fatalf("/48 key = %q", got)
	}
}

// Behind Caddy, cloudflared, or a load balancer, the client's own X-Real-Ip
// reaches the relay, so by default every header is ignored and a client
// cannot pick a fresh rate-limit key for each request.
func TestForwardingHeadersAreIgnoredByDefault(t *testing.T) {
	srv := NewServer(NewMemory(), Limits{RequestsPerIP: 3})
	srv.Now = func() time.Time { return t0 }
	ts := httptest.NewServer(srv)
	defer ts.Close()
	codes := map[int]int{}
	for i := range 20 {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/health", nil)
		req.Header.Set("X-Real-Ip", "203.0.113."+strconv.Itoa(i))
		req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i))
		resp, _ := send(t, req)
		codes[resp.StatusCode]++
	}
	if codes[http.StatusOK] != 3 || codes[http.StatusTooManyRequests] != 17 {
		t.Fatalf("status counts = %v, want 3 allowed and 17 limited", codes)
	}
}

// countingStore counts rate-limit commands.
type countingStore struct {
	*Memory
	counts atomic.Int64
}

func (c *countingStore) Count(ctx context.Context, key string, window time.Duration) (int64, error) {
	c.counts.Add(1)
	return c.Memory.Count(ctx, key, window)
}

// On Vercel KV every counted request is paid for. A path the relay does not
// serve, and a client that is already over its limit, cost nothing.
func TestRejectedRequestsSkipTheStore(t *testing.T) {
	store := &countingStore{Memory: NewMemory()}
	srv := NewServer(store, Limits{RequestsPerIP: 5})
	c := &clock{t: t0}
	srv.Now = c.Now
	ts := httptest.NewServer(srv)
	defer ts.Close()
	get := func(path string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		resp, _ := send(t, req)
		return resp
	}
	for _, path := range []string{"/nonsense", "/v1/", "/v1/teams"} {
		if resp := get(path); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status = %d", path, resp.StatusCode)
		}
	}
	if n := store.counts.Load(); n != 0 {
		t.Fatalf("unrouted requests made %d store commands", n)
	}
	codes := map[int]int{}
	for range 50 {
		resp := get("/v1/health")
		codes[resp.StatusCode]++
		if resp.StatusCode == http.StatusTooManyRequests && resp.Header.Get("Retry-After") != "3600" {
			t.Fatalf("Retry-After = %q", resp.Header.Get("Retry-After"))
		}
	}
	if codes[http.StatusOK] != 5 || codes[http.StatusTooManyRequests] != 45 {
		t.Fatalf("status counts = %v", codes)
	}
	if n := store.counts.Load(); n != 6 {
		t.Fatalf("%d store commands, want 6: five allowed and the one over the limit", n)
	}
	c.Add(time.Hour)
	if resp := get("/v1/health"); resp.StatusCode != http.StatusOK {
		t.Fatalf("next window: status = %d", resp.StatusCode)
	}
}

func TestOverLimitIsBounded(t *testing.T) {
	var o overLimit
	for i := range maxOverLimit + 10 {
		o.add(strconv.Itoa(i), 100, 50)
	}
	if len(o.until) != maxOverLimit || !o.has("0", 50) || o.has("0", 100) {
		t.Fatalf("%d keys held", len(o.until))
	}
	// Expired keys make room.
	o.add("late", 200, 150)
	if len(o.until) != 1 || !o.has("late", 150) {
		t.Fatalf("%d keys held after expiry", len(o.until))
	}
}

func TestSignedRead(t *testing.T) {
	e := newRelay(t, Limits{})
	k, other := newKey(t), newKey(t)
	ctx := context.Background()
	if err := e.client(k).Publish(ctx, "work-laptop", marshal(t, docFor(k, "work-laptop", t0))); err != nil {
		t.Fatal(err)
	}
	for _, skew := range []time.Duration{-ClockSkew, ClockSkew, 0} {
		c := e.client(k)
		c.Now = func() time.Time { return t0.Add(skew) }
		if devices, _, err := c.Pull(ctx); err != nil || len(devices) != 1 {
			t.Fatalf("skew %v: %d devices, %v", skew, len(devices), err)
		}
	}
	for _, skew := range []time.Duration{-ClockSkew - time.Second, ClockSkew + time.Second, -24 * time.Hour} {
		c := e.client(k)
		c.Now = func() time.Time { return t0.Add(skew) }
		if _, _, err := c.Pull(ctx); statusOf(err) != http.StatusUnauthorized {
			t.Fatalf("skew %v: err = %v, want 401", skew, err)
		}
	}

	fp := k.Fingerprint()
	path := "/v1/teams/" + fp
	cases := []struct {
		name string
		edit func(*http.Request)
		code int
	}{
		{"valid", func(*http.Request) {}, http.StatusOK},
		{"missing time", func(r *http.Request) { r.Header.Del(HeaderTime) }, http.StatusUnauthorized},
		{"time not a number", func(r *http.Request) { r.Header.Set(HeaderTime, "yesterday") }, http.StatusUnauthorized},
		{"time changed after signing", func(r *http.Request) { r.Header.Set(HeaderTime, strconv.FormatInt(t0.Unix()+1, 10)) }, http.StatusUnauthorized},
		{"huge time", func(r *http.Request) { r.Header.Set(HeaderTime, "9223372036854775807") }, http.StatusUnauthorized},
		{"missing signature", func(r *http.Request) { r.Header.Del(HeaderSig) }, http.StatusUnauthorized},
		{"missing key", func(r *http.Request) { r.Header.Del(HeaderKey) }, http.StatusForbidden},
		{"another team's key", func(r *http.Request) { r.Header.Set(HeaderKey, encode(other.Public())) }, http.StatusForbidden},
		{"signed for DELETE", func(r *http.Request) {
			r.Header.Set(HeaderSig, encode(k.Sign(RequestMessage(http.MethodDelete, fp, "", t0))))
		}, http.StatusUnauthorized},
		{"signed for a device", func(r *http.Request) {
			r.Header.Set(HeaderSig, encode(k.Sign(RequestMessage(http.MethodGet, fp, "work-laptop", t0))))
		}, http.StatusUnauthorized},
		{"snapshot signature", func(r *http.Request) {
			r.Header.Set(HeaderSig, encode(k.Sign(SnapshotMessage(RequestMessage(http.MethodGet, fp, "", t0)))))
		}, http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := signedRequest(t, e.ts.URL, http.MethodGet, path, k, http.MethodGet, fp, "", t0)
			c.edit(req)
			if resp, msg := send(t, req); resp.StatusCode != c.code {
				t.Fatalf("status = %d (%s), want %d", resp.StatusCode, strings.TrimSpace(msg), c.code)
			}
		})
	}
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/teams/NOT-A-TEAM", nil)
	if resp, _ := send(t, req); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad team path: status = %d", resp.StatusCode)
	}
}

func TestDelete(t *testing.T) {
	e := newRelay(t, Limits{})
	k := newKey(t)
	c := e.client(k)
	ctx := context.Background()
	fp := k.Fingerprint()
	for _, dev := range []string{"device-one", "device-two"} {
		if err := c.Publish(ctx, dev, marshal(t, docFor(k, dev, t0))); err != nil {
			t.Fatal(err)
		}
	}

	// A delete signature names its device.
	req := signedRequest(t, e.ts.URL, http.MethodDelete, "/v1/teams/"+fp+"/devices/device-two", k, http.MethodDelete, fp, "device-one", t0)
	if resp, _ := send(t, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("delete signed for another device: status = %d", resp.StatusCode)
	}
	// A read signature is not a delete signature.
	req = signedRequest(t, e.ts.URL, http.MethodDelete, "/v1/teams/"+fp+"/devices/device-two", k, http.MethodGet, fp, "device-two", t0)
	if resp, _ := send(t, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("delete with a GET signature: status = %d", resp.StatusCode)
	}
	req = signedRequest(t, e.ts.URL, http.MethodDelete, "/v1/teams/"+fp+"/devices/BAD", k, http.MethodDelete, fp, "BAD", t0)
	if resp, _ := send(t, req); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad device path: status = %d", resp.StatusCode)
	}

	if err := c.Remove(ctx, "device-two"); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, "device-two"); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
	devices, _, err := c.Pull(ctx)
	if err != nil || len(devices) != 1 || devices[0].Doc.Device != "device-one" {
		t.Fatalf("after delete: %d devices, %v", len(devices), err)
	}
}

func TestHealth(t *testing.T) {
	e := newRelay(t, Limits{})
	resp, body := send(t, func() *http.Request { r, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/health", nil); return r }())
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, headers %v", resp.StatusCode, resp.Header)
	}
	var h struct {
		OK      bool `json:"ok"`
		Version int  `json:"snapshot_version"`
	}
	if err := json.Unmarshal([]byte(body), &h); err != nil || !h.OK || h.Version != snapshot.Version {
		t.Fatalf("health = %s", body)
	}
}

func TestNewServerFillsUnsetLimits(t *testing.T) {
	s := NewServer(NewMemory(), Limits{DevicesPerTeam: 3})
	want := DefaultLimits()
	want.DevicesPerTeam = 3
	if s.Limits != want {
		t.Fatalf("Limits = %+v, want %+v", s.Limits, want)
	}
	if NewServer(NewMemory(), Limits{}).Limits != DefaultLimits() {
		t.Fatal("zero Limits is not DefaultLimits")
	}
	// A window under a second set after NewServer must not divide by zero.
	s.Limits.Window = 0
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// brokenStore fails the named operations.
type brokenStore struct {
	*Memory
	fail map[string]bool
}

var errBroken = errors.New("store is down")

func (b brokenStore) Get(ctx context.Context, teamFP, device string) (*Record, error) {
	if b.fail["get"] {
		return nil, errBroken
	}
	return b.Memory.Get(ctx, teamFP, device)
}

func (b brokenStore) Put(ctx context.Context, teamFP, device string, rec Record, ttl time.Duration) error {
	if b.fail["put"] {
		return errBroken
	}
	return b.Memory.Put(ctx, teamFP, device, rec, ttl)
}

func (b brokenStore) Delete(ctx context.Context, teamFP, device string) error {
	if b.fail["delete"] {
		return errBroken
	}
	return b.Memory.Delete(ctx, teamFP, device)
}

func (b brokenStore) List(ctx context.Context, teamFP string) (map[string]Record, error) {
	if b.fail["list"] {
		return nil, errBroken
	}
	return b.Memory.List(ctx, teamFP)
}

func (b brokenStore) Count(ctx context.Context, key string, window time.Duration) (int64, error) {
	if b.fail["count"] {
		return 0, errBroken
	}
	return b.Memory.Count(ctx, key, window)
}

func TestStoreFailuresAre503(t *testing.T) {
	k := newKey(t)
	ctx := context.Background()
	body := marshal(t, docFor(k, "work-laptop", t0))
	for _, op := range []string{"count", "get", "list", "put", "delete"} {
		t.Run(op, func(t *testing.T) {
			store := brokenStore{Memory: NewMemory(), fail: map[string]bool{}}
			srv := NewServer(store, Limits{})
			srv.Now = func() time.Time { return t0 }
			ts := httptest.NewServer(srv)
			defer ts.Close()
			c := &Client{BaseURL: ts.URL, Key: k, Now: func() time.Time { return t0 }}
			store.fail[op] = true
			var err error
			switch op {
			case "delete":
				err = c.Remove(ctx, "work-laptop")
			case "list":
				if err = c.Publish(ctx, "work-laptop", body); statusOf(err) == http.StatusServiceUnavailable {
					_, _, err = c.Pull(ctx)
				}
			default:
				err = c.Publish(ctx, "work-laptop", body)
			}
			if statusOf(err) != http.StatusServiceUnavailable {
				t.Fatalf("err = %v, want 503", err)
			}
		})
	}
}

func TestClientErrors(t *testing.T) {
	k := newKey(t)
	ctx := context.Background()
	for _, base := range []string{"", "  ", "/"} {
		c := &Client{BaseURL: base, Key: k}
		if err := c.Publish(ctx, "work-laptop", []byte("{}")); !errors.Is(err, ErrNoRelay) {
			t.Fatalf("Publish with base %q: %v", base, err)
		}
		if _, _, err := c.Pull(ctx); !errors.Is(err, ErrNoRelay) {
			t.Fatalf("Pull with base %q: %v", base, err)
		}
		if err := c.Remove(ctx, "work-laptop"); !errors.Is(err, ErrNoRelay) {
			t.Fatalf("Remove with base %q: %v", base, err)
		}
	}

	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	err := (&Client{BaseURL: url, Key: k}).Publish(ctx, "work-laptop", []byte("{}"))
	if err == nil || statusOf(err) != 0 || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("closed relay: %v", err)
	}

	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "<html>bad gateway</html>")
	}))
	defer ts.Close()
	err = (&Client{BaseURL: ts.URL, Key: k}).Publish(ctx, "work-laptop", []byte("{}"))
	if statusOf(err) != http.StatusBadGateway || err.Error() != "relay: HTTP 502" {
		t.Fatalf("non-JSON error: %v", err)
	}
	if got := (&ErrStatus{Code: 409, Msg: "newer"}).Error(); got != "relay: newer (HTTP 409)" {
		t.Fatalf("ErrStatus = %q", got)
	}
}

// The client signs exactly what it sends; nothing in between may reformat it.
func TestPublishSignsTheExactBody(t *testing.T) {
	k := newKey(t)
	body := []byte(`{"any":"bytes"}`)
	var got []byte
	var sig []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		sig, _ = decode(r.Header.Get(HeaderSig))
		if r.Header.Get(HeaderKey) != encode(k.Public()) || r.Method != http.MethodPut || r.URL.Path != "/v1/teams/"+k.Fingerprint()+"/devices/work-laptop" {
			t.Errorf("request %s %s key %s", r.Method, r.URL.Path, r.Header.Get(HeaderKey))
		}
		w.Write([]byte(`{"stored":true}`))
	}))
	defer ts.Close()
	if err := (&Client{BaseURL: ts.URL, Key: k}).Publish(context.Background(), "work-laptop", body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) || !ed25519.Verify(k.Public(), SnapshotMessage(body), sig) {
		t.Fatal("the relay did not receive the signed bytes")
	}
}

// The canonical check must accept whatever json.Marshal makes of a collector
// document: zone offsets, nanoseconds, awkward floats, omitted fields.
func TestCollectorEncodingIsCanonical(t *testing.T) {
	e := newRelay(t, Limits{})
	k := newKey(t)
	ctx := context.Background()
	zones := map[string]*time.Location{
		"utc":   time.UTC,
		"local": time.Local,
		"msk":   time.FixedZone("MSK", 3*3600),
		"nst":   time.FixedZone("NST", -(3*3600 + 30*60)),
	}
	for name, loc := range zones {
		dev := "device-" + name
		at := t0.Add(-time.Second + 123456789).In(loc)
		d := docFor(k, dev, at)
		d.LastSuccessAt = time.Time{}
		d.Accounts[0].Plan = ""
		d.Accounts[0].Windows = []snapshot.Window{
			{Name: "5h", Percent: 100.0 / 3},
			{Name: "7d", Percent: 1e-7, Minutes: 10080},
			{Name: "opus 7d", Percent: 999.9999999999999},
			{Name: "credits", Percent: 0},
		}
		d.Accounts = append(d.Accounts, snapshot.Account{
			Provider: "hermes", Label: k.Seal("openrouter"), Windows: []snapshot.Window{}, Projects: []snapshot.Project{},
		})
		if err := e.client(k).Publish(ctx, dev, marshal(t, d)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSlowBodyTimesOut(t *testing.T) {
	old := bodyTimeout
	bodyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { bodyTimeout = old })
	e := newRelay(t, Limits{})
	k := newKey(t)
	conn, err := net.Dial("tcp", e.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Promise a body and send one byte of it.
	fmt.Fprintf(conn, "PUT /v1/teams/%s/devices/work-laptop HTTP/1.1\r\nHost: relay\r\n%s: %s\r\nContent-Length: 1000\r\n\r\n{",
		k.Fingerprint(), HeaderKey, encode(k.Public()))
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer to a stalled body: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
