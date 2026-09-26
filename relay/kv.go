package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

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
