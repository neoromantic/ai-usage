package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/relay"
)

var now = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// fresh clears the lazily built relay and the store environment, so each test
// decides which store Handler finds.
func fresh(t *testing.T) {
	t.Helper()
	for _, k := range []string{"KV_REST_API_URL", "KV_REST_API_TOKEN", "UPSTASH_REDIS_REST_URL", "UPSTASH_REDIS_REST_TOKEN", "VERCEL", "VERCEL_ENV"} {
		t.Setenv(k, "")
	}
	reset := func() {
		once = sync.Once{}
		served = nil
	}
	reset()
	t.Cleanup(reset)
}

// pinClock fixes the relay's clock once it is built.
func pinClock(t *testing.T) {
	t.Helper()
	srv, ok := instance().(*relay.Server)
	if !ok {
		t.Fatalf("handler is %T, want *relay.Server", instance())
	}
	srv.Now = func() time.Time { return now }
}

// vercel stands in for the platform: it applies the vercel.json rewrite and
// calls the function with the rewritten URL, as the Go runtime does.
func vercel() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/v1/"); ok {
			r.URL.Path = "/api/relay"
			r.URL.RawPath = ""
			r.URL.RawQuery = "path=" + url.QueryEscape(rest)
			r.RequestURI = r.URL.RequestURI()
		}
		Handler(w, r)
	})
}

func get(t *testing.T, h http.Handler, target string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s: body %q is not JSON", target, rec.Body.String())
	}
	return rec.Code, body
}

func TestRestorePath(t *testing.T) {
	tests := []struct {
		target, path, query string
	}{
		{"/api/relay?path=teams/abc/devices/d-0123456789", "/v1/teams/abc/devices/d-0123456789", ""},
		{"/api/relay?path=teams%2Fabc", "/v1/teams/abc", ""},
		{"/api/relay?path=%2Fhealth", "/v1/health", ""},
		{"/api/relay?path=health&x=1", "/v1/health", "x=1"},
		{"/api/relay?path=", "/v1/", ""},
		// A platform that keeps the original path needs no help.
		{"/v1/health?path=other", "/v1/health", "path=other"},
		// Without the query there is nothing to restore; the relay answers 404.
		{"/api/relay", "/api/relay", ""},
	}
	for _, tc := range tests {
		r := httptest.NewRequest(http.MethodGet, tc.target, nil)
		restorePath(r)
		if r.URL.Path != tc.path || r.URL.RawQuery != tc.query {
			t.Errorf("%s: got path %q query %q, want %q %q", tc.target, r.URL.Path, r.URL.RawQuery, tc.path, tc.query)
		}
		if r.RequestURI != r.URL.RequestURI() {
			t.Errorf("%s: RequestURI %q, want %q", tc.target, r.RequestURI, r.URL.RequestURI())
		}
	}
}

func TestStoreChoice(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantCode  int
		wantError string
	}{
		{"memory outside Vercel", nil, http.StatusOK, ""},
		{"memory on Vercel", map[string]string{"VERCEL": "1", "VERCEL_ENV": "production"}, http.StatusServiceUnavailable, "relay store not configured"},
		{"memory on a preview", map[string]string{"VERCEL": "1", "VERCEL_ENV": "preview"}, http.StatusServiceUnavailable, "relay store not configured"},
		{"memory under vercel dev", map[string]string{"VERCEL": "1", "VERCEL_ENV": "development"}, http.StatusOK, ""},
		// One half of the KV pair is not a store.
		{"URL without token on Vercel", map[string]string{"VERCEL": "1", "KV_REST_API_URL": "http://127.0.0.1:1"}, http.StatusServiceUnavailable, "relay store not configured"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			code, body := get(t, vercel(), "/v1/health")
			if code != tc.wantCode {
				t.Fatalf("status %d, want %d (%v)", code, tc.wantCode, body)
			}
			if tc.wantError != "" && body["error"] != tc.wantError {
				t.Fatalf("error %v, want %q", body["error"], tc.wantError)
			}
			if tc.wantError == "" && body["ok"] != true {
				t.Fatalf("health body %v", body)
			}
		})
	}
}

