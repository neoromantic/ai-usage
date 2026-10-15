package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/meter"
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
	// Known is, by device id, the release each other device of the team
	// last ran, as the reads found it. It outlives the device's document
	// on the relay, so that a laptop asleep until its document expired
	// still counts as on its release when it comes back. See knownAfter.
	Known map[string]Known `json:"known,omitempty"`
	// History is, by HistoryKey, the quota readings of each account the
	// reads found over the last historyKept, oldest first, one point per
	// window and reading, with what the team had burned in the window by
	// then. It outlives the readings the documents carry, so that a
	// forecast can follow the pace of the last hours.
	History map[string][]Point `json:"history,omitempty"`
	// Seen is, by device id, the distinct collected_at of the device's
	// documents the reads found over the last historyKept, oldest first: when
	// a device reported, between the reads.
	Seen map[string][]time.Time `json:"seen,omitempty"`
	// Reads is when this device read the team over the last historyKept,
	// oldest first: how much of the time between two reports of a device
	// the reads saw.
	Reads []time.Time `json:"reads,omitempty"`
}

// Point is one window of an account's quota reading, as a team read found
// it.
type Point struct {
	At       time.Time  `json:"at"`
	Window   string     `json:"window"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Percent  float64    `json:"percent"`
	// TeamBurn is what the team had burned on the account since the window
	// began, as the documents in the read said: the devices holding it, with
	// a reading of it or none, and the Hermes accounts that bill to it.
	TeamBurn int64 `json:"team_burn"`
}

// How long, and how many, points and report times the team cache keeps.
const (
	historyKept   = 48 * time.Hour
	maxHistory    = 400
	maxSeenPerDev = 300
)

// HistoryKey keys an account in TeamCache.History: one person on a
// provider, however the devices spell the label.
func HistoryKey(provider, label string) string {
	return state.Key(provider, snapshot.LabelKey(label))
}

// Known is the release a device ran when a read last found it.
type Known struct {
	Version string `json:"version"`
	// First is the first read that found the device, and Last its newest
	// snapshot's collected_at.
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
	// Since is when the relay first stored the device, when the relay
	// said so.
	Since time.Time `json:"since,omitzero"`
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
// read, and forgets the release it ran. The caller holds the run lock.
func ForgetCachedDevice(dir state.Dir, device string) error {
	c, err := LoadTeamCache(dir)
	if err != nil {
		return err
	}
	kept := c.Bodies[:0]
	for _, body := range c.Bodies {
		if doc, err := snapshot.Decode(body); err == nil && doc.Device == device {
			continue
		}
		kept = append(kept, body)
	}
	_, known := c.Known[device]
	_, seen := c.Seen[device]
	if len(kept) == len(c.Bodies) && !known && !seen {
		return nil
	}
	c.Bodies = kept
	delete(c.Known, device)
	delete(c.Seen, device)
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

// rejectedFor is how long the fields ExtSince added, the cycles
// CyclesSince added, or the hours HoursSince added, go unsent after the
// relay refused them.
const rejectedFor = 24 * time.Hour

// syncTeam publishes this device and reads the team back. The snapshot holds
// running totals, so publishing the newest one also covers any runs that
// could not reach the relay. It has the hours HoursSince added, the cycles
// CyclesSince added, and the fields ExtSince added, each only while its gate
// is open, and goes without them for rejectedFor when the relay refuses
// them: a relay that refuses the hours is sent the rest.
func syncTeam(ctx context.Context, o Options, st *state.State, device string, doc *snapshot.Doc, cache *TeamCache, now time.Time) {
	fail := func(err error) {
		st.Relay.LastError = shortErr(err)
		st.Relay.LastErrorAt = now
	}
	push := func(d snapshot.Doc) error {
		body, err := json.Marshal(d)
		if err != nil {
			return err
		}
		pushCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return o.Relay.Publish(pushCtx, device, body)
	}
	// Each body the gates let through, the fullest first, and when the
	// relay refused what it adds.
	type try struct {
		doc     snapshot.Doc
		refused *time.Time
	}
	var tries []try
	fp := o.Relay.Key.Fingerprint()
	if doc.HasHours() && HoursWire(*cache, fp, device, NoHoursUntil(st.Relay), now).Open {
		tries = append(tries, try{*doc, &st.Relay.NoHoursUntil})
	}
	if without := doc.WithoutHours(); without.HasCycles() && CyclesWire(*cache, fp, device, NoCyclesUntil(st.Relay), now).Open {
		tries = append(tries, try{without, &st.Relay.NoCyclesUntil})
	}
	if without := doc.WithoutCycles(); without.Extended() && ExtendedWire(*cache, fp, device, st.Relay.NoExtUntil, now).Open {
		tries = append(tries, try{without, &st.Relay.NoExtUntil})
	}
	tries = append(tries, try{doc.Legacy(), nil})
	var err error
	for _, t := range tries {
		if err = push(t.doc); t.refused == nil || !errors.Is(err, relay.ErrRejected) {
			break
		}
		// A relay older than this release: the team reads the rest.
		*t.refused = now.Add(rejectedFor)
	}
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
	pulled, err := o.Relay.Pull(pullCtx)
	cancel()
	if err != nil {
		fail(err)
		return
	}
	st.Relay.LastPullAt = now
	next := TeamCache{PulledAt: now, Team: o.Relay.Key.Fingerprint()}
	// This device is as this run built it, whatever the relay holds for it.
	seen := []snapshot.Doc{*doc}
	since := map[string]time.Time{}
	for _, d := range pulled.Devices {
		since[d.Doc.Device] = d.Since
		next.Bodies = append(next.Bodies, d.Body)
		next.Docs = append(next.Docs, d.Doc)
		if d.Doc.Device != device {
			seen = append(seen, d.Doc)
		}
	}
	prev, known, after := cache.Behind, cache.Known, cache.PulledAt
	if cache.Team != next.Team {
		// With no read of this team before, no snapshot is known to be new.
		prev, known, after = nil, nil, now
	}
	next.Behind = behindSince(prev, seen, st.Update.Latest, after, now)
	next.Known = knownAfter(known, next.Docs, since, device, now)
	if cache.Team == next.Team {
		next.History, next.Seen, next.Reads = cache.History, cache.Seen, cache.Reads
	}
	next.History = historyAfter(next.History, seen, o.Relay.Key, now)
	next.Seen = seenAfter(next.Seen, seen, now)
	next.Reads = timesAfter(next.Reads, now, now)
	next.ReadError = readError(pulled)
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

// readError is what was wrong with a team read, or "" when nothing was.
func readError(p relay.PullResult) string {
	var errs []string
	switch {
	case p.Undecodable == 1:
		errs = append(errs, "1 device sends a newer snapshot format: update ai-usage")
	case p.Undecodable > 1:
		errs = append(errs, fmt.Sprintf("%d devices send a newer snapshot format: update ai-usage", p.Undecodable))
	}
	if p.Unverified > 0 {
		errs = append(errs, "relay returned documents that do not verify with the team key")
	}
	return strings.Join(errs, "; ")
}

// keepReadError makes the relay's error the one the cached read had, if any,
// as of that read.
func keepReadError(st *state.State, c TeamCache) {
	st.Relay.LastError = c.ReadError
	if c.ReadError != "" {
		st.Relay.LastErrorAt = c.PulledAt
	}
}

// knownAfter is prev after a read that found docs, leaving out self. A
// device the read found takes its release, its newest collected_at and,
// from since, when the relay first stored it. One it did not find was
// either forgotten with `ai-usage team forget-device` or went quiet until
// the relay let its document expire. The relay keeps a document as long as
// its device has been writing, within bounds, from when it first stored it,
// or from First, no earlier, when the relay does not say: a device gone
// before its Last plus that long was forgotten and is dropped. One gone
// later expired, and is kept until the longest a document is kept after its
// Last: when it comes back, it may still run that release.
func knownAfter(prev map[string]Known, docs []snapshot.Doc, since map[string]time.Time, self string, now time.Time) map[string]Known {
	out := map[string]Known{}
	for _, d := range docs {
		if d.Device == self {
			continue
		}
		k, ok := prev[d.Device]
		if !ok {
			k.First = now
		}
		k.Version = d.CollectorVersion
		if s := since[d.Device]; !s.IsZero() {
			k.Since = s.UTC()
		}
		if d.CollectedAt.After(k.Last) {
			k.Last = d.CollectedAt
		}
		out[d.Device] = k
	}
	// How long the relay keeps a document after its last write: as long as
	// the device has been writing, within these bounds.
	kept := relay.DefaultLimits()
	for id, k := range prev {
		if _, found := out[id]; found || id == self {
			continue
		}
		from := k.First
		if !k.Since.IsZero() {
			from = k.Since
		}
		ttl := min(max(k.Last.Sub(from), kept.MinRecordTTL), kept.RecordTTL)
		if now.Before(k.Last.Add(ttl)) || !now.Before(k.Last.Add(kept.RecordTTL)) {
			continue
		}
		out[id] = k
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// historyAfter is prev with the newest quota reading that docs hold of each
// account: a point per window of it, unless the window has a point as new.
// Each point has what the team burned on the account in the window's
// current period, as the documents say: see burners. Points older than
// historyKept are left out, and the oldest beyond maxHistory an account.
func historyAfter(prev map[string][]Point, docs []snapshot.Doc, key *team.Key, now time.Time) map[string][]Point {
	readings := map[string][]snapshot.Account{}
	burned := map[string][]burner{}
	for _, d := range docs {
		for k, bs := range burners(d, key) {
			burned[k] = append(burned[k], bs...)
		}
		for _, a := range d.Accounts {
			if label, err := key.Open(a.Label); err == nil && a.QuotaAt != nil && len(a.Windows) > 0 {
				k := HistoryKey(a.Provider, label)
				readings[k] = append(readings[k], a)
			}
		}
	}
	out := map[string][]Point{}
	for k, as := range readings {
		newest := as[0]
		for _, a := range as[1:] {
			if a.QuotaAt.After(*newest.QuotaAt) {
				newest = a
			}
		}
		for _, w := range newest.Windows {
			// The points are oldest first.
			var last time.Time
			for _, p := range prev[k] {
				if p.Window == w.Name {
					last = p.At
				}
			}
			if !newest.QuotaAt.After(last) {
				continue
			}
			p := Point{At: newest.QuotaAt.UTC(), Window: w.Name, ResetsAt: w.ResetsAt, Percent: w.Percent}
			if start, ok := w.Start(); ok {
				for _, b := range burned[k] {
					p.TeamBurn += b.since(w.Name, start)
				}
			}
			out[k] = append(out[k], p)
		}
	}
	cut := now.Add(-historyKept)
	for k, ps := range prev {
		out[k] = append(slices.Clone(ps), out[k]...)
	}
	for k, ps := range out {
		ps = slices.DeleteFunc(ps, func(p Point) bool { return p.At.Before(cut) })
		slices.SortStableFunc(ps, func(a, b Point) int { return a.At.Compare(b.At) })
		if len(ps) > maxHistory {
			ps = ps[len(ps)-maxHistory:]
		}
		if len(ps) == 0 {
			delete(out, k)
			continue
		}
		out[k] = ps
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// burner is what one account on one device doc burned, or the part of it
// that billed to another account: its burn by UTC day, newest first, and
// since each window of its reading began, as of collected.
type burner struct {
	days      []int64
	recent    []snapshot.Recent
	burn      map[string]int64
	collected time.Time
	// part is the share of it that counts, out of one.
	part float64
}

// since is what b burned since start, the beginning of window: what the
// device counted itself when its own reading of the window began then, else
// its days from start's day on, which may count some of that day too many,
// the same in every reading.
func (b burner) since(window string, start time.Time) int64 {
	n := DaysSince(b.days, start, b.collected)
	for _, r := range b.recent {
		// A device's own reading may be of the window's last period.
		if r.Window == window && r.Start.Sub(start).Abs() < time.Hour {
			n = b.burn[window]
		}
	}
	return int64(math.Round(float64(n) * b.part))
}

// burners is, by HistoryKey, what each account of a device doc burned on
// it: the account itself, whether or not the device has a reading of it,
// and the Hermes accounts that billed to it. A Hermes account is shared
// among the logins its account's Linked names, by what it burned through
// each, else it bills whole to the account whose reading it borrowed.
func burners(d snapshot.Doc, key *team.Key) map[string][]burner {
	labels := make([]string, len(d.Accounts))
	opened := make([]bool, len(d.Accounts))
	for i, a := range d.Accounts {
		l, err := key.Open(a.Label)
		labels[i], opened[i] = l, err == nil
	}
	type login struct {
		to   string
		burn int64
	}
	through := map[string][]login{}
	for i, a := range d.Accounts {
		for _, u := range a.Linked {
			l, err := key.Open(u.Label)
			if err != nil || !opened[i] {
				continue
			}
			k := HistoryKey(u.Provider, l)
			through[k] = append(through[k], login{HistoryKey(a.Provider, labels[i]), meter.TokensBurn(u.Tokens, u.Provider, l)})
		}
	}
	out := map[string][]burner{}
	for i, a := range d.Accounts {
		if !opened[i] {
			continue
		}
		days, burn, _, _ := AccountBurn(a, a.Provider, labels[i], d.CollectedAt)
		b := burner{days: days, recent: a.Recent, burn: burn, collected: d.CollectedAt, part: 1}
		k := HistoryKey(a.Provider, labels[i])
		out[k] = append(out[k], b)
		logins := through[k]
		var all int64
		for _, l := range logins {
			all += max(l.burn, 0)
		}
		if all <= 0 {
			if to := borrowedFrom(d.Accounts, labels, opened, i); to != "" {
				out[to] = append(out[to], b)
			}
			continue
		}
		for _, l := range logins {
			if l.burn > 0 {
				part := b
				part.part = float64(l.burn) / float64(all)
				out[l.to] = append(out[l.to], part)
			}
		}
	}
	return out
}

// borrowedFrom is the HistoryKey of the one account of a device doc whose
// reading the i-th account borrowed, or "" when there is none, or several.
func borrowedFrom(accts []snapshot.Account, labels []string, opened []bool, i int) string {
	a := accts[i]
	if a.QuotaFrom == "" {
		return ""
	}
	var to string
	for j, b := range accts {
		if j == i || !opened[j] || b.Provider != a.QuotaFrom || !snapshot.SameReading(a, b) {
			continue
		}
		if to != "" {
			return ""
		}
		to = HistoryKey(b.Provider, labels[j])
	}
	return to
}

// DaysSince is the sum of days, a device's tokens by UTC day with index 0
// the day of collected, from start's day on.
func DaysSince(days []int64, start, collected time.Time) int64 {
	if collected.Before(start) {
		return 0
	}
	var n int64
	for i := 0; i <= int(dayNumber(collected)-dayNumber(start)) && i < len(days); i++ {
		n += days[i]
	}
	return n
}

// seenAfter is prev with when each of docs was collected, over the last
// historyKept and at most maxSeenPerDev times a device.
func seenAfter(prev map[string][]time.Time, docs []snapshot.Doc, now time.Time) map[string][]time.Time {
	out := map[string][]time.Time{}
	for id, ts := range prev {
		out[id] = slices.Clone(ts)
	}
	for _, d := range docs {
		out[d.Device] = append(out[d.Device], d.CollectedAt.UTC())
	}
	for id, ts := range out {
		if ts = timesAfter(ts, time.Time{}, now); len(ts) == 0 {
			delete(out, id)
			continue
		}
		out[id] = ts
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// timesAfter is ts with t, unless t is zero, sorted and without repeats,
// over the last historyKept of now and at most maxSeenPerDev of them.
func timesAfter(ts []time.Time, t, now time.Time) []time.Time {
	ts = slices.Clone(ts)
	if !t.IsZero() {
		ts = append(ts, t.UTC())
	}
	cut := now.Add(-historyKept)
	ts = slices.DeleteFunc(ts, func(t time.Time) bool { return t.Before(cut) })
	slices.SortFunc(ts, time.Time.Compare)
	ts = slices.CompactFunc(ts, time.Time.Equal)
	if len(ts) > maxSeenPerDev {
		ts = ts[len(ts)-maxSeenPerDev:]
	}
	if len(ts) == 0 {
		return nil
	}
	return ts
}
