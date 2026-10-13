package view

import (
	"cmp"
	"slices"
	"strconv"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// utilization is the history of an account's quota windows from the
// periods the team's snapshots carry, merged: each window with a period
// that ended, the main one first, then the longest. q is the account's
// reading, which names its main window.
func utilization(cycles []snapshot.Cycle, q *Quota, now time.Time) []Utilization {
	merged := collect.MergeCycles(cycles)
	var out []Utilization
	for i := 0; i < len(merged); {
		j := i + 1
		for j < len(merged) && merged[j].Window == merged[i].Window {
			j++
		}
		if u, ok := windowUtilization(merged[i:j], now); ok {
			out = append(out, u)
		}
		i = j
	}
	if len(out) == 0 {
		return []Utilization{}
	}
	main := utilizationMain(out, q)
	for i := range out {
		out[i].Main = out[i].Window == main
	}
	slices.SortStableFunc(out, func(a, b Utilization) int {
		if a.Main != b.Main {
			if a.Main {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(cycleLength(b.Window), cycleLength(a.Window)), cmp.Compare(a.Window, b.Window))
	})
	return out
}

// windowUtilization is one window's history from its periods cs, newest
// first: those that ended by now, within the 90 days a device keeps, and of
// a window shorter than a day the newest a snapshot keeps. A period ended
// when it reset, or when a newer one began, as when Codex resets a window
// early.
func windowUtilization(cs []snapshot.Cycle, now time.Time) (Utilization, bool) {
	u := Utilization{Window: cs[0].Window, Cycles: []Cycle{}}
	length := cycleLength(u.Window)
	short := length < 24*time.Hour
	for i, c := range cs {
		if i == 0 && c.ResetsAt.After(now) {
			continue
		}
		if now.Sub(c.ResetsAt) > state.Retention || short && len(u.Cycles) == collect.ShortCycles {
			break
		}
		// It ended at its reset, or as the newer one began, which is never
		// before it was last read.
		end := c.ResetsAt
		if i > 0 && length > 0 {
			if began := cs[i-1].ResetsAt.Add(-length); began.Before(end) {
				end = began
				if end.Before(c.LastReadAt) {
					end = c.LastReadAt
				}
			}
		}
		u.Cycles = append(u.Cycles, Cycle{
			ResetsAt:   c.ResetsAt,
			Percent:    c.Percent,
			LastReadAt: c.LastReadAt,
			Partial:    c.Percent < 100 && end.Sub(c.LastReadAt) > partialAfter(length),
			State:      cycleState(c.Percent),
		})
	}
	if len(u.Cycles) == 0 {
		return u, false
	}
	sum := 0.0
	for _, c := range u.Cycles {
		sum += c.Percent
		if c.Percent >= 100 {
			u.Out++
		}
	}
	u.Average = sum / float64(len(u.Cycles))
	return u, true
}

// cycleLength is how long a window is by its name, or 0 when the name does
// not say: a cycle carries no length of its own.
func cycleLength(name string) time.Duration { return snapshot.Window{Name: name}.Length() }

// partialAfter is how long before a period ended its last reading may be
// for the period to count as read to its end: StaleAfter, or a quarter of a
// window shorter than a day.
func partialAfter(length time.Duration) time.Duration {
	if length > 0 && length/4 < StaleAfter {
		return length / 4
	}
	return StaleAfter
}

// cycleState is the band of a period's fullest reading: out at 100% or
// more, else as a forecast's.
func cycleState(pct float64) string {
	if pct >= 100 {
		return StateOut
	}
	return forecastState(pct)
}

// utilizationMain is the main window of the history us: the reading's main
// window when it has one, else the weekly one, else the longest.
func utilizationMain(us []Utilization, q *Quota) string {
	if q != nil {
		if m := mainWindow(q.Windows); m != nil && slices.ContainsFunc(us, func(u Utilization) bool { return u.Window == m.Name }) {
			return m.Name
		}
	}
	rs := make([]reading, len(us))
	for i, u := range us {
		rs[i] = reading{Window: snapshot.Window{Name: u.Window}}
	}
	return us[mainIndex(rs)].Window
}

// cyclesGate says, when this device has quota cycles the gate keeps from the
// team, which devices keep them and why, as cycles_held_back_by and
// cycles_reason have it. Without a team, or a read of it, there is no one
// to keep them from.
func (m *merger) cyclesGate(t *Team) {
	t.CyclesHeldBackBy = []string{}
	if !m.in.Doc.HasCycles() {
		return
	}
	g := collect.CyclesWire(m.in.Team, m.in.Key.Fingerprint(), m.in.Config.Device, collect.NoCyclesUntil(m.in.State.Relay), m.now)
	if g.Open || g.Reason == collect.GateTeam {
		return
	}
	hs := m.heldBackDevices(g)
	for _, h := range hs {
		t.CyclesHeldBackBy = append(t.CyclesHeldBackBy, h.Label)
	}
	var reason string
	switch {
	case g.Reason == collect.GateStale:
		reason = "the team was last read " + age(m.now.Sub(m.in.Team.PulledAt)) + " ago"
	case g.Reason == collect.GateRejected:
		reason = "the relay did not accept cycles; trying again " + g.Until.In(m.loc()).Format("Jan 2 15:04")
	case len(hs) == 1:
		reason = m.heldBackReason(hs[0], collect.CyclesSince)
	default:
		reason = strconv.Itoa(len(hs)) + " devices run releases before " + collect.CyclesSince + ": " + m.heldBackSteps(hs)
	}
	t.CyclesReason = strPtr(reason)
}
