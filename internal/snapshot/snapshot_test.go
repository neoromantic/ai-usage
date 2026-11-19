package snapshot

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

// golden is a hand-written body of testdata, as a collector sends it.
func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(b)
}

// Each older body of v1-cost.json is the golden the collectors of its
// releases read, byte for byte: WithoutCosts v0.7.0 to v0.9.x
// (v1-hours.json), WithoutHours v0.5.0 to v0.6.x (v1-cycles.json),
// WithoutCycles v0.4.0 (v1-extended.json), and Legacy those before v0.4.0
// (v1.json). Each drops what the one before it has and it does not.
func TestOlderWireFormats(t *testing.T) {
	d, err := Decode(golden(t, "v1-cost.json"))
	if err != nil {
		t.Fatal(err)
	}
	prev := d
	for _, c := range []struct {
		name   string
		body   Doc
		golden string
		drops  func(Doc) bool
	}{
		{"WithoutCosts", d.WithoutCosts(), "v1-hours.json", Doc.HasCosts},
		{"WithoutHours", d.WithoutHours(), "v1-cycles.json", Doc.HasHours},
		{"WithoutCycles", d.WithoutCycles(), "v1-extended.json", Doc.HasCycles},
		{"Legacy", d.Legacy(), "v1.json", Doc.Extended},
	} {
		if !c.drops(prev) || c.drops(c.body) {
			t.Errorf("%s keeps what it drops, or the body before it has none", c.name)
		}
		if b := encode(t, c.body); !bytes.Equal(b, golden(t, c.golden)) {
			t.Errorf("%s encodes to\n%s", c.name, b)
		}
		prev = c.body
	}
}

func TestExtended(t *testing.T) {
	at := t0.Add(-time.Hour)
	for name, c := range map[string]struct {
		edit func(*Doc)
		want bool
	}{
		"none":            {func(*Doc) {}, false},
		"days only":       {func(d *Doc) { d.Accounts[0].Days = []int64{5} }, false},
		"last_error_at":   {func(d *Doc) { d.LastError, d.LastErrorAt = sealed(8), &at }, true},
		"burn_days":       {func(d *Doc) { d.Accounts[0].BurnDays = []int64{0, 7} }, true},
		"recent burn":     {func(d *Doc) { d.Accounts[0].Recent = []Recent{{Window: "5h", Start: at, Tokens: 3, Burn: 9}} }, true},
		"recent, no burn": {func(d *Doc) { d.Accounts[0].Recent = []Recent{{Window: "5h", Start: at, Tokens: 3}} }, false},
		"cycles": {func(d *Doc) {
			d.Accounts[0].Cycles = []Cycle{{Window: "5h", ResetsAt: at, Percent: 90, LastReadAt: at.Add(-time.Hour)}}
		}, true},
		"hours":      {func(d *Doc) { d.Accounts[0].Hours = []int64{0, 4} }, true},
		"burn_hours": {func(d *Doc) { d.Accounts[0].BurnHours = []int64{9} }, true},
		// Every body without the hours is without the costs too.
		"costs": {func(d *Doc) { d.Costs = []Cost{{Provider: "claude", Label: sealed(8), Data: sealed(8), At: at}} }, false},
	} {
		d := validDoc()
		c.edit(&d)
		if got := d.Extended(); got != c.want {
			t.Errorf("%s: Extended = %v, want %v", name, got, c.want)
		}
	}
}

// The older bodies share no slice with the document they came from, so
// fitting or trimming one never changes the other.
func TestOlderBodiesShareNothing(t *testing.T) {
	at := t0.Add(-time.Hour)
	d := validDoc()
	d.LastError, d.LastErrorAt = sealed(8), &at
	d.Aliases = []Alias{{Provider: "claude", Label: sealed(8), At: at}}
	d.Costs = []Cost{{Provider: "claude", Label: sealed(8), Data: sealed(8), At: at}}
	d.Pooled = []string{"claude"}
	a := &d.Accounts[0]
	a.Days, a.BurnDays, a.Hours, a.BurnHours = []int64{1, 2}, []int64{10, 20}, []int64{1}, []int64{5}
	a.Recent = []Recent{{Window: "5h", Start: at, Tokens: 3, Burn: 9}}
	a.Linked = []Linked{{Provider: "hermes", Label: sealed(8)}}
	a.Cycles = []Cycle{{Window: "5h", ResetsAt: at, Percent: 90, LastReadAt: at.Add(-time.Hour)}}
	before := encode(t, d)
	for name, older := range map[string]func(Doc) Doc{
		"WithoutManyProjects": Doc.WithoutManyProjects, "WithoutPooled": Doc.WithoutPooled, "WithoutCosts": Doc.WithoutCosts,
		"WithoutHours": Doc.WithoutHours, "WithoutCycles": Doc.WithoutCycles, "Legacy": Doc.Legacy,
	} {
		o := older(d)
		for i := range o.Pooled {
			o.Pooled[i] = "codex"
		}
		for i := range o.Costs {
			o.Costs[i].Data = sealed(4)
		}
		for i := range o.Aliases {
			o.Aliases[i].Name = sealed(4)
		}
		for i := range o.Sources {
			o.Sources[i].Status = "error"
		}
		for i := range o.Accounts {
			a := &o.Accounts[i]
			a.Plan = "changed"
			for j := range a.Windows {
				a.Windows[j].Percent = 99
			}
			for j := range a.Projects {
				a.Projects[j].Sessions = 99
			}
			for j := range a.Linked {
				a.Linked[j].Sessions = 99
			}
			for j := range a.Recent {
				a.Recent[j].Tokens = 99
			}
			for j := range a.Cycles {
				a.Cycles[j].Percent = 99
			}
			for _, series := range [][]int64{a.Days, a.BurnDays, a.Hours, a.BurnHours} {
				for j := range series {
					series[j] = 99
				}
			}
		}
		if after := encode(t, d); !bytes.Equal(after, before) {
			t.Fatalf("changing %s changed the document:\n%s\n%s", name, before, after)
		}
	}
}
