package snapshot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// sealed stands in for team-sealed text; the snapshot only sees its charset.
func sealed(n int) string { return strings.Repeat("Ab-_", n/4+1)[:n] }

func validDoc() Doc {
	quotaAt := t0.Add(-time.Minute)
	resets := t0.Add(3 * time.Hour)
	active := t0.Add(-5 * time.Minute)
	tok := Tokens{Input: 100, Output: 20, CacheRead: 5000, CacheWrite: 300}
	return Doc{
		V:                Version,
		Team:             strings.Repeat("a2", 16),
		Device:           "mac-0123abcd",
		DeviceLabel:      sealed(40),
		OSUser:           sealed(39),
		CollectorVersion: "v1.2.3",
		CollectedAt:      t0,
		LastSuccessAt:    t0.Add(-15 * time.Minute),
		Accounts: []Account{{
			Provider:     "claude",
			Label:        sealed(60),
			Current:      true,
			Plan:         "max 20x",
			QuotaAt:      &quotaAt,
			Windows:      []Window{{Name: "5h", Percent: 42.5, ResetsAt: &resets, Minutes: 300}},
			Sessions:     3,
			Tokens:       tok,
			LastActiveAt: &active,
			Projects:     []Project{{Path: sealed(80), Sessions: 3, Tokens: tok}},
		}},
		Sources: []Source{{Provider: "claude", Status: "ok"}, {Provider: "codex", Status: "error", Error: sealed(50)}},
	}
}

func encode(t *testing.T, d Doc) []byte {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTokens(t *testing.T) {
	a := Tokens{Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40}
	if got := a.Add(Tokens{1, 2, 3, 4}); got != (Tokens{11, 22, 33, 44}) {
		t.Errorf("Add = %+v", got)
	}
	if a.Total() != 100 || (Tokens{}).Total() != 0 {
		t.Errorf("Total = %d", a.Total())
	}
	if a.Zero() || !(Tokens{}).Zero() {
		t.Error("Zero is wrong")
	}

	cases := []struct {
		name      string
		now, prev Tokens
		want      Tokens
	}{
		{"from nothing", a, Tokens{}, a},
		{"no change", a, a, Tokens{}},
		{"grew", Tokens{15, 25, 35, 45}, a, Tokens{5, 5, 5, 5}},
		{"counter went down", Tokens{5, 5, 5, 5}, a, Tokens{}},
		{"mixed", Tokens{15, 5, 30, 41}, a, Tokens{5, 0, 0, 1}},
	}
	for _, c := range cases {
		if got := c.now.Growth(c.prev); got != c.want {
			t.Errorf("%s: Growth = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDurationName(t *testing.T) {
	for minutes, want := range map[int]string{
		0:     "window",
		90:    "90m",
		60:    "1h",
		300:   "5h",
		1440:  "1d",
		10080: "7d",
		1500:  "25h",
		1441:  "1441m",
	} {
		if got := DurationName(minutes); got != want {
			t.Errorf("DurationName(%d) = %q, want %q", minutes, got, want)
		}
		if err := plainText.check("name", DurationName(minutes), true); err != nil {
			t.Errorf("DurationName(%d) is not a plain label: %v", minutes, err)
		}
	}
}

func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"evil\nFAKE LINE\x1b[2J": "evil FAKE LINE [2J",
		"a\u200bb\u202ec":        "abc",
		"Build bot · ℹ":          "Build bot · ℹ",
		// Joiners are part of names and emoji.
		"\u0644\u067e\u200c\u062a\u0627\u067e":    "\u0644\u067e\u200c\u062a\u0627\u067e",
		"Acme\u200dCo \U0001f469\u200d\U0001f4bb": "Acme\u200dCo \U0001f469\u200d\U0001f4bb",
		"\u2066isolated\u2069 \u200fmark\ufeff":   "isolated mark",
	} {
		if got := Printable(in); got != want {
			t.Errorf("Printable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWindowLengthAndStart(t *testing.T) {
	reset := time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		w    Window
		want time.Duration
	}{
		{Window{Name: "7d", Minutes: 10080}, 7 * 24 * time.Hour},
		{Window{Name: "5h"}, 5 * time.Hour},
		{Window{Name: "7d Opus"}, 7 * 24 * time.Hour},
		{Window{Name: "90m"}, 90 * time.Minute},
		{Window{Name: "month credits"}, 0},
		{Window{Name: "7days"}, 0},
		{Window{Name: "window"}, 0},
	} {
		if got := c.w.Length(); got != c.want {
			t.Errorf("%+v: length %v, want %v", c.w, got, c.want)
		}
	}
	start, ok := Window{Name: "7d", ResetsAt: &reset}.Start()
	if !ok || !start.Equal(reset.Add(-7*24*time.Hour)) {
		t.Errorf("start %v %v", start, ok)
	}
	if _, ok := (Window{Name: "7d"}).Start(); ok {
		t.Error("a window with no reset time has a start")
	}
}