// fakeUpstash answers every pipeline command with 1, which is all a health
// check needs, and refuses any other token.
func fakeUpstash(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/pipeline" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var cmds [][]any
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &cmds); err != nil {
			http.Error(w, "bad pipeline", http.StatusBadRequest)
			return
		}
		out := make([]map[string]any, len(cmds))
		for i := range cmds {
			out[i] = map[string]any{"result": 1}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestKVOnVercel(t *testing.T) {
	for _, names := range [][2]string{
		{"KV_REST_API_URL", "KV_REST_API_TOKEN"},
		{"UPSTASH_REDIS_REST_URL", "UPSTASH_REDIS_REST_TOKEN"},
	} {
		t.Run(names[0], func(t *testing.T) {
			fresh(t)
			var calls atomic.Int32
			kv := fakeUpstash(t, &calls)
			t.Setenv("VERCEL", "1")
			t.Setenv(names[0], kv.URL)
			t.Setenv(names[1], "tok")
			code, body := get(t, vercel(), "/v1/health")
			if code != http.StatusOK || body["ok"] != true {
				t.Fatalf("status %d body %v", code, body)
			}
			if calls.Load() == 0 {
				t.Fatal("the relay did not use the KV store")
			}
		})
	}
}

// Each client is limited on its own address even when the function sees no
// peer address, because Vercel sets X-Real-Ip itself.
func TestClientAddressFromVercel(t *testing.T) {
	fresh(t)
	pinClock(t)
	srv := instance().(*relay.Server)
	srv.Limits.RequestsPerIP = 1
	health := func(ip string) int {
		r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		r.RemoteAddr = ""
		r.Header.Set("X-Real-Ip", ip)
		rec := httptest.NewRecorder()
		vercel().ServeHTTP(rec, r)
		return rec.Code
	}
	if health("203.0.113.1") != http.StatusOK || health("203.0.113.2") != http.StatusOK {
		t.Fatal("two clients share one rate limit")
	}
	if code := health("203.0.113.1"); code != http.StatusTooManyRequests {
		t.Fatalf("second request from one client: status %d, want 429", code)
	}
}

// The store is chosen once per instance; a later environment change does not
// swap it under running requests.
func TestBuiltOnce(t *testing.T) {
	fresh(t)
	t.Setenv("VERCEL", "1")
	if code, _ := get(t, vercel(), "/v1/health"); code != http.StatusServiceUnavailable {
		t.Fatalf("first request: status %d", code)
	}
	t.Setenv("VERCEL", "")
	if code, _ := get(t, vercel(), "/v1/health"); code != http.StatusServiceUnavailable {
		t.Fatalf("second request: status %d, want the first instance's answer", code)
	}
}

func testDoc(t *testing.T, key *team.Key, device string) []byte {
	t.Helper()
	doc := snapshot.Doc{
		V:                snapshot.Version,
		Team:             key.Fingerprint(),
		Device:           device,
		DeviceLabel:      key.Seal("studio"),
		OSUser:           key.Seal("sam"),
		CollectorVersion: "v1.0.0",
		CollectedAt:      now.Add(-time.Minute),
		LastSuccessAt:    now.Add(-time.Minute),
		Accounts: []snapshot.Account{{
			Provider: "codex",
			Label:    key.Seal("sam@example.com"),
			Current:  true,
			Windows:  []snapshot.Window{},
			Sessions: 3,
			Tokens:   snapshot.Tokens{Input: 10, Output: 20, CacheRead: 30},
			Projects: []snapshot.Project{},
		}},
		Sources: []snapshot.Source{{Provider: "codex", Status: "ok"}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A collector talks to the deployed URL exactly as it talks to `relay serve`.
func TestClientThroughRewrite(t *testing.T) {
	fresh(t)
	pinClock(t)
	srv := httptest.NewServer(vercel())
	defer srv.Close()

	key, err := team.Generate()
	if err != nil {
		t.Fatal(err)
	}
	c := &relay.Client{BaseURL: srv.URL + "/", Key: key, HTTP: srv.Client(), Now: func() time.Time { return now }}
	ctx := context.Background()
	const device = "d-0123456789abcdef01234567"
	body := testDoc(t, key, device)
	if err := c.Publish(ctx, device, body); err != nil {
		t.Fatalf("publish: %v", err)
	}
	devices, bad, err := c.Pull(ctx)
	if err != nil || bad != 0 || len(devices) != 1 {
		t.Fatalf("pull: %d devices, %d bad, err %v", len(devices), bad, err)
	}
	if string(devices[0].Body) != string(body) {
		t.Fatal("pulled body differs from the published one")
	}

	// Another team's key cannot read this team through the same URL.
	other, err := team.Generate()
	if err != nil {
		t.Fatal(err)
	}
	stranger := &relay.Client{BaseURL: srv.URL, Key: other, HTTP: srv.Client(), Now: c.Now}
	if devices, _, err := stranger.Pull(ctx); err != nil || len(devices) != 0 {
		t.Fatalf("other team sees %d devices, err %v", len(devices), err)
	}

	if err := c.Remove(ctx, device); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if devices, _, err := c.Pull(ctx); err != nil || len(devices) != 0 {
		t.Fatalf("after remove: %d devices, err %v", len(devices), err)
	}
}

// A body that is not a snapshot is refused through the function as well.
func TestRejectsNonSnapshot(t *testing.T) {
	fresh(t)
	pinClock(t)
	key, err := team.Generate()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"v":1,"note":"anything else"}`)
	r := httptest.NewRequest(http.MethodPut, "/v1/teams/"+key.Fingerprint()+"/devices/d-0123456789abcdef01234567", strings.NewReader(string(body)))
	r.Header.Set(relay.HeaderKey, encode(key.Public()))
	r.Header.Set(relay.HeaderSig, encode(key.Sign(relay.SnapshotMessage(body))))
	rec := httptest.NewRecorder()
	vercel().ServeHTTP(rec, r)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
