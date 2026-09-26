package relay

import (
	"context"
	"strconv"
	"testing"
	"time"
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
