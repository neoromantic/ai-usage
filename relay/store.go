package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
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

// KV is Vercel KV, spoken over its REST API: one POST of commands per call.
// HTTP is required.
//
// Keys:
//
//	aiu:t:{team}:d:{device}  one snapshot record, expires after the TTL
//	aiu:t:{team}:devices     set of device ids, pruned when a record is gone
//	aiu:rl:{key}             rate-limit counters
type KV struct {
	URL   string
	Token string
	HTTP  *http.Client
}

func docKey(teamFP, device string) string { return "aiu:t:" + teamFP + ":d:" + device }
func setKey(teamFP string) string         { return "aiu:t:" + teamFP + ":devices" }

func (kv *KV) Get(ctx context.Context, teamFP, device string) (*Record, error) {
	res, err := kv.pipeline(ctx, []any{"GET", docKey(teamFP, device)})
	if err != nil {
		return nil, err
	}
	return decodeRecord(res[0])
}

func (kv *KV) Put(ctx context.Context, teamFP, device string, rec Record, ttl time.Duration) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	secs := strconv.Itoa(int(ttl.Seconds()))
	// The set lasts as long as its longest-lived record: a device's write
	// never shortens it, or a team's newest device, writing last, would
	// unlist the older ones before their records expire.
	_, err = kv.pipeline(ctx,
		[]any{"SET", docKey(teamFP, device), string(b), "EX", secs},
		[]any{"SADD", setKey(teamFP), device},
		[]any{"EXPIRE", setKey(teamFP), secs, "NX"},
		[]any{"EXPIRE", setKey(teamFP), secs, "GT"},
	)
	return err
}

func (kv *KV) Size(ctx context.Context, teamFP string) (int, error) {
	res, err := kv.pipeline(ctx, []any{"SCARD", setKey(teamFP)})
	if err != nil {
		return 0, err
	}
	var n int
	if err := json.Unmarshal(res[0], &n); err != nil {
		return 0, fmt.Errorf("kv: SCARD: %w", err)
	}
	return n, nil
}

func (kv *KV) Delete(ctx context.Context, teamFP, device string) error {
	_, err := kv.pipeline(ctx,
		[]any{"DEL", docKey(teamFP, device)},
		[]any{"SREM", setKey(teamFP), device},
	)
	return err
}

func (kv *KV) List(ctx context.Context, teamFP string) (map[string]Record, error) {
	res, err := kv.pipeline(ctx, []any{"SMEMBERS", setKey(teamFP)})
	if err != nil {
		return nil, err
	}
	var devices []string
	if err := json.Unmarshal(res[0], &devices); err != nil {
		return nil, fmt.Errorf("kv: SMEMBERS: %w", err)
	}
	out := map[string]Record{}
	if len(devices) == 0 {
		return out, nil
	}
	cmd := []any{"MGET"}
	for _, d := range devices {
		cmd = append(cmd, docKey(teamFP, d))
	}
	res, err = kv.pipeline(ctx, cmd)
	if err != nil {
		return nil, err
	}
	var values []*string
	if err := json.Unmarshal(res[0], &values); err != nil {
		return nil, fmt.Errorf("kv: MGET: %w", err)
	}
	var gone []any
	for i, v := range values {
		if i >= len(devices) {
			break
		}
		if v == nil {
			gone = append(gone, devices[i])
			continue
		}
		var rec Record
		if json.Unmarshal([]byte(*v), &rec) == nil {
			out[devices[i]] = rec
		}
	}
	if len(gone) > 0 {
		_, _ = kv.pipeline(ctx, append([]any{"SREM", setKey(teamFP)}, gone...))
	}
	return out, nil
}

func (kv *KV) Count(ctx context.Context, key string, window time.Duration) (int64, error) {
	k := "aiu:rl:" + key
	res, err := kv.pipeline(ctx,
		[]any{"INCR", k},
		[]any{"EXPIRE", k, strconv.Itoa(int(window.Seconds())), "NX"},
	)
	if err != nil {
		return 0, err
	}
	var n int64
	if err := json.Unmarshal(res[0], &n); err != nil {
		return 0, fmt.Errorf("kv: INCR: %w", err)
	}
	return n, nil
}

func decodeRecord(raw json.RawMessage) (*Record, error) {
	var s *string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("kv: GET: %w", err)
	}
	if s == nil {
		return nil, nil
	}
	var rec Record
	if err := json.Unmarshal([]byte(*s), &rec); err != nil {
		return nil, fmt.Errorf("kv: stored record: %w", err)
	}
	return &rec, nil
}

// maxKVResponse bounds what one pipeline call reads back. The largest is the
// team read's MGET: every record in one response, up to about 88 KB each for a
// full 64 KB snapshot, so 4.4 MB at DefaultLimits' 50 devices. 6 MiB also
// fits the few devices that racing first writes can add past that cap.
const maxKVResponse = 6 << 20

// pipeline runs commands and returns each result, failing on any command error.
func (kv *KV) pipeline(ctx context.Context, cmds ...[]any) ([]json.RawMessage, error) {
	body, err := json.Marshal(cmds)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kv.URL+"/pipeline", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+kv.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := kv.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kv: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxKVResponse))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kv: HTTP %d", resp.StatusCode)
	}
	var results []struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("kv: %w", err)
	}
	if len(results) != len(cmds) {
		return nil, errors.New("kv: result count does not match")
	}
	out := make([]json.RawMessage, len(results))
	for i, r := range results {
		if r.Error != "" {
			return nil, fmt.Errorf("kv: %s", r.Error)
		}
		out[i] = r.Result
	}
	return out, nil
}
