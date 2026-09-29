// Package meter counts tokens by kind and by hour, and weighs them into burn:
// one number per hour that follows what the tokens cost against a quota.
//
// A log records four kinds of tokens, and Claude splits its cache writes by
// how long they are kept. They cost very differently: an hour of cache reads
// can hold ten times the tokens of an hour of fresh input and cost a tenth as
// much. Burn weighs each kind by its price relative to input.
package meter

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/bits"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Use is tokens by kind. CacheWrite1h is the part of CacheWrite kept for an
// hour (Claude); the rest of CacheWrite is kept for five minutes.
type Use struct {
	Input, Output, CacheRead, CacheWrite, CacheWrite1h int64
}

// kinds is how many kinds of tokens a Use counts.
const kinds = 5

func (u Use) array() [kinds]int64 {
	return [kinds]int64{u.Input, u.Output, u.CacheRead, u.CacheWrite, u.CacheWrite1h}
}

func fromArray(a [kinds]int64) Use {
	return Use{Input: a[0], Output: a[1], CacheRead: a[2], CacheWrite: a[3], CacheWrite1h: a[4]}
}

// FromTokens is t as a Use. A log's counts do not say how long cache writes
// are kept, so none are taken to be kept for an hour.
func FromTokens(t snapshot.Tokens) Use {
	return Use{Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite}
}

// Tokens is u as the counts a snapshot carries.
func (u Use) Tokens() snapshot.Tokens {
	return snapshot.Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
}

func (u Use) Add(v Use) Use {
	a, b := u.array(), v.array()
	for i := range a {
		a[i] += b[i]
	}
	return fromArray(a)
}

// Sub is u less v, kind by kind, never below zero.
func (u Use) Sub(v Use) Use {
	a, b := u.array(), v.array()
	for i := range a {
		a[i] = max(a[i]-b[i], 0)
	}
	return fromArray(a)
}

func (u Use) Zero() bool { return u == Use{} }

// InOut is input plus output, the count reports showed before burn.
func (u Use) InOut() int64 { return u.Input + u.Output }

// Fresh is the new context u added: input, cache writes and output. It is
// the same count on every harness, whatever each charges for it.
func (u Use) Fresh() int64 { return u.Input + u.CacheWrite + u.Output }

// Total is every token counted, each kind once.
func (u Use) Total() int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

// Weights are what a token of each kind costs, an input token costing 1.
// They are ratios of API prices, which follow quota use closely enough to
// compare hours, sessions and people; they are not a provider's quota
// formula.
type Weights struct {
	Output, CacheRead, CacheWrite, CacheWrite1h float64
}

var (
	claudeWeights = Weights{Output: 5, CacheRead: 0.1, CacheWrite: 1.25, CacheWrite1h: 2}
	codexWeights  = Weights{Output: 8, CacheRead: 0.1, CacheWrite: 1, CacheWrite1h: 1}
	grokWeights   = Weights{Output: 5, CacheRead: 0.25, CacheWrite: 1, CacheWrite1h: 1}
)

// WeightsOf is the weights of a provider's tokens. Hermes bills each call to
// the provider its label names, and takes that provider's weights; a label
// that names none, as an API key or a fallback route, takes Claude's.
func WeightsOf(provider, label string) Weights {
	switch provider {
	case "claude":
		return claudeWeights
	case "codex":
		return codexWeights
	case "grok":
		return grokWeights
	case "hermes":
		l := strings.ToLower(label)
		has := func(words ...string) bool {
			return slices.ContainsFunc(words, func(w string) bool { return strings.Contains(l, w) })
		}
		switch {
		case has("openai", "codex", "gpt"):
			return codexWeights
		case has("xai", "grok"):
			return grokWeights
		}
	}
	return claudeWeights
}

// Burn is u weighed by w, rounded to a whole token.
func (u Use) Burn(w Weights) int64 {
	five := max(u.CacheWrite-u.CacheWrite1h, 0)
	b := float64(u.Input) + float64(u.Output)*w.Output + float64(u.CacheRead)*w.CacheRead +
		float64(five)*w.CacheWrite + float64(u.CacheWrite1h)*w.CacheWrite1h
	return int64(math.Round(b))
}

// TokensBurn is the burn of counts that do not say how long cache writes are
// kept, as a snapshot's: all of them weigh as kept for five minutes.
func TokensBurn(t snapshot.Tokens, provider, label string) int64 {
	return FromTokens(t).Burn(WeightsOf(provider, label))
}

// MarshalJSON writes u as [input, output, cache read, cache write, cache
// write kept an hour], without trailing zeros: a ledger holds thousands.
func (u Use) MarshalJSON() ([]byte, error) {
	a := u.array()
	n := kinds
	for n > 0 && a[n-1] == 0 {
		n--
	}
	return json.Marshal(a[:n])
}

// UnmarshalJSON reads what MarshalJSON writes: up to five counts, the
// missing ones zero.
func (u *Use) UnmarshalJSON(b []byte) error {
	var a []int64
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if len(a) > kinds {
		return fmt.Errorf("meter: %d counts, want at most %d", len(a), kinds)
	}
	var k [kinds]int64
	copy(k[:], a)
	*u = fromArray(k)
	return nil
}

// Hours is tokens by the UTC hour they were spent in, keyed by Unix time /
// 3600.
type Hours map[int64]Use

// hourOf is the key of the UTC hour t falls in.
func hourOf(t time.Time) int64 { return t.Unix() / 3600 }

// Add adds u spent at t, making the map when needed.
func (h *Hours) Add(t time.Time, u Use) {
	if u.Zero() || t.IsZero() {
		return
	}
	if *h == nil {
		*h = Hours{}
	}
	k := hourOf(t)
	(*h)[k] = (*h)[k].Add(u)
}

