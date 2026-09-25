package collect

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/relay"
)

// TeamCache is the last verified team read, kept so the console can show the
// other devices while the relay is unreachable.
type TeamCache struct {
	PulledAt time.Time `json:"pulled_at"`
	Team     string    `json:"team"`
	// ReadError is what was wrong with the read itself, such as documents
	// that did not verify. It stands until the next read.
	ReadError string         `json:"read_error,omitempty"`
	Bodies    [][]byte       `json:"bodies"`
	Docs      []snapshot.Doc `json:"-"`
	// Behind is, by device id, since when the reads have found each device
	// on an older release than the team's newest, and that release. Each
	// read carries it over, and starts a device again once it runs another.
	Behind map[string]Behind `json:"behind,omitempty"`
}

// Behind is a device's release when a read first found it older than the
// team's newest, and when that was.
type Behind struct {
	Version string    `json:"version"`
	Since   time.Time `json:"since"`
}

const teamCacheFile = "team-cache.json"

// LoadTeamCache reads the cache. Documents are decoded again; they were
// verified when they were pulled. A device listed twice, in a cache written
// before pulls kept one document per device, keeps its newest document, so
// its tokens are not added twice.
func LoadTeamCache(dir state.Dir) (TeamCache, error) {
	var c TeamCache
	b, err := os.ReadFile(dir.Path(teamCacheFile))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return TeamCache{}, err
	}
	at := map[string]int{}
	for _, body := range c.Bodies {
		doc, err := snapshot.Decode(body)
		if err != nil {
			continue
		}
		if i, ok := at[doc.Device]; ok {
			if doc.CollectedAt.After(c.Docs[i].CollectedAt) {
				c.Docs[i] = doc
			}
			continue
		}
		at[doc.Device] = len(c.Docs)
		c.Docs = append(c.Docs, doc)
	}
	return c, nil
}

// ForgetCachedDevice drops a device from the cached team read, so that the
// team views stop listing a device the relay no longer has before the next
// read. The caller holds the run lock.
func ForgetCachedDevice(dir state.Dir, device string) error {
	c, err := LoadTeamCache(dir)
	if err != nil || c.Bodies == nil {
		return err
	}
	kept := c.Bodies[:0]
	for _, body := range c.Bodies {
		if doc, err := snapshot.Decode(body); err == nil && doc.Device == device {
			continue
		}
		kept = append(kept, body)
	}
	if len(kept) == len(c.Bodies) {
		return nil
	}
	c.Bodies = kept
	return saveTeamCache(dir, c)
}

func saveTeamCache(dir state.Dir, c TeamCache) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return state.WriteFile(dir.Path(teamCacheFile), b)
}

// syncTeam publishes this device and reads the team back. The snapshot holds
// running totals, so publishing the newest one also covers any runs that
// could not reach the relay.
func syncTeam(ctx context.Context, o Options, st *state.State, device string, doc *snapshot.Doc, cache *TeamCache, now time.Time) {
	fail := func(err error) {
		st.Relay.LastError = shortErr(err)
		st.Relay.LastErrorAt = now
	}
	body, err := json.Marshal(doc)
	if err != nil {
		fail(err)
		return
	}
	pushCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err = o.Relay.Publish(pushCtx, device, body)
	cancel()
	var status *relay.ErrStatus
	var conflict error
	switch {
	case err == nil:
		st.Relay.LastPushAt = now
		st.Relay.Pending = false
	case errors.As(err, &status) && status.Code == 409:
		// The team keeps seeing an older snapshot than this one, so it is
		// still pending. The team is read anyway.
		st.Relay.Pending = true
		conflict = errors.New("the relay holds a newer snapshot for this device id: this clock went back, or another machine uses the same device id")
	default:
		st.Relay.Pending = true
		fail(err)
		return
	}

	if fresh := now.Sub(cache.PulledAt); o.PullEvery > 0 && cache.Team == o.Relay.Key.Fingerprint() && fresh >= 0 && fresh < o.PullEvery {
		// The write's own error is this run's; the cached read's stands
		// until the next read.
		st.Relay.LastError = cache.ReadError
		if cache.ReadError != "" {
			st.Relay.LastErrorAt = cache.PulledAt
		}
		if conflict != nil {
			fail(conflict)
		}
		return
	}
	pullCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	devices, bad, err := o.Relay.Pull(pullCtx)
	cancel()
	if err != nil {
		fail(err)
		return
	}
	st.Relay.LastPullAt = now
	next := TeamCache{PulledAt: now, Team: o.Relay.Key.Fingerprint()}
	// This device is as this run built it, whatever the relay holds for it.
	seen := []snapshot.Doc{*doc}
	for _, d := range devices {
		next.Bodies = append(next.Bodies, d.Body)
		next.Docs = append(next.Docs, d.Doc)
		if d.Doc.Device != device {
			seen = append(seen, d.Doc)
		}
	}
	prev := cache.Behind
	if cache.Team != next.Team {
		prev = nil
	}
	next.Behind = behindSince(prev, seen, st.Update.Latest, now)
	if bad > 0 {
		next.ReadError = "relay returned documents that do not verify with the team key"
	}
	*cache = next
	if err := saveTeamCache(o.Dir, next); err != nil {
		fail(err)
		return
	}
	if next.ReadError != "" {
		fail(errors.New(next.ReadError))
		return
	}
	if conflict != nil {
		fail(conflict)
		return
	}
	st.Relay.LastError = ""
}

// behindSince is, for each device in docs on an older release than the
// newest any of them runs or checked is, what prev says of it while it runs
// the release prev names; else that release, since now.
func behindSince(prev map[string]Behind, docs []snapshot.Doc, checked string, now time.Time) map[string]Behind {
	versions := []string{checked}
	for _, d := range docs {
		versions = append(versions, d.CollectorVersion)
	}
	latest := selfupdate.Newest(versions...)
	var out map[string]Behind
	for _, d := range docs {
		if !selfupdate.Newer(latest, d.CollectorVersion) {
			continue
		}
		b, ok := prev[d.Device]
		if !ok || b.Version != d.CollectorVersion {
			b = Behind{Version: d.CollectorVersion, Since: now}
		}
		if out == nil {
			out = map[string]Behind{}
		}
		out[d.Device] = b
	}
	return out
}
