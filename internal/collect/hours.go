package collect

import (
	"maps"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// placeHours adds the hours a session's growth this run went to, before the
// ledger adds the growth: to the session's hours and, once more than one
// account spends in it, to the hours of the account each part grew for. A
// log that records times has it in the hours where the log shows more than
// the ledger placed, in proportion. Taking the log's hours as they are would
// count some tokens twice: a partial read misses some, and the tokens a log
// records no time for are spread anew as the session grows. A log without
// times has it at the session's last activity, which is within the last
// run's interval. So a session's hours, and each account's, add up to their
// input plus output.
func placeHours(e *state.Session, s logs.Session, grown map[string]snapshot.Tokens, updated, now time.Time) {
	var had, spent int64
	for _, t := range e.By {
		had += t.InOut()
	}
	for _, g := range grown {
		spent += g.InOut()
	}
	if len(e.Hours) == 0 && len(s.Hours) > 0 {
		// A session read for the first time, or kept from before the ledger
		// recorded hours: the log's times cover its whole history, which its
		// accounts share as they share its tokens.
		e.Hours = maps.Clone(s.Hours)
		logs.ScaleHours(e.Hours, had+spent)
		e.ByHours = nil
		return
	}
	if spent <= 0 {
		return
	}
	many := spenders(e, grown) > 1
	if e.ByHours == nil && (many || len(e.Hours) == 0) {
		// Each account's hours start where the report put them. A session
		// kept from before the ledger recorded hours, whose log records no
		// times, has each account's share at the hour the share last grew.
		by := accountHours(e)
		e.Hours = nil
		for _, h := range by {
			e.Hours = logs.AddHours(e.Hours, h)
		}
		if many {
			e.ByHours = by
		}
	}
	add := grownHours(e.Hours, s.Hours, spent, updated, now)
	for l, g := range grown {
		n := g.InOut()
		if n <= 0 {
			continue
		}
		part := maps.Clone(add)
		logs.ScaleHours(part, n)
		e.Hours = logs.AddHours(e.Hours, part)
		if e.ByHours != nil {
			e.ByHours[l] = logs.AddHours(e.ByHours[l], part)
		}
	}
}

// spenders is how many accounts spent input or output tokens in a session,
// with this run's growth.
func spenders(e *state.Session, grown map[string]snapshot.Tokens) int {
	n := 0
	for l, t := range e.By {
		if t.InOut()+grown[l].InOut() > 0 {
			n++
		}
	}
	for l, g := range grown {
		if _, ok := e.By[l]; !ok && g.InOut() > 0 {
			n++
		}
	}
	return n
}

// grownHours is where a session's growth of spent tokens went: the hours in
// which its log shows more than the ledger placed, in proportion, leaving out
// those the ledger no longer keeps. A log without times, or one that shows no
// such hour, has it all at updated.
func grownHours(placed, log map[int64]int64, spent int64, updated, now time.Time) map[int64]int64 {
	cutoff := now.Add(-state.Retention)
	out := map[int64]int64{}
	for h, n := range log {
		if d := n - placed[h]; d > 0 && hourKept(h, cutoff) {
			out[h] = d
		}
	}
	if len(out) == 0 {
		return map[int64]int64{logs.HourOf(updated): spent}
	}
	logs.ScaleHours(out, spent)
	return out
}

// hourKept reports whether the ledger keeps hour h, which it does while the
// hour ended at cutoff or later.
func hourKept(h int64, cutoff time.Time) bool {
	return !logs.HourStart(h + 1).Before(cutoff)
}

// dropHours drops the hours the ledger no longer keeps.
func dropHours(hours map[int64]int64, cutoff time.Time) {
	for h := range hours {
		if !hourKept(h, cutoff) {
			delete(hours, h)
		}
	}
}

// labelHours is label's part of a session's hours. Once a second account
// spends in a session, each keeps its own; until then they are all the one
// account's. A ledger from before accounts kept their own gives each its
// share of the session's input plus output, spread over the hours as the
// session's are. A session whose hours are not known, as one the ledger kept
// from before it recorded them, has the part at the hour label's share last
// grew.
func labelHours(s *state.Session, label string) map[int64]int64 {
	if s.ByHours != nil {
		return s.ByHours[label]
	}
	own := s.By[label].InOut()
	if own <= 0 {
		return nil
	}
	var all, sum int64
	for _, t := range s.By {
		all += t.InOut()
	}
	for _, n := range s.Hours {
		sum += n
	}
	if sum <= 0 {
		return map[int64]int64{logs.HourOf(lastActive(s, label)): own}
	}
	if own == all {
		return s.Hours
	}
	out := maps.Clone(s.Hours)
	logs.ScaleHours(out, int64(float64(sum)*float64(own)/float64(all)))
	return out
}

// accountHours is each account's part of a session's hours, as labelHours
// gives it.
func accountHours(s *state.Session) map[string]map[int64]int64 {
	out := map[string]map[int64]int64{}
	for l := range s.By {
		if h := labelHours(s, l); len(h) > 0 {
			out[l] = maps.Clone(h)
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