// AddHours adds b onto a and returns a, made when needed.
func AddHours(a, b Hours) Hours {
	for k, u := range b {
		if a == nil {
			a = Hours{}
		}
		a[k] = a[k].Add(u)
	}
	return a
}

func (h Hours) Clone() Hours { return maps.Clone(h) }

// Sum is every hour's tokens.
func (h Hours) Sum() Use {
	var s Use
	for _, u := range h {
		s = s.Add(u)
	}
	return s
}

// Since is the hours from the hour with key first on, or nil when there are
// none.
func (h Hours) Since(first int64) Hours {
	var out Hours
	for k, u := range h {
		if k >= first {
			if out == nil {
				out = Hours{}
			}
			out[k] = u
		}
	}
	return out
}

// Series is f of each hour, leaving out the hours where it is zero.
func (h Hours) Series(f func(Use) int64) map[int64]int64 {
	out := map[int64]int64{}
	for k, u := range h {
		if n := f(u); n != 0 {
			out[k] = n
		}
	}
	return out
}

// ScaleTo makes the hours add up to total, kind by kind, keeping each kind's
// shape over the hours. The sums are exact: what rounding leaves goes to the
// hours with the largest remainders, the earliest first on a tie. A kind the
// hours lack is spread as all their tokens are, and hours with no tokens at
// all have everything at the fallback hour. The hour-long cache writes of an
// hour never exceed its cache writes.
func (h *Hours) ScaleTo(total Use, fallback int64) {
	total.CacheWrite1h = min(max(total.CacheWrite1h, 0), total.CacheWrite)
	keys := slices.Sorted(maps.Keys(*h))
	all := make([]int64, len(keys))
	for i, k := range keys {
		all[i] = (*h)[k].Total()
	}
	if len(keys) == 0 || sum(all) <= 0 {
		keys, all = []int64{fallback}, []int64{1}
	}
	tot := total.array()
	out := make([][kinds]int64, len(keys))
	for kind := range kinds {
		shape := make([]int64, len(keys))
		for i, k := range keys {
			shape[i] = (*h)[k].array()[kind]
		}
		if kind == 4 && sum(shape) <= 0 {
			// Hour-long writes go where the writes went.
			for i := range out {
				shape[i] = out[i][3]
			}
		}
		if sum(shape) <= 0 {
			shape = all
		}
		for i, n := range split(tot[kind], shape) {
			out[i][kind] = n
		}
	}
	clampHourLong(out)
	next := Hours{}
	for i, k := range keys {
		if u := fromArray(out[i]); !u.Zero() {
			next[k] = u
		}
	}
	*h = next
}

// clampHourLong moves an hour's hour-long cache writes beyond its cache
// writes to the earliest hours that have room, keeping the sum.
func clampHourLong(out [][kinds]int64) {
	var extra int64
	for i := range out {
		if d := out[i][4] - out[i][3]; d > 0 {
			out[i][4] -= d
			extra += d
		}
	}
	for i := range out {
		if extra == 0 {
			return
		}
		d := min(out[i][3]-out[i][4], extra)
		out[i][4] += d
		extra -= d
	}
}

// Spread is total spread over the hours of shape in its proportion, kind by
// kind, with exact sums as ScaleTo has them. It is nil when shape has no
// weight.
func Spread(total Use, shape map[int64]int64) Hours {
	h := Hours{}
	for k, n := range shape {
		if n > 0 {
			h[k] = Use{Input: n}
		}
	}
	if len(h) == 0 {
		return nil
	}
	h.ScaleTo(total, 0)
	if len(h) == 0 {
		return nil
	}
	return h
}

// TakeNewest removes u from the hours, kind by kind, from the newest hour
// back, never taking an hour below zero. It returns what it took from each
// hour.
func (h Hours) TakeNewest(u Use) Hours {
	left := u.array()
	var taken Hours
	keys := slices.Sorted(maps.Keys(h))
	for i := len(keys) - 1; i >= 0; i-- {
		k := keys[i]
		have := h[k].array()
		var took [kinds]int64
		for kind := range kinds {
			d := min(have[kind], max(left[kind], 0))
			took[kind] = d
			have[kind] -= d
			left[kind] -= d
		}
		have[4] = min(have[4], have[3])
		if t := fromArray(took); !t.Zero() {
			if taken == nil {
				taken = Hours{}
			}
			taken[k] = t
		}
		if u := fromArray(have); u.Zero() {
			delete(h, k)
		} else {
			h[k] = u
		}
	}
	return taken
}

func sum(ns []int64) int64 {
	var s int64
	for _, n := range ns {
		s += max(n, 0)
	}
	return s
}

// split divides total in proportion to shape, which must have weight, with
// an exact sum: each part is rounded down, and what that leaves goes one by
// one to the parts with the largest remainders, the first on a tie. The
// products are computed in 128 bits, so counts never overflow.
func split(total int64, shape []int64) []int64 {
	out := make([]int64, len(shape))
	den := uint64(sum(shape))
	if total <= 0 || den == 0 {
		return out
	}
	rems := make([]uint64, len(shape))
	left := total
	for i, n := range shape {
		if n <= 0 {
			continue
		}
		hi, lo := bits.Mul64(uint64(total), uint64(n))
		q, r := bits.Div64(hi, lo, den)
		out[i], rems[i] = int64(q), r
		left -= int64(q)
	}
	order := make([]int, len(shape))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		switch {
		case rems[a] > rems[b]:
			return -1
		case rems[a] < rems[b]:
			return 1
		}
		return 0
	})
	for _, i := range order {
		if left == 0 {
			break
		}
		if shape[i] > 0 {
			out[i]++
			left--
		}
	}
	return out
}
