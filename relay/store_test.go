package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

func TestMemoryTTL(t *testing.T) {
	c := &clock{t: t0}
	m := NewMemory()
	m.now = c.Now
	ctx := context.Background()
	rec := Record{Body: []byte("body"), Sig: []byte("sig")}
	if err := m.Put(ctx, "team", "short-lived", rec, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := m.Put(ctx, "team", "long-lived", rec, 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Get(ctx, "team", "short-lived"); got == nil || string(got.Body) != "body" {
		t.Fatalf("Get = %+v", got)
	}
	c.Add(2 * time.Hour)
	if got, _ := m.Get(ctx, "team", "short-lived"); got != nil {
		t.Fatal("expired record is still returned")
	}
	recs, _ := m.List(ctx, "team")
	if len(recs) != 1 || recs["long-lived"].Body == nil {
		t.Fatalf("List = %v", recs)
	}
	if got, _ := m.Get(ctx, "team", "missing"); got != nil {
		t.Fatal("Get(missing) returned a record")
	}
	if err := m.Delete(ctx, "team", "long-lived"); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "no-team", "x"); err != nil {
		t.Fatal(err)
	}
	if recs, _ := m.List(ctx, "team"); len(recs) != 0 {
		t.Fatalf("List after delete = %v", recs)
	}
}

func TestMemoryCountAndSweep(t *testing.T) {
	c := &clock{t: t0}
	m := NewMemory()
	m.now = c.Now
	ctx := context.Background()
	for want := int64(1); want <= 3; want++ {
		if n, _ := m.Count(ctx, "k", time.Minute); n != want {
			t.Fatalf("Count = %d, want %d", n, want)
		}
	}
	c.Add(time.Minute + time.Second)
	if n, _ := m.Count(ctx, "k", time.Minute); n != 1 {
		t.Fatalf("after the window Count = %d, want 1", n)
	}

	// A long-running relay sees new counter keys every window.
	_ = m.Put(ctx, "gone-team", "device", Record{}, time.Minute)
	_ = m.Put(ctx, "live-team", "device", Record{}, 24*time.Hour)
	for i := range sweepEvery {
		_, _ = m.Count(ctx, "ip:"+strconv.Itoa(i), time.Minute)
	}
	c.Add(2 * time.Minute)
	for i := range sweepEvery {
		_, _ = m.Count(ctx, "next:"+strconv.Itoa(i), time.Minute)
	}
	m.mu.Lock()
	counters, teams := len(m.count), len(m.docs)
	m.mu.Unlock()
	if counters > sweepEvery+1 {
		t.Fatalf("%d counters kept, want expired ones dropped", counters)
	}
	if teams != 1 {
		t.Fatalf("%d teams kept, want only the live one", teams)
	}

	// A quiet relay forgets an address within an hour of its window.
	c.Add(2 * time.Hour)
	_, _ = m.Count(ctx, "ip:quiet", time.Minute)
	m.mu.Lock()
	counters = len(m.count)
	m.mu.Unlock()
	if counters != 1 {
		t.Fatalf("%d counters kept after an hour, want 1", counters)
	}
}

// fakeKV is the part of the Vercel KV REST API the store uses: POST
// /pipeline with a JSON array of commands, answered by a JSON array of
// {"result"} or {"error"}.
type fakeKV struct {
	mu    sync.Mutex
	now   func() time.Time
	token string
	str   map[string]string
	sets  map[string]map[string]bool
	exp   map[string]time.Time
	seen  [][]string
	// reply, when set, answers the next request instead of the store.
	reply func(w http.ResponseWriter)
}

func newFakeKV(now func() time.Time) *fakeKV {
	return &fakeKV{now: now, token: "test-token", str: map[string]string{}, sets: map[string]map[string]bool{}, exp: map[string]time.Time{}}
}

func (f *fakeKV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if reply := f.reply; reply != nil {
		f.reply = nil
		reply(w)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/pipeline" {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"Unauthorized"}`)
		return
	}
	var cmds [][]string
	if err := json.NewDecoder(r.Body).Decode(&cmds); err != nil {
		http.Error(w, `{"error":"bad pipeline"}`, http.StatusBadRequest)
		return
	}
	out := make([]map[string]any, len(cmds))
	for i, cmd := range cmds {
		f.seen = append(f.seen, cmd)
		res, err := f.run(cmd)
		if err != nil {
			out[i] = map[string]any{"error": err.Error()}
		} else {
			out[i] = map[string]any{"result": res}
		}
	}
	json.NewEncoder(w).Encode(out)
}

func (f *fakeKV) expire(k string) {
	if at, ok := f.exp[k]; ok && !f.now().Before(at) {
		delete(f.str, k)
		delete(f.sets, k)
		delete(f.exp, k)
	}
}

func (f *fakeKV) exists(k string) bool {
	_, s := f.str[k]
	_, set := f.sets[k]
	return s || set
}

var errWrongType = errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")

func (f *fakeKV) run(cmd []string) (any, error) {
	if len(cmd) == 0 {
		return nil, errors.New("ERR empty command")
	}
	args := cmd[1:]
	for _, k := range args {
		f.expire(k)
	}
	need := func(n int) error {
		if len(args) < n {
			return errors.New("ERR wrong number of arguments for '" + strings.ToLower(cmd[0]) + "' command")
		}
		return nil
	}
	switch strings.ToUpper(cmd[0]) {
	case "GET":
		if err := need(1); err != nil {
			return nil, err
		}
		if _, ok := f.sets[args[0]]; ok {
			return nil, errWrongType
		}
		if v, ok := f.str[args[0]]; ok {
			return v, nil
		}
		return nil, nil
	case "SET":
		if err := need(2); err != nil {
			return nil, err
		}
		k := args[0]
		delete(f.sets, k)
		delete(f.exp, k)
		f.str[k] = args[1]
		if len(args) == 4 && strings.EqualFold(args[2], "EX") {
			secs, err := strconv.Atoi(args[3])
			if err != nil || secs <= 0 {
				return nil, errors.New("ERR invalid expire time in 'set' command")
			}
			f.exp[k] = f.now().Add(time.Duration(secs) * time.Second)
		} else if len(args) != 2 {
			return nil, errors.New("ERR syntax error")
		}
		return "OK", nil
	case "SADD", "SREM":
		if err := need(2); err != nil {
			return nil, err
		}
		k := args[0]
		if _, ok := f.str[k]; ok {
			return nil, errWrongType
		}
		set := f.sets[k]
		if set == nil {
			set = map[string]bool{}
		}
		n := 0
		for _, m := range args[1:] {
			if strings.EqualFold(cmd[0], "SADD") && !set[m] {
				set[m] = true
				n++
			}
			if strings.EqualFold(cmd[0], "SREM") && set[m] {
				delete(set, m)
				n++
			}
		}
		if len(set) == 0 {
			delete(f.sets, k)
			delete(f.exp, k)
		} else {
			f.sets[k] = set
		}
		return n, nil
	case "SMEMBERS":
		if err := need(1); err != nil {
			return nil, err
		}
		if _, ok := f.str[args[0]]; ok {
			return nil, errWrongType
		}
		out := []string{}
		for m := range f.sets[args[0]] {
			out = append(out, m)
		}
		sort.Strings(out)
		return out, nil
	case "SCARD":
		if err := need(1); err != nil {
			return nil, err
		}
		if _, ok := f.str[args[0]]; ok {
			return nil, errWrongType
		}
		return len(f.sets[args[0]]), nil
	case "MGET":
		if err := need(1); err != nil {
			return nil, err
		}
		out := make([]any, len(args))
		for i, k := range args {
			if v, ok := f.str[k]; ok {
				out[i] = v
			}
		}
		return out, nil
	case "DEL":
		if err := need(1); err != nil {
			return nil, err
		}
		n := 0
		for _, k := range args {
			if f.exists(k) {
				n++
			}
			delete(f.str, k)
			delete(f.sets, k)
			delete(f.exp, k)
		}
		return n, nil
	case "INCR":
		if err := need(1); err != nil {
			return nil, err
		}
		k := args[0]
		if _, ok := f.sets[k]; ok {
			return nil, errWrongType
		}
		n := int64(0)
		if v, ok := f.str[k]; ok {
			var err error
			if n, err = strconv.ParseInt(v, 10, 64); err != nil {
				return nil, errors.New("ERR value is not an integer or out of range")
			}
		}
		n++
		f.str[k] = strconv.FormatInt(n, 10)
		return n, nil
	case "EXPIRE":
		if err := need(2); err != nil {
			return nil, err
		}
		k := args[0]
		secs, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		nx, gt := false, false
		for _, opt := range args[2:] {
			switch strings.ToUpper(opt) {
			case "NX":
				nx = true
			case "GT":
				gt = true
			default:
				return nil, errors.New("ERR Unsupported option " + opt)
			}
		}
		if !f.exists(k) {
			return 0, nil
		}
		at := f.now().Add(time.Duration(secs) * time.Second)
		cur, has := f.exp[k]
		// GT takes a key with no expiry as one that never expires.
		if has && nx || gt && (!has || !at.After(cur)) {
			return 0, nil
		}
		f.exp[k] = at
		return 1, nil
	}
	return nil, errors.New("ERR unknown command '" + cmd[0] + "'")
}

func newKV(t *testing.T) (*KV, *fakeKV, *clock) {
	t.Helper()
	c := &clock{t: t0}
	f := newFakeKV(c.Now)
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return &KV{URL: ts.URL, Token: f.token, HTTP: ts.Client()}, f, c
}

func TestKVPutGetDelete(t *testing.T) {
	kv, f, _ := newKV(t)
	ctx := context.Background()
	if got, err := kv.Get(ctx, "team", "device-one"); got != nil || err != nil {
		t.Fatalf("Get(missing) = %+v, %v", got, err)
	}
	rec := Record{Body: []byte(`{"v":1}`), Sig: []byte{0, 1, 2, 255}, Since: t0}
	if err := kv.Put(ctx, "team", "device-one", rec, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err := kv.Get(ctx, "team", "device-one")
	if err != nil || got == nil || string(got.Body) != `{"v":1}` || string(got.Sig) != string(rec.Sig) || !got.Since.Equal(t0) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	f.mu.Lock()
	ttl := f.exp[docKey("team", "device-one")].Sub(t0)
	setTTL := f.exp[setKey("team")].Sub(t0)
	f.mu.Unlock()
	if ttl != 90*24*time.Hour || setTTL != ttl {
		t.Fatalf("record TTL %v, device set TTL %v", ttl, setTTL)
	}
	if err := kv.Delete(ctx, "team", "device-one"); err != nil {
		t.Fatal(err)
	}
	if got, err := kv.Get(ctx, "team", "device-one"); got != nil || err != nil {
		t.Fatalf("Get after Delete = %+v, %v", got, err)
	}
	if recs, err := kv.List(ctx, "team"); err != nil || len(recs) != 0 {
		t.Fatalf("List after Delete = %v, %v", recs, err)
	}
}

func TestKVListPrunesExpired(t *testing.T) {
	kv, f, c := newKV(t)
	ctx := context.Background()
	if recs, err := kv.List(ctx, "team"); err != nil || len(recs) != 0 {
		t.Fatalf("List(empty) = %v, %v", recs, err)
	}
	_ = kv.Put(ctx, "team", "device-one", Record{Body: []byte("one")}, time.Hour)
	_ = kv.Put(ctx, "team", "device-two", Record{Body: []byte("two")}, 3*time.Hour)
	_ = kv.Put(ctx, "other", "device-one", Record{Body: []byte("other")}, 3*time.Hour)
	recs, err := kv.List(ctx, "team")
	if err != nil || len(recs) != 2 || string(recs["device-one"].Body) != "one" {
		t.Fatalf("List = %v, %v", recs, err)
	}
	c.Add(2 * time.Hour)
	recs, err = kv.List(ctx, "team")
	if err != nil || len(recs) != 1 || string(recs["device-two"].Body) != "two" {
		t.Fatalf("List after expiry = %v, %v", recs, err)
	}
	f.mu.Lock()
	members := f.sets[setKey("team")]
	f.mu.Unlock()
	if members["device-one"] || !members["device-two"] {
		t.Fatalf("device set = %v, want the expired device pruned", members)
	}

	// A record that is not JSON is skipped rather than failing the team read.
	f.mu.Lock()
	f.str[docKey("team", "device-two")] = "not json"
	f.mu.Unlock()
	if recs, err := kv.List(ctx, "team"); err != nil || len(recs) != 0 {
		t.Fatalf("List with a broken record = %v, %v", recs, err)
	}
	if _, err := kv.Get(ctx, "team", "device-two"); err == nil {
		t.Fatal("Get of a broken record did not fail")
	}
}

// A device that joined last, with the shortest lifetime, writing last does not
// unlist the others while their records live.
func TestKVSetOutlivesTheLastWrite(t *testing.T) {
	kv, _, c := newKV(t)
	ctx := context.Background()
	_ = kv.Put(ctx, "team", "device-old", Record{Body: []byte("old")}, 30*24*time.Hour)
	_ = kv.Put(ctx, "team", "device-new", Record{Body: []byte("new")}, 7*24*time.Hour)
	c.Add(8 * 24 * time.Hour)
	recs, err := kv.List(ctx, "team")
	if err != nil || len(recs) != 1 || string(recs["device-old"].Body) != "old" {
		t.Fatalf("List = %v, %v", recs, err)
	}
}

// A full team of the largest snapshots comes back in one MGET, which must fit
// under maxKVResponse.
func TestKVListFullTeam(t *testing.T) {
	kv, _, _ := newKV(t)
	ctx := context.Background()
	n := DefaultLimits().DevicesPerTeam
	rec := Record{Body: []byte(strings.Repeat("\xff", snapshot.MaxBytes)), Sig: make([]byte, 64), Since: t0}
	for i := range n {
		if err := kv.Put(ctx, "team", "device-"+strconv.Itoa(1000+i), rec, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := kv.List(ctx, "team")
	if err != nil || len(recs) != n {
		t.Fatalf("List = %d records, %v; want %d", len(recs), err, n)
	}
}

func TestKVCount(t *testing.T) {
	kv, f, c := newKV(t)
	ctx := context.Background()
	for want := int64(1); want <= 3; want++ {
		n, err := kv.Count(ctx, "ip:1", time.Minute)
		if err != nil || n != want {
			t.Fatalf("Count = %d, %v; want %d", n, err, want)
		}
		c.Add(15 * time.Second)
	}
	// NX keeps the first TTL, so later hits do not stretch the window.
	f.mu.Lock()
	left := f.exp["aiu:rl:ip:1"].Sub(c.Now())
	f.mu.Unlock()
	if left != 15*time.Second {
		t.Fatalf("counter expires in %v, want 15s", left)
	}
	c.Add(15 * time.Second)
	if n, err := kv.Count(ctx, "ip:1", time.Minute); err != nil || n != 1 {
		t.Fatalf("Count after the window = %d, %v; want 1", n, err)
	}
}

func TestKVErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(*KV, *fakeKV)
		// want is what the error must mention: the HTTP code or the store's
		// own message. Empty means any error.
		want string
	}{
		{"wrong token", func(kv *KV, _ *fakeKV) { kv.Token = "nope" }, "HTTP 401"},
		{"server error", func(_ *KV, f *fakeKV) {
			f.reply = func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }
		}, "HTTP 500"},
		{"not json", func(_ *KV, f *fakeKV) {
			f.reply = func(w http.ResponseWriter) { io.WriteString(w, "<html>") }
		}, ""},
		{"too few results", func(_ *KV, f *fakeKV) {
			f.reply = func(w http.ResponseWriter) { io.WriteString(w, `[]`) }
		}, ""},
		{"command error", func(_ *KV, f *fakeKV) {
			f.reply = func(w http.ResponseWriter) { io.WriteString(w, `[{"error":"ERR max requests limit exceeded"}]`) }
		}, "max requests limit exceeded"},
		{"wrong type", func(_ *KV, f *fakeKV) {
			f.sets[docKey("team", "device-one")] = map[string]bool{"x": true}
		}, "WRONGTYPE"},
		{"unreachable", func(kv *KV, _ *fakeKV) { kv.URL = "http://127.0.0.1:1" }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kv, f, _ := newKV(t)
			f.mu.Lock()
			c.setup(kv, f)
			f.mu.Unlock()
			_, err := kv.Get(ctx, "team", "device-one")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}

	// Every operation reports a failing store.
	kv, f, _ := newKV(t)
	kv.Token = "nope"
	if err := kv.Put(ctx, "team", "d", Record{}, time.Hour); err == nil {
		t.Error("Put did not fail")
	}
	if err := kv.Delete(ctx, "team", "d"); err == nil {
		t.Error("Delete did not fail")
	}
	if _, err := kv.List(ctx, "team"); err == nil {
		t.Error("List did not fail")
	}
	if _, err := kv.Count(ctx, "k", time.Minute); err == nil {
		t.Error("Count did not fail")
	}
	kv.Token = f.token
	f.mu.Lock()
	f.str[setKey("team")] = "not a set"
	f.mu.Unlock()
	if _, err := kv.List(ctx, "team"); err == nil {
		t.Error("List of a broken device set did not fail")
	}
}

// The relay runs the same over KV as over memory.
func TestRelayOverKV(t *testing.T) {
	kv, f, c := newKV(t)
	srv := NewServer(kv)
	srv.Limits.DevicesPerTeam = 2
	srv.Now = c.Now
	ts := httptest.NewServer(srv)
	defer ts.Close()
	k := newKey(t)
	ctx := context.Background()
	client := &Client{BaseURL: ts.URL, Key: k, HTTP: ts.Client(), Now: c.Now}
	for _, dev := range []string{"device-one", "device-two"} {
		if err := client.Publish(ctx, dev, marshal(t, docFor(k, dev, t0))); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.Publish(ctx, "device-three", marshal(t, docFor(k, "device-three", t0))); statusOf(err) != http.StatusForbidden {
		t.Fatalf("third device: %v", err)
	}
	if err := client.Publish(ctx, "device-one", marshal(t, docFor(k, "device-one", t0.Add(-time.Minute)))); statusOf(err) != http.StatusConflict {
		t.Fatalf("older snapshot: %v", err)
	}
	if err := client.Remove(ctx, "device-two"); err != nil {
		t.Fatal(err)
	}
	devices, bad, err := client.Pull(ctx)
	if err != nil || bad != 0 || len(devices) != 1 || devices[0].Doc.Device != "device-one" {
		t.Fatalf("Pull = %d devices, %d bad, %v", len(devices), bad, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cmd := range f.seen {
		if strings.EqualFold(cmd[0], "EXPIRE") && strings.HasPrefix(cmd[1], "aiu:rl:") && (len(cmd) != 4 || cmd[3] != "NX") {
			t.Fatalf("rate counter EXPIRE without NX: %v", cmd)
		}
	}
}

func TestStoreFromEnv(t *testing.T) {
	vars := []string{"KV_REST_API_URL", "KV_REST_API_TOKEN"}
	cases := []struct {
		name     string
		env      map[string]string
		kind     string
		url, tok string
	}{
		{"nothing set", nil, "memory", "", ""},
		{"Vercel KV", map[string]string{"KV_REST_API_URL": "https://kv.example/", "KV_REST_API_TOKEN": "kv"}, "kv", "https://kv.example", "kv"},
		{"URL without token", map[string]string{"KV_REST_API_URL": "https://kv.example"}, "memory", "", ""},
		{"token without URL", map[string]string{"KV_REST_API_TOKEN": "kv"}, "memory", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, v := range vars {
				t.Setenv(v, c.env[v])
			}
			store, kind := StoreFromEnv()
			if kind != c.kind {
				t.Fatalf("kind = %s, want %s", kind, c.kind)
			}
			switch s := store.(type) {
			case *Memory:
				if c.kind != "memory" {
					t.Fatalf("store is %T", store)
				}
			case *KV:
				if s.URL != c.url || s.Token != c.tok || s.HTTP == nil || s.HTTP.Timeout == 0 {
					t.Fatalf("KV = %+v", s)
				}
			default:
				t.Fatalf("store is %T", store)
			}
		})
	}
}
