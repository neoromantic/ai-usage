package collect

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// Cycles are the periods of each account's quota windows that this device
// read, by HistoryKey: each period with the fullest reading of it, by window,
// newest first.
type Cycles map[string][]snapshot.Cycle

// cycleSlack is how far apart two readings may say a window resets and
// still be of one period: a provider's reset time wobbles by a second or
// two, and one read from a log is rounded to the minute or the hour.
const cycleSlack = 5 * time.Minute

// ShortCycles is how many of a window's periods a snapshot keeps when the
// window is shorter than a day, or of no known length: the newest. Of a
// longer window it keeps every one the samples hold.
const ShortCycles = 12

// LoadCycles reads this device's samples and finds the quota cycles in
// them.
func LoadCycles(d state.Dir, now time.Time) (Cycles, error) {
	samples, err := d.LoadSamples(now.Add(-state.Retention))
	return FindCycles(samples), err
}

// FindCycles finds the periods of each account's quota windows in the
// samples, each with the fullest reading of it. A period is one reset time,
// give or take cycleSlack, so a drop within it, as when a provider takes
// back usage or a window resets early after it ran out, changes nothing.
// Only a subscription's own windows count: not Hermes, whose quota is
// another login's, not an account that is not known, and not a window
// without a reset time. A period read only empty is left out: Codex moves
// an idle window's reset with every reading.
func FindCycles(samples []state.Sample) Cycles {
	read := map[string][]snapshot.Cycle{}
	lengths := map[string]map[string]time.Duration{}
	for _, s := range samples {
		for _, a := range s.Accounts {
			if !subscription(a.Provider) || a.Label == "" || a.Label == UnknownAccount || a.QuotaAt == nil {
				continue
			}
			k := HistoryKey(a.Provider, a.Label)
			for _, w := range a.Windows {
				if w.ResetsAt == nil || w.Name == "" {
					continue
				}
				reset := w.ResetsAt.UTC().Truncate(time.Second)
				at := a.QuotaAt.UTC().Truncate(time.Second)
				read[k] = append(read[k], snapshot.Cycle{Window: w.Name, ResetsAt: reset, Percent: w.Percent, LastReadAt: minTime(at, reset)})
				if lengths[k] == nil {
					lengths[k] = map[string]time.Duration{}
				}
				lengths[k][w.Name] = max(lengths[k][w.Name], w.Length())
			}
		}
	}
	out := Cycles{}
	for k, rs := range read {
		if cs := boundCycles(MergeCycles(rs), lengths[k]); len(cs) > 0 {
			out[k] = cs
		}
	}
	return out
}

// subscription reports whether provider's quota is an account's own.
func subscription(provider string) bool {
	return provider != "hermes" && snapshot.KnownProvider(provider)
}

// MergeCycles makes one list of the periods in lists, such as one device's
// readings or every device's snapshot: periods of a window whose reset
// times are cycleSlack or less apart, one after another, are one, with the
// fullest reading of any, read last when any was. A period whose fullest
// reading is empty is left out. They come by window, newest first.
func MergeCycles(lists ...[]snapshot.Cycle) []snapshot.Cycle {
	var all []snapshot.Cycle
	for _, l := range lists {
		all = append(all, l...)
	}
	slices.SortFunc(all, func(a, b snapshot.Cycle) int {
		return cmp.Or(cmp.Compare(a.Window, b.Window), a.ResetsAt.Compare(b.ResetsAt))
	})
	var out []snapshot.Cycle
	for i := 0; i < len(all); {
		c := all[i]
		j := i + 1
		for ; j < len(all) && all[j].Window == c.Window && all[j].ResetsAt.Sub(c.ResetsAt) <= cycleSlack; j++ {
			n := all[j]
			c.ResetsAt = n.ResetsAt
			c.Percent = max(c.Percent, n.Percent)
			if n.LastReadAt.After(c.LastReadAt) {
				c.LastReadAt = n.LastReadAt
			}
		}
		if c.Percent > 0 {
			out = append(out, c)
		}
		i = j
	}
	slices.SortStableFunc(out, func(a, b snapshot.Cycle) int {
		return cmp.Or(cmp.Compare(a.Window, b.Window), b.ResetsAt.Compare(a.ResetsAt))
	})
	return out
}

// boundCycles keeps the newest ShortCycles periods of each window shorter
// than a day, or of no known length, and then, while there are more than a
// snapshot holds, drops the oldest period of the window with the most.
func boundCycles(cs []snapshot.Cycle, lengths map[string]time.Duration) []snapshot.Cycle {
	var out []snapshot.Cycle
	kept := map[string]int{}
	for _, c := range cs {
		if lengths[c.Window] < 24*time.Hour && kept[c.Window] == ShortCycles {
			continue
		}
		kept[c.Window]++
		out = append(out, c)
	}
	for len(out) > snapshot.MaxCycles {
		i := oldestOfMost(out)
		out = slices.Delete(out, i, i+1)
	}
	return out
}

// oldestOfMost is the index of the oldest period of the window with the
// most, the first such window when several have as many. cs is by window,
// newest first, and not empty.
func oldestOfMost(cs []snapshot.Cycle) int {
	at, most, n := 0, 0, 0
	for i, c := range cs {
		n++
		if i+1 < len(cs) && cs[i+1].Window == c.Window {
			continue
		}
		if n > most {
			at, most = i, n
		}
		n = 0
	}
	return at
}

// cycleBytes is how many bytes c takes in a list of an account's cycles,
// with the comma before it.
func cycleBytes(c snapshot.Cycle) int {
	b, _ := json.Marshal(c)
	return len(b) + 1
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
