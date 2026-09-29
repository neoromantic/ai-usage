package collect

import (
	"maps"
	"math/bits"
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// placeHours adds the hours a session's growth this run went to, before the
// ledger adds the growth: to the session's hours and, once more than one
// account spends in it, to the hours of the account each part grew for. A
// log that records times has it in the hours where the log shows more than
// the ledger placed, kind by kind, in proportion. Taking the log's hours as
// they are would count some tokens twice: a partial read misses some, and
// the tokens a log records no time for are spread anew as the session grows.
// A log without times has it at the session's last activity, which is within
// the last run's interval. So each kind of a session's hours, and of each
// account's, adds up to their tokens in the hours the ledger keeps.
func placeHours(e *state.Session, s logs.Session, grown map[string]snapshot.Tokens, updated, now time.Time) {
	var had, spent meter.Use
	for _, t := range e.By {
		had = had.Add(meter.FromTokens(t))
	}
	for _, g := range grown {
		spent = spent.Add(meter.FromTokens(g))
	}
	first := firstKept(now.Add(-state.Retention))
	if len(s.Hours) > 0 && (len(e.U) == 0 || e.Rederive) {
		// A session read for the first time, or migrated from a ledger that
		// kept only input plus output by hour: the log's times cover its
		// whole history, which its accounts share as they share its tokens.
		// A migrated session's accounts keep what they had by hour.
		total := had.Add(spent)
		total.CacheWrite1h = s.Hours.Sum().CacheWrite1h
		u := s.Hours.Clone()
		u.ScaleTo(total, logs.HourOf(updated))
		var by map[string]meter.Hours
		if e.Rederive && e.UB != nil {
			by = splitHours(u, e.UB, grownBy(e.By, grown), logs.HourOf(updated))
			u = nil
			for l, h := range by {
				u = meter.AddHours(u, h)
				by[l] = keptHours(h, first)
			}
		}
		e.U, e.UB, e.Rederive = u.Since(first), by, false
		return
	}
	if spent.Zero() {
		return
	}
	many := spenders(e, grown) > 1
	if e.UB == nil && (many || len(e.U) == 0) {
		// Each account's hours start where the report put them. A session
		// kept from before the ledger recorded hours, whose log records no
		// times, has each account's share at the hour the share last grew.
		by := accountHours(e)
		e.U = nil
		for _, h := range by {
			e.U = meter.AddHours(e.U, h)
		}
		if many {
			e.UB = by
		}
	}
	add := grownHours(e.U, s.Hours, spent, updated, first)
	long := splitCount(add.Sum().CacheWrite1h, func(l string) int64 { return grown[l].CacheWrite }, slices.Collect(maps.Keys(grown)))
	for l, g := range grown {
		target := meter.FromTokens(g)
		target.CacheWrite1h = long[l]
		if target.Zero() {
			continue
		}
		part := add.Clone()
		part.ScaleTo(target, logs.HourOf(updated))
		e.U = meter.AddHours(e.U, part)
		if e.UB != nil {
			e.UB[l] = meter.AddHours(e.UB[l], part)
		}
	}
}

// grownBy is each account's tokens once this run's growth is added.
func grownBy(by, grown map[string]snapshot.Tokens) map[string]meter.Use {
	out := map[string]meter.Use{}
	for l, t := range by {
		out[l] = meter.FromTokens(t)
	}
	for l, g := range grown {
		out[l] = out[l].Add(meter.FromTokens(g))
	}
	return out
}

// splitHours splits a migrated session's new hours between its accounts,
// each keeping exactly its tokens. Each kind of an hour is shared as the
// accounts' old hours shared it there, or, where they had none of it, as the
// accounts share the session's tokens. Hour-long cache writes are shared as
// cache writes are.
func splitHours(u meter.Hours, oldBy map[string]meter.Hours, by map[string]meter.Use, fallback int64) map[string]meter.Hours {
	labels := slices.Sorted(maps.Keys(by))
	input := func(x meter.Use) int64 { return x.Input }
	output := func(x meter.Use) int64 { return x.Output }
	read := func(x meter.Use) int64 { return x.CacheRead }
	write := func(x meter.Use) int64 { return x.CacheWrite }
	long := func(x meter.Use) int64 { return x.CacheWrite1h }
	// Each kind as hours count it, and as the session's tokens count it:
	// these do not say which cache writes are kept for an hour.
	inHours := [...]func(meter.Use) int64{input, output, read, write, long}
	inTokens := [...]func(meter.Use) int64{input, output, read, write, write}
	var all meter.Use
	for _, u := range by {
		all = all.Add(u)
	}
	// part is label's part of n, the count of kind k in hour h.
	part := func(n int64, l string, h int64, k int) int64 {
		var had int64
		for _, hours := range oldBy {
			had += inHours[k](hours[h])
		}
		if had > 0 {
			return share(n, inHours[k](oldBy[l][h]), had)
		}
		return share(n, inTokens[k](by[l]), inTokens[k](all))
	}
	longBy := splitCount(u.Sum().CacheWrite1h, func(l string) int64 { return by[l].CacheWrite }, labels)
	out := map[string]meter.Hours{}
	for _, l := range labels {
		target := by[l]
		target.CacheWrite1h = longBy[l]
		if target.Zero() {
			continue
		}
		shape := meter.Hours{}
		for h, x := range u {
			shape[h] = meter.Use{
				Input:        part(x.Input, l, h, 0),
				Output:       part(x.Output, l, h, 1),
				CacheRead:    part(x.CacheRead, l, h, 2),
				CacheWrite:   part(x.CacheWrite, l, h, 3),
				CacheWrite1h: part(x.CacheWrite1h, l, h, 4),
			}
		}
		shape.ScaleTo(target, fallback)
		out[l] = shape
	}
	return out
}

// keptHours is the hours from first on, empty rather than nil: an account
// whose hours all dropped keeps its entry.
func keptHours(h meter.Hours, first int64) meter.Hours {
	if kept := h.Since(first); kept != nil {
		return kept
	}
	return meter.Hours{}
}

// splitCount divides n between labels in proportion to weight, exactly, as
// meter.Spread divides a count between hours. With no weight at all, it is
// split evenly.
func splitCount(n int64, weight func(string) int64, labels []string) map[string]int64 {
	out := map[string]int64{}
	if n <= 0 || len(labels) == 0 {
		return out
	}
	slices.Sort(labels)
	shape := map[int64]int64{}
	var all int64
	for i, l := range labels {
		shape[int64(i)] = max(weight(l), 0)
		all += shape[int64(i)]
	}
	if all == 0 {
		for i := range labels {
			shape[int64(i)] = 1
		}
	}
	for i, u := range meter.Spread(meter.Use{Input: n}, shape) {
		out[labels[i]] = u.Input
	}
	return out
}

// spenders is how many accounts spent tokens in a session, with this run's
// growth.
func spenders(e *state.Session, grown map[string]snapshot.Tokens) int {
	n := 0
	for l, t := range e.By {
		if !t.Add(grown[l]).Zero() {
			n++
		}
	}
	for l, g := range grown {
		if _, ok := e.By[l]; !ok && !g.Zero() {
			n++
		}
	}
	return n
}

// grownHours is where a session's growth went: the hours in which its log
// shows more than the ledger placed, kind by kind, in proportion, leaving
// out those before first, which the ledger no longer keeps. A log without
// times, or one that shows no such hour, has it all at updated. The log's
// hours say how much of the growth's cache writes are kept for an hour.
func grownHours(placed, log meter.Hours, spent meter.Use, updated time.Time, first int64) meter.Hours {
	out := meter.Hours{}
	for h, n := range log {
		if d := n.Sub(placed[h]); h >= first && !d.Zero() {
			out[h] = d
		}
	}
	if len(out) == 0 {
		return meter.Hours{logs.HourOf(updated): spent}
	}
	spent.CacheWrite1h = out.Sum().CacheWrite1h
	out.ScaleTo(spent, logs.HourOf(updated))
	return out
}

// firstKept is the key of the first hour the ledger keeps at cutoff: the
// first that ended at cutoff or later.
func firstKept(cutoff time.Time) int64 {
	h := logs.HourOf(cutoff)
	if !logs.HourStart(h).Before(cutoff) {
		h--
	}
	return h
}

// labelHours is label's part of a session's hours. Once a second account
// spends in a session, each keeps its own; until then they are all the one
// account's. A ledger from before accounts kept their own gives each its
// share of each kind of the session's tokens, spread over the hours as the
// session's are. A session whose hours are not known, as one the ledger kept
// from before it recorded them, has the part at the hour label's share last
// grew.
func labelHours(s *state.Session, label string) meter.Hours {
	if s.UB != nil {
		return s.UB[label]
	}
	own := meter.FromTokens(s.By[label])
	if own.Zero() {
		return nil
	}
	var all meter.Use
	for _, t := range s.By {
		all = all.Add(meter.FromTokens(t))
	}
	at := logs.HourOf(lastActive(s, label))
	if len(s.U) == 0 {
		return meter.Hours{at: own}
	}
	if own == all {
		return s.U
	}
	sum := s.U.Sum()
	out := s.U.Clone()
	out.ScaleTo(meter.Use{
		Input:        share(sum.Input, own.Input, all.Input),
		Output:       share(sum.Output, own.Output, all.Output),
		CacheRead:    share(sum.CacheRead, own.CacheRead, all.CacheRead),
		CacheWrite:   share(sum.CacheWrite, own.CacheWrite, all.CacheWrite),
		CacheWrite1h: share(sum.CacheWrite1h, own.CacheWrite, all.CacheWrite),
	}, at)
	return out
}

// share is n × part / whole, rounded to the nearest, part being at most
// whole. The product is computed in 128 bits, so counts never overflow.
func share(n, part, whole int64) int64 {
	if n <= 0 || part <= 0 || whole <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(n), uint64(min(part, whole)))
	q, r := bits.Div64(hi, lo, uint64(whole))
	if r >= uint64(whole)-r {
		q++
	}
	return int64(q)
}

