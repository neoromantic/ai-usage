package relay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/team"
)

func TestTeamWriteLimit(t *testing.T) {
	e := newRelay(t)
	e.srv.Limits.WritesPerTeam = 2
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
	e := newRelay(t)
	e.srv.Limits.RequestsPerIP = 3
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
	e := newRelay(t)
	e.srv.Limits.NewTeamsPerIP = 1
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
	e := newRelay(t)
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
	if err := e.publish(t, e.client(k), k, "device-00"); err != nil {
		t.Fatalf("a known device: %v", err)
	}
	// The last key stored nothing today; the next day it may.
	e.clock.Add(23 * time.Hour)
	k = keys[len(keys)-1]
	if err := e.publish(t, e.client(k), k, "device-00"); err != nil {
		t.Fatalf("next day: %v", err)
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

// On Vercel KV every counted request is paid for. A path the relay does not
// serve, and a client that is already over its limit, cost nothing.
//
// Behind Caddy, cloudflared, or a load balancer, the client's own X-Real-Ip
// reaches the relay, so by default every header is ignored and a client
// cannot pick a fresh rate-limit key for each request: the health requests
// below name a new address each, and still share one limit.
func TestRejectedRequestsSkipTheStore(t *testing.T) {
	store := &testStore{Store: NewMemory()}
	srv := NewServer(store)
	srv.Limits.RequestsPerIP = 5
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
	for i := range 50 {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/health", nil)
		req.Header.Set("X-Real-Ip", "203.0.113."+strconv.Itoa(i))
		req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i))
		resp, _ := send(t, req)
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
	// A client that comes back later, under another window's key, still has
	// the ended key dropped within a minute.
	o.has("next-window", 205)
	if _, ok := o.until["late"]; !ok {
		t.Fatal("dropped before a minute had passed since the last sweep")
	}
	o.has("next-window", 210)
	if len(o.until) != 0 {
		t.Fatalf("%d keys held a minute after their windows ended", len(o.until))
	}
}
