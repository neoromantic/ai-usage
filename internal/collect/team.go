package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
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
	// Behind is, by device id, since when each device has run an older
	// release than the team's newest, as the reads found it, and that
	// release. Each read carries it over, and starts a device again once it
	// runs another, or after it has not reported for a day.
	Behind map[string]Behind `json:"behind,omitempty"`
}

// Behind is a device's release older than the team's newest, and its first
// run on it that a read found, made since the read before.
type Behind struct {
	Version string    `json:"version"`
	Since   time.Time `json:"since"`
}

// SilentAfter is how long a device goes without reporting before the views
// call it silent. Such a device cannot update itself.
const SilentAfter = 24 * time.Hour

// LoadTeamCache reads the cache. Documents are decoded again; they were
// verified when they were pulled.
func LoadTeamCache(dir state.Dir) (TeamCache, error) {
	var c TeamCache
	b, err := os.ReadFile(dir.TeamCacheFile())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return TeamCache{}, err
	}
	for _, body := range c.Bodies {
		if doc, err := snapshot.Decode(body); err == nil {
			c.Docs = append(c.Docs, doc)
		}
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
	return fsutil.WriteFile(dir.TeamCacheFile(), b, 0o600)
}

// LoadKey reads the team key, generating one on the first run.
func LoadKey(dir state.Dir) (*team.Key, error) {
	k, err := team.Load(dir.KeyFile())
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("team key: %w", err)
	}
	k, err = team.Generate()
	if err != nil {
		return nil, err
	}
	if err := fsutil.WriteFile(dir.KeyFile(), []byte(k.Export()+"\n"), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// publish publishes res.Doc and reads the team back into res.Team, as
// syncTeam does, when there is a relay. It returns the relay's error when
// this run had one.
func (s *sampler) publish(ctx context.Context, key *team.Key, device string, res *Result) string {
	if s.Relay == nil {
		return ""
	}
	// Publish with the key this run sealed and signed the snapshot for.
	client := *s.Relay
	client.Key = key
	s.Relay = &client
	st := s.st
	last := st.Relay
	syncTeam(ctx, s.Options, st, device, &res.Doc, &res.Team, s.now)
	if ctx.Err() != nil && st.Relay.LastErrorAt.Equal(s.now) && !st.Relay.LastPullAt.Equal(s.now) {
		// A stop that cuts the exchange short before its read is no
		// failure of the relay's. A snapshot it did not push stays
		// pending, with the last run's error; once the snapshot is
		// pushed, the cached read's error stands, as when a run skips
		// the read.
		st.Relay.LastError, st.Relay.LastErrorAt = last.LastError, last.LastErrorAt
		if st.Relay.LastPushAt.Equal(s.now) {
			keepReadError(st, res.Team)
		}
	}
	if st.Relay.LastError != "" && st.Relay.LastErrorAt.Equal(s.now) {
		return st.Relay.LastError
	}
	return ""
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
		keepReadError(st, *cache)
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
	prev, after := cache.Behind, cache.PulledAt
	if cache.Team != next.Team {
		// With no read of this team before, no snapshot is known to be new.
		prev, after = nil, now
	}
	next.Behind = behindSince(prev, seen, st.Update.Latest, after, now)
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

// keepReadError makes the relay's error the one the cached read had, if any,
// as of that read.
func keepReadError(st *state.State, c TeamCache) {
	st.Relay.LastError = c.ReadError
	if c.ReadError != "" {
		st.Relay.LastErrorAt = c.PulledAt
	}
}

// behindSince is, for each device in docs on an older release than the
// newest any of them runs or checked is, what prev says of it while it runs
// the release prev names; else that release, since the device's snapshot
// when that is a run made after the read before, at after. A device that
// did not run since, such as a laptop asleep when a release came out, starts
// with its next run, so the time it did not run does not count as time it
// did not update. One that has not reported for a day is left out, and
// starts again when it reports.
func behindSince(prev map[string]Behind, docs []snapshot.Doc, checked string, after, now time.Time) map[string]Behind {
	versions := []string{checked}
	for _, d := range docs {
		versions = append(versions, d.CollectorVersion)
	}
	latest := selfupdate.Newest(versions...)
	var out map[string]Behind
	for _, d := range docs {
		if !selfupdate.Newer(latest, d.CollectorVersion) || now.Sub(d.CollectedAt) > SilentAfter {
			continue
		}
		b, ok := prev[d.Device]
		if !ok || b.Version != d.CollectorVersion {
			if !d.CollectedAt.After(after) {
				continue
			}
			b = Behind{Version: d.CollectorVersion, Since: d.CollectedAt}
		}
		if out == nil {
			out = map[string]Behind{}
		}
		out[d.Device] = b
	}
	return out
}
