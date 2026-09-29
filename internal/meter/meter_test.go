package meter

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

func TestWeightsOf(t *testing.T) {
	cases := []struct {
		provider, label string
		want            Weights
	}{
		{"claude", "ann@acme.dev", Weights{Output: 5, CacheRead: 0.1, CacheWrite: 1.25, CacheWrite1h: 2}},
		{"codex", "ann@acme.dev", Weights{Output: 8, CacheRead: 0.1, CacheWrite: 1, CacheWrite1h: 1}},
		{"grok", "sam@mail.test", Weights{Output: 5, CacheRead: 0.25, CacheWrite: 1, CacheWrite1h: 1}},
		{"hermes", "openai-codex", codexWeights},
		{"hermes", "xai-oauth", grokWeights},
		{"hermes", "anthropic", claudeWeights},
		{"hermes", "unknown", claudeWeights},
		{"hermes", "custom-key", claudeWeights},
	}
	for _, c := range cases {
		if got := WeightsOf(c.provider, c.label); got != c.want {
			t.Errorf("WeightsOf(%s, %s) = %+v, want %+v", c.provider, c.label, got, c.want)
		}
	}
}

func TestBurn(t *testing.T) {
	u := Use{Input: 100, Output: 900, CacheRead: 10000, CacheWrite: 1000, CacheWrite1h: 400}
	if got := u.Burn(WeightsOf("claude", "")); got != 7150 {
		t.Errorf("burn = %d, want 7150", got)
	}
	// Counts that do not say how long writes are kept weigh them as 5m.
	tok := snapshot.Tokens{Input: 100, Output: 900, CacheRead: 10000, CacheWrite: 1000}
	if got := TokensBurn(tok, "claude", ""); got != 6850 {
		t.Errorf("tokens burn = %d, want 6850", got)
	}
	if got := (Use{CacheRead: 5}).Burn(WeightsOf("codex", "")); got != 1 {
		t.Errorf("0.5 rounds to %d, want 1", got)
	}
	if u.InOut() != 1000 || u.Fresh() != 2000 || u.Total() != 12000 || u.Tokens() != tok || FromTokens(tok).Add(Use{CacheWrite1h: 400}) != u {
		t.Errorf("counts of %+v are wrong", u)
	}
	if got := (Use{Input: 5, Output: 1}).Sub(Use{Input: 3, Output: 4}); got != (Use{Input: 2}) {
		t.Errorf("sub = %+v, want floored at zero", got)
	}
}

func TestUseJSON(t *testing.T) {
	for body, want := range map[string]Use{
		"[]":          {},
		"[5]":         {Input: 5},
		"[1,2,3,4,1]": {Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, CacheWrite1h: 1},
	} {
		var u Use
		if err := json.Unmarshal([]byte(body), &u); err != nil || u != want {
			t.Errorf("%s decodes to %+v, %v; want %+v", body, u, err, want)
		}
		if b, err := json.Marshal(u); err != nil || string(b) != body {
			t.Errorf("%+v encodes to %s, %v; want %s", u, b, err, body)
		}
	}
	var u Use
	if err := json.Unmarshal([]byte("[1,2,3,4,5,6]"), &u); err == nil {
		t.Error("six counts decode")
	}
	h := Hours{497000: {Input: 60, Output: 540}}
	b, err := json.Marshal(h)
	if err != nil || string(b) != `{"497000":[60,540]}` {
		t.Fatalf("hours encode to %s, %v", b, err)
	}
	var back Hours
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, h) {
		t.Fatalf("hours decode to %v, %v", back, err)
	}
}

func TestSpread(t *testing.T) {
	got := Spread(Use{Input: 10}, map[int64]int64{1: 1, 2: 1, 3: 1})
	if want := (Hours{1: {Input: 4}, 2: {Input: 3}, 3: {Input: 3}}); !reflect.DeepEqual(got, want) {
		t.Errorf("spread = %v, want %v", got, want)
	}
	got = Spread(Use{Input: 100, Output: 900, CacheRead: 50000, CacheWrite: 4000}, map[int64]int64{497000: 600, 497001: 400})
	want := Hours{497000: {Input: 60, Output: 540, CacheRead: 30000, CacheWrite: 2400}, 497001: {Input: 40, Output: 360, CacheRead: 20000, CacheWrite: 1600}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spread = %v, want %v", got, want)
	}
	if got := Spread(Use{Input: 10}, map[int64]int64{1: 0}); got != nil {
		t.Errorf("spread over nothing = %v, want nil", got)
	}
}

