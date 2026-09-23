package view

import (
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// Pace is how fast one window is filling, from stored samples.
type Pace struct {
	PerHour float64 `json:"percent_per_hour"`
	// FillsAt is when the window reaches 100% at this pace. It is set only
	// when that happens before the window resets.
	FillsAt *time.Time `json:"fills_at"`
}

type point struct {
	at  time.Time
	pct float64
}

// paceLookback bounds the samples a pace is fitted to.
const paceLookback = 6 * time.Hour

// windowPace fits the readings of one window since its current period began.
// Readings are keyed by when the harness observed them, so a cached value
// repeated across runs counts once. It needs two readings at least 10 minutes
// apart; otherwise there is no pace.
func windowPace(samples []state.Sample, provider, label string, cur snapshot.Window, curAt time.Time) *Pace {
	// Keyed by instant: equal times decoded from different documents may
	// carry different locations and would not match as time.Time keys.
	seen := map[int64]bool{}
	var pts []point
	add := func(at time.Time, w snapshot.Window) {
		if seen[at.UnixNano()] || at.After(curAt) || curAt.Sub(at) > paceLookback {
			return
		}
		if !sameReset(w.ResetsAt, cur.ResetsAt) {
			return
		}
		seen[at.UnixNano()] = true
		pts = append(pts, point{at, w.Percent})
	}
	for _, s := range samples {
		for _, a := range s.Accounts {
			if a.Provider != provider || a.Label != label || a.QuotaAt == nil {
				continue
			}
			for _, w := range a.Windows {
				if w.Name == cur.Name {
					add(*a.QuotaAt, w)
				}
			}
		}
	}
	add(curAt, cur)
	if len(pts) < 2 {
		return nil
	}
	first, last := pts[0], pts[0]
	for _, p := range pts {
		if p.at.Before(first.at) {
			first = p
		}
		if p.at.After(last.at) {
			last = p
		}
	}
	span := last.at.Sub(first.at)
	if span < 10*time.Minute {
		return nil
	}
	perHour := (last.pct - first.pct) / span.Hours()
	pace := &Pace{PerHour: round1(perHour)}
	if perHour <= 0 || last.pct >= 100 {
		return pace
	}
	fills := last.at.Add(time.Duration((100 - last.pct) / perHour * float64(time.Hour))).UTC().Truncate(time.Minute)
	if cur.ResetsAt == nil || fills.Before(*cur.ResetsAt) {
		pace.FillsAt = &fills
	}
	return pace
}

func sameReset(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	d := a.Sub(*b)
	return d < 5*time.Minute && d > -5*time.Minute
}

func round1(f float64) float64 {
	if f < 0 {
		return -round1(-f)
	}
	return float64(int64(f*10+0.5)) / 10
}