// accountHours is each account's part of a session's hours, as labelHours
// gives it.
func accountHours(s *state.Session) map[string]meter.Hours {
	out := map[string]meter.Hours{}
	for l := range s.By {
		if h := labelHours(s, l); len(h) > 0 {
			out[l] = h.Clone()
		}
	}
	return out
}

// DaysOf buckets hours by UTC day, newest first: index 0 is now's UTC day.
// It covers the retention window and leaves out trailing zeros.
func DaysOf(hours map[int64]int64, now time.Time) []int64 {
	today := dayNumber(now)
	var out []int64
	for h, n := range hours {
		i := today - dayNumber(logs.HourStart(h))
		if i < 0 || i >= snapshot.MaxDays || n <= 0 {
			continue
		}
		for int64(len(out)) <= i {
			out = append(out, 0)
		}
		out[i] += n
	}
	return out
}

// dayNumber counts UTC days since the Unix epoch.
func dayNumber(t time.Time) int64 { return t.Unix() / 86400 }

// SinceStart is the tokens of hours spent since start. An hour counts when
// most of it is past start.
func SinceStart(hours map[int64]int64, start time.Time) int64 {
	var n int64
	for h, v := range hours {
		if !logs.HourStart(h).Add(30 * time.Minute).Before(start) {
			n += v
		}
	}
	return n
}
