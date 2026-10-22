package cost

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// MaxData is the most bytes an answer's data may take before it is sealed:
// a sealed string is at most snapshot.MaxSealed characters.
const MaxData = 356

// dataSkew is how far past its snapshot's collected_at a time in an answer
// may be, as clocks drift.
const dataSkew = 10 * time.Minute

// data is an answer as a snapshot carries it, sealed: short keys, read
// leniently, so that a later release adds keys without a gate. A cleared
// answer is null with its time. pt dates p and pl, ct c, and rt r, in Unix
// seconds; x and ok are their own times.
type data struct {
	P  json.RawMessage `json:"p,omitempty"`
	PL string          `json:"pl,omitempty"`
	PT int64           `json:"pt,omitempty"`
	C  json.RawMessage `json:"c,omitempty"`
	CT int64           `json:"ct,omitempty"`
	R  json.RawMessage `json:"r,omitempty"`
	RT int64           `json:"rt,omitempty"`
	X  string          `json:"x,omitempty"`
	OK string          `json:"ok,omitempty"`
}

var null = json.RawMessage("null")

// EncodeAnswer is a's data for a snapshot, before it is sealed, and the
// time of its newest answer. ok is false when a says nothing, or its data
// would not fit, which a plan typed for of 40 bytes cannot make it.
func EncodeAnswer(a Answer) (string, time.Time, bool) {
	if a.IsZero() {
		return "", time.Time{}, false
	}
	var d data
	if !a.PriceAt.IsZero() {
		d.P, d.PT = null, a.PriceAt.Unix()
		if a.Price != nil {
			d.P, d.PL = json.RawMessage(strconv.FormatFloat(*a.Price, 'f', -1, 64)), a.PriceFor
		}
	}
	if !a.CadenceAt.IsZero() {
		d.C, d.CT = null, a.CadenceAt.Unix()
		switch a.Cadence {
		case Monthly:
			d.C = json.RawMessage(`"m"`)
		case Yearly:
			d.C = json.RawMessage(`"y"`)
		}
	}
	if !a.RenewsAt.IsZero() {
		d.R, d.RT = null, a.RenewsAt.Unix()
		if a.Renews != "" {
			d.R, _ = json.Marshal(a.Renews)
		}
	}
	if !a.CancelledAt.IsZero() {
		d.X = a.CancelledAt.UTC().Format(time.RFC3339)
	}
	if !a.ConfirmedAt.IsZero() {
		d.OK = a.ConfirmedAt.UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(d)
	if err != nil || len(b) > MaxData {
		return "", time.Time{}, false
	}
	return string(b), a.At().UTC().Truncate(time.Second), true
}

// DecodeAnswer reads an answer's opened data from a snapshot collected at
// collected. Every part is checked on its own: one that is not what this
// build writes, or is dated after collected, is left out, and the rest
// stays. A teammate's device can seal anything.
func DecodeAnswer(s string, collected time.Time) Answer {
	var raw map[string]json.RawMessage
	var a Answer
	if json.Unmarshal([]byte(s), &raw) != nil {
		return a
	}
	latest := collected.Add(dataSkew)
	at := func(key string) time.Time {
		var n int64
		if json.Unmarshal(raw[key], &n) != nil || n <= 0 {
			return time.Time{}
		}
		t := time.Unix(n, 0).UTC()
		if t.After(latest) {
			return time.Time{}
		}
		return t
	}
	stamp := func(key string) time.Time {
		var v string
		if json.Unmarshal(raw[key], &v) != nil {
			return time.Time{}
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil || t.After(latest) {
			return time.Time{}
		}
		return t.UTC()
	}
	isNull := func(key string) bool { return bytes.Equal(bytes.TrimSpace(raw[key]), null) }
	if t := at("pt"); !t.IsZero() {
		var p float64
		var plan string
		switch {
		case isNull("p"):
			a.PriceAt = t
		case json.Unmarshal(raw["p"], &p) == nil && p >= 0 && p <= MaxUSD && !math.IsNaN(p):
			a.Price, a.PriceAt = &p, t
			if json.Unmarshal(raw["pl"], &plan) == nil && snapshot.PlainLabel(plan) == plan {
				a.PriceFor = plan
			}
		}
	}
	if t := at("ct"); !t.IsZero() {
		var c string
		switch {
		case isNull("c"):
			a.CadenceAt = t
		case json.Unmarshal(raw["c"], &c) == nil && (c == "m" || c == "y"):
			a.Cadence, a.CadenceAt = map[string]string{"m": Monthly, "y": Yearly}[c], t
		}
	}
	if t := at("rt"); !t.IsZero() {
		var r string
		switch {
		case isNull("r"):
			a.RenewsAt = t
		case json.Unmarshal(raw["r"], &r) == nil:
			if _, err := parseDay(r); err == nil {
				a.Renews, a.RenewsAt = r, t
			}
		}
	}
	a.CancelledAt, a.ConfirmedAt = stamp("x"), stamp("ok")
	return a
}

// Merge is what devices said of one subscription, by device id: each
// answer the newest any of them gave, and of two given at once, the one of
// the smaller device id, so every device merges the same.
func Merge(voices map[string]Answer) Answer {
	var out Answer
	ids := make([]string, 0, len(voices))
	for id := range voices {
		ids = append(ids, id)
	}
	// Of answers given at once, the first, from the smallest id, stays.
	slices.Sort(ids)
	newer := func(t, cur time.Time) bool { return !t.IsZero() && t.After(cur) }
	for _, id := range ids {
		a := voices[id]
		if newer(a.PriceAt, out.PriceAt) {
			out.Price, out.PriceFor, out.PriceAt = a.Price, a.PriceFor, a.PriceAt
		}
		if newer(a.CadenceAt, out.CadenceAt) {
			out.Cadence, out.CadenceAt = a.Cadence, a.CadenceAt
		}
		if newer(a.RenewsAt, out.RenewsAt) {
			out.Renews, out.RenewsAt = a.Renews, a.RenewsAt
		}
		if newer(a.CancelledAt, out.CancelledAt) {
			out.CancelledAt = a.CancelledAt
		}
		if newer(a.ConfirmedAt, out.ConfirmedAt) {
			out.ConfirmedAt = a.ConfirmedAt
		}
	}
	return out
}

// Heard is what each device of docs said of each subscription, by Key and
// device id, as the costs its snapshot carries say, opened with open. An
// entry that does not open, or of a provider with no price, is left out.
func Heard(docs []snapshot.Doc, open func(string) (string, error)) map[string]map[string]Answer {
	out := map[string]map[string]Answer{}
	for _, d := range docs {
		for _, c := range d.Costs {
			if !costProvider(c.Provider) {
				continue
			}
			label, err := open(c.Label)
			if err != nil || label == "" {
				continue
			}
			s, err := open(c.Data)
			if err != nil {
				continue
			}
			a := DecodeAnswer(s, d.CollectedAt)
			if a.IsZero() {
				continue
			}
			k := Key(c.Provider, label)
			if out[k] == nil {
				out[k] = map[string]Answer{}
			}
			if cur, ok := out[k][d.Device]; !ok || a.At().After(cur.At()) {
				out[k][d.Device] = a
			}
		}
	}
	return out
}
