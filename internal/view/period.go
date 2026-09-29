package view

import (
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Period is the span of tokens the matrix, USAGE, and PROJECTS show. The
// team's tables count UTC days, as devices send them; this device's own
// USAGE and PROJECTS count its local day and the hours before now.
type Period int

const (
	Week    Period = iota // 7d, the default
	Today                 // the report's day
	Month                 // 30d
	Quarter               // 90d
)

// Periods are the periods in the order `p` steps through them.
var Periods = []Period{Today, Week, Month, Quarter}

func (p Period) String() string {
	switch p {
	case Today:
		return "today"
	case Month:
		return "30d"
	case Quarter:
		return "90d"
	default:
		return "7d"
	}
}

// Of is the period's tokens in u.
func (p Period) Of(u Usage) int64 {
	switch p {
	case Today:
		return u.Today
	case Month:
		return u.Month
	case Quarter:
		return u.Quarter
	default:
		return u.Week
	}
}

// share is the period's part in s.
func (p Period) share(s Share) *float64 {
	switch p {
	case Today:
		return s.Today
	case Month:
		return s.Month
	case Quarter:
		return s.Quarter
	default:
		return s.Week
	}
}

// Next is the period after p, back to today after 90d.
func (p Period) Next() Period {
	for i, q := range Periods {
		if q == p {
			return Periods[(i+1)%len(Periods)]
		}
	}
	return Week
}

// hours is how many hours up to now each rolling period of this device's
// own views takes, the current one included.
func (p Period) hours() int64 {
	switch p {
	case Month:
		return 30 * 24
	case Quarter:
		return 90 * 24
	default:
		return 7 * 24
	}
}

// localMidnight is the start of now's day in loc.
func localMidnight(now time.Time, loc *time.Location) time.Time {
	t := now.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// inLocal says whether the hour h, keyed by logs.HourOf, counts in the
// period p of this device's own views as of now: today when most of it is
// past local midnight, so a zone half an hour off UTC splits no hour, else
// when it is one of the period's last hours, the current one included. An
// hour after now's counts in none.
func inLocal(h int64, p Period, now time.Time, midnight time.Time) bool {
	cur := logs.HourOf(now)
	if h > cur {
		return false
	}
	if p == Today {
		return !logs.HourStart(h).Add(30 * time.Minute).Before(midnight)
	}
	return cur-h < p.hours()
}

// localUsage sums hourly values, keyed by logs.HourOf, into this device's
// local periods as of now in loc.
func localUsage(series map[int64]int64, now time.Time, loc *time.Location) Usage {
	mid := localMidnight(now, loc)
	var u Usage
	for h, n := range series {
		for _, p := range Periods {
			if inLocal(h, p, now, mid) {
				*p.field(&u) += n
			}
		}
	}
	return u
}

// field is the period's value in u.
func (p Period) field(u *Usage) *int64 {
	switch p {
	case Today:
		return &u.Today
	case Month:
		return &u.Month
	case Quarter:
		return &u.Quarter
	default:
		return &u.Week
	}
}

// localDays sums hourly values into local days in loc, newest first: the
// first is now's day, for up to snapshot.MaxDays days, trailing zeros left
// out. An hour counts on the day most of it falls in, and one after now's
// on none.
func localDays(series map[int64]int64, now time.Time, loc *time.Location) []int64 {
	today, cur := dayOf(now, loc), logs.HourOf(now)
	out := []int64{}
	for h, n := range series {
		i := today - dayOf(logs.HourStart(h).Add(30*time.Minute), loc)
		if h > cur || i < 0 || i >= snapshot.MaxDays || n <= 0 {
			continue
		}
		for int64(len(out)) <= i {
			out = append(out, 0)
		}
		out[i] += n
	}
	return out
}

// dayOf numbers t's calendar day in loc, so that consecutive days differ by
// one, whatever their length.
func dayOf(t time.Time, loc *time.Location) int64 {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400
}

// activeCounts counts the times in each of this device's local periods as
// of now in loc: today from local midnight, and the rest by the hours they
// fall in, as localUsage counts them. A time ahead of now, from a clock
// ahead of this one's, is now.
func activeCounts(times []time.Time, now time.Time, loc *time.Location) Counts {
	mid, cur := localMidnight(now, loc), logs.HourOf(now)
	var c Counts
	for _, t := range times {
		h := min(logs.HourOf(t), cur)
		if !t.Before(mid) {
			c.Today++
		}
		for _, p := range []Period{Week, Month, Quarter} {
			if cur-h < p.hours() {
				*p.count(&c)++
			}
		}
	}
	return c
}

// count is the period's count in c.
func (p Period) count(c *Counts) *int {
	switch p {
	case Today:
		return &c.Today
	case Month:
		return &c.Month
	case Quarter:
		return &c.Quarter
	default:
		return &c.Week
	}
}