func TestScaleTo(t *testing.T) {
	cases := []struct {
		name  string
		h     Hours
		total Use
		want  Hours
	}{
		{"each kind keeps its shape; a missing kind follows all tokens",
			Hours{1: {Input: 1, Output: 3}, 2: {Input: 1, Output: 1}},
			Use{Input: 10, Output: 8, CacheRead: 6},
			Hours{1: {Input: 5, Output: 6, CacheRead: 4}, 2: {Input: 5, Output: 2, CacheRead: 2}}},
		{"remainders go to the largest, the earliest on a tie",
			Hours{1: {Input: 1}, 2: {Input: 1}, 3: {Input: 1}},
			Use{Input: 2},
			Hours{1: {Input: 1}, 2: {Input: 1}}},
		{"no hours: all at the fallback",
			Hours{},
			Use{Input: 7, CacheRead: 3},
			Hours{9: {Input: 7, CacheRead: 3}}},
		{"hour-long writes never exceed an hour's writes",
			Hours{1: {CacheWrite: 10, CacheWrite1h: 10}, 2: {CacheWrite: 10}},
			Use{CacheWrite: 10, CacheWrite1h: 8},
			Hours{1: {CacheWrite: 5, CacheWrite1h: 5}, 2: {CacheWrite: 5, CacheWrite1h: 3}}},
		{"hour-long writes without a shape follow the writes, clamped to them",
			Hours{1: {CacheWrite: 3}, 2: {CacheWrite: 1}},
			Use{CacheWrite: 4, CacheWrite1h: 9},
			Hours{1: {CacheWrite: 3, CacheWrite1h: 3}, 2: {CacheWrite: 1, CacheWrite1h: 1}}},
		{"a zero total empties",
			Hours{1: {Input: 4}},
			Use{},
			Hours{}},
		{"large counts do not overflow",
			Hours{1: {Input: 3e12}, 2: {Input: 1e12}},
			Use{Input: 4e12},
			Hours{1: {Input: 3e12}, 2: {Input: 1e12}}},
	}
	for _, c := range cases {
		h := c.h
		h.ScaleTo(c.total, 9)
		if !reflect.DeepEqual(h, c.want) {
			t.Errorf("%s: %v, want %v", c.name, h, c.want)
		}
		if got, want := h.Sum(), c.total; got != want && c.total.CacheWrite1h <= c.total.CacheWrite {
			t.Errorf("%s: sum %+v, want %+v", c.name, got, want)
		}
	}
	var none Hours
	none.ScaleTo(Use{Input: 1}, 4)
	if !reflect.DeepEqual(none, Hours{4: {Input: 1}}) {
		t.Errorf("nil hours scale to %v", none)
	}
}

func TestTakeNewest(t *testing.T) {
	h := Hours{1: {Input: 10, Output: 5}, 2: {Input: 3, Output: 5}, 3: {Input: 2}}
	taken := h.TakeNewest(Use{Input: 4, Output: 20})
	if want := (Hours{1: {Input: 10}, 2: {Input: 1}}); !reflect.DeepEqual(h, want) {
		t.Errorf("left %v, want %v", h, want)
	}
	if want := (Hours{2: {Input: 2, Output: 5}, 3: {Input: 2}, 1: {Output: 5}}); !reflect.DeepEqual(taken, want) {
		t.Errorf("took %v, want %v", taken, want)
	}
}

func TestHours(t *testing.T) {
	at := time.Date(2026, 9, 21, 10, 30, 0, 0, time.UTC)
	var h Hours
	h.Add(at, Use{Input: 1})
	h.Add(at.Add(time.Hour), Use{Output: 2})
	h.Add(time.Time{}, Use{Input: 5})
	h.Add(at, Use{})
	k := at.Unix() / 3600
	if want := (Hours{k: {Input: 1}, k + 1: {Output: 2}}); !reflect.DeepEqual(h, want) {
		t.Fatalf("hours = %v, want %v", h, want)
	}
	if got := AddHours(nil, h); !reflect.DeepEqual(got, h) {
		t.Errorf("added = %v", got)
	}
	if got := h.Since(k + 1); !reflect.DeepEqual(got, Hours{k + 1: {Output: 2}}) {
		t.Errorf("since = %v", got)
	}
	if got := h.Since(k + 2); got != nil {
		t.Errorf("since after the last = %v, want nil", got)
	}
	if got := h.Series(Use.InOut); !reflect.DeepEqual(got, map[int64]int64{k: 1, k + 1: 2}) {
		t.Errorf("series = %v", got)
	}
	c := h.Clone()
	c[k] = Use{}
	if h[k].Input != 1 || h.Sum() != (Use{Input: 1, Output: 2}) {
		t.Errorf("clone shares the map, or sum is wrong: %v", h)
	}
}
