package relay

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Record is one stored snapshot: the exact bytes that were signed, and the
// signature. Since is when the relay first stored the device, which sets how
// long the record is kept.
type Record struct {
	Body  []byte    `json:"body"`
	Sig   []byte    `json:"sig"`
	Since time.Time `json:"since,omitzero"`
}

// Store is a key-value store that overwrites one document per device.
type Store interface {
	Get(ctx context.Context, teamFP, device string) (*Record, error)
	Put(ctx context.Context, teamFP, device string, rec Record, ttl time.Duration) error
	Delete(ctx context.Context, teamFP, device string) error
	// List returns the team's live documents keyed by device.
	List(ctx context.Context, teamFP string) (map[string]Record, error)
	// Size is about how many devices the team has, cheaply. It may count
	// documents that expired and that List has not dropped yet.
	Size(ctx context.Context, teamFP string) (int, error)
	// Count increments a counter that expires after window and returns it.
	Count(ctx context.Context, key string, window time.Duration) (int64, error)
}

// StoreFromEnv picks Vercel KV when the variables its Storage tab sets are
// present, and memory otherwise.
func StoreFromEnv() (Store, string) {
	url := os.Getenv("KV_REST_API_URL")
	token := os.Getenv("KV_REST_API_TOKEN")
	if url != "" && token != "" {
		return &KV{URL: strings.TrimRight(url, "/"), Token: token, HTTP: &http.Client{Timeout: 10 * time.Second}}, "kv"
	}
	return NewMemory(), "memory"
}

// Memory is a store for tests and a single self-hosted process.
type Memory struct {
	mu    sync.Mutex
	docs  map[string]map[string]memItem
	count map[string]memCount
	now   func() time.Time
	hits  int
	swept time.Time
}

// sweepEvery is how many Count calls pass between sweeps of expired entries.
const sweepEvery = 1024

type memItem struct {
	rec     Record
	expires time.Time
}

type memCount struct {
	n       int64
	expires time.Time
}

func NewMemory() *Memory {
	return &Memory{docs: map[string]map[string]memItem{}, count: map[string]memCount{}, now: time.Now}
}

func (m *Memory) Get(_ context.Context, teamFP, device string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.docs[teamFP][device]
	if !ok || m.now().After(it.expires) {
		return nil, nil
	}
	rec := it.rec
	return &rec, nil
}

func (m *Memory) Put(_ context.Context, teamFP, device string, rec Record, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.docs[teamFP] == nil {
		m.docs[teamFP] = map[string]memItem{}
	}
	m.docs[teamFP][device] = memItem{rec: rec, expires: m.now().Add(ttl)}
	return nil
}

func (m *Memory) Delete(_ context.Context, teamFP, device string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.docs[teamFP], device)
	if len(m.docs[teamFP]) == 0 {
		delete(m.docs, teamFP)
	}
	return nil
}

func (m *Memory) List(_ context.Context, teamFP string) (map[string]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Record{}
	for dev, it := range m.docs[teamFP] {
		if m.now().After(it.expires) {
			delete(m.docs[teamFP], dev)
			continue
		}
		out[dev] = it.rec
	}
	return out, nil
}

func (m *Memory) Size(_ context.Context, teamFP string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.docs[teamFP]), nil
}

func (m *Memory) Count(_ context.Context, key string, window time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.hits++; m.hits%sweepEvery == 0 {
		m.sweep(now)
	}
	// Hourly too, so a quiet relay keeps no address past its window for long.
	if now.Sub(m.swept) > time.Hour {
		m.sweep(now)
	}
	c := m.count[key]
	if now.After(c.expires) {
		c = memCount{expires: now.Add(window)}
	}
	c.n++
	m.count[key] = c
	return c.n, nil
}

// sweep drops expired counters and documents. Every window brings new counter
// keys and abandoned teams are never listed again, so a long-running process
// would otherwise keep both forever.
func (m *Memory) sweep(now time.Time) {
	m.swept = now
	for k, c := range m.count {
		if now.After(c.expires) {
			delete(m.count, k)
		}
	}
	for teamFP, devs := range m.docs {
		for dev, it := range devs {
			if now.After(it.expires) {
				delete(devs, dev)
			}
		}
		if len(devs) == 0 {
			delete(m.docs, teamFP)
		}
	}
}
