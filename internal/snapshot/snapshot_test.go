package snapshot

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

// Legacy of the extended golden is the golden a collector before v0.4.0
// reads, byte for byte.
func TestLegacyIsTheOlderWireFormat(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return bytes.TrimSpace(b)
	}
	d, err := Decode(read("v1-extended.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Extended() {
		t.Fatal("the extended golden is not Extended")
	}
	legacy := d.Legacy()
	if legacy.Extended() {
		t.Fatal("Legacy is Extended")
	}
	if b := encode(t, legacy); !bytes.Equal(b, read("v1.json")) {
		t.Fatalf("Legacy encodes to\n%s", b)
	}
	older, err := Decode(read("v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if older.Extended() || !reflect.DeepEqual(older.Legacy(), older) {
		t.Fatal("Legacy changed a document without the newer fields")
	}
}

// WithoutCycles of the cycles golden is the extended golden a collector from
// v0.4.0 on, and before v0.5.0, reads, byte for byte, and its Legacy the one
// before v0.4.0.
func TestWithoutCyclesIsTheOlderWireFormat(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return bytes.TrimSpace(b)
	}
	d, err := Decode(read("v1-cycles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.HasCycles() || !d.Extended() {
		t.Fatal("the cycles golden has no cycles")
	}
	without := d.WithoutCycles()
	if without.HasCycles() || !without.Extended() {
		t.Fatal("WithoutCycles has cycles, or lost the v0.4.0 fields")
	}
	if b := encode(t, without); !bytes.Equal(b, read("v1-extended.json")) {
		t.Fatalf("WithoutCycles encodes to\n%s", b)
	}
	if b := encode(t, d.Legacy()); !bytes.Equal(b, read("v1.json")) {
		t.Fatalf("Legacy encodes to\n%s", b)
	}
	extended, err := Decode(read("v1-extended.json"))
	if err != nil {
		t.Fatal(err)
	}
	if extended.HasCycles() || !reflect.DeepEqual(extended.WithoutCycles(), extended) {
		t.Fatal("WithoutCycles changed a document without cycles")
	}
}

// v1-hours.json is v1-cycles.json with the hours v0.7.0 added, and
// WithoutHours is the body v0.5.0 to v0.6.2 read.
func TestWithoutHoursIsTheOlderWireFormat(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return bytes.TrimSpace(b)
	}
	d, err := Decode(read("v1-hours.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.HasHours() || !d.HasCycles() || !d.Extended() {
		t.Fatal("the hours golden has no hours or cycles")
	}
	if b := encode(t, d); !bytes.Equal(b, read("v1-hours.json")) {
		t.Fatalf("the hours golden encodes to\n%s", b)
	}
	if b := encode(t, d.WithoutHours()); !bytes.Equal(b, read("v1-cycles.json")) {
		t.Fatalf("WithoutHours encodes to\n%s", b)
	}
	if b := encode(t, d.WithoutCycles()); !bytes.Equal(b, read("v1-extended.json")) {
		t.Fatalf("WithoutCycles encodes to\n%s", b)
	}
}

// v1-cost.json is v1-hours.json with the costs v0.10.0 added, and
// WithoutCosts is the body v0.7.0 to v0.9.x read. Every older body is
// without them too.
func TestWithoutCostsIsTheOlderWireFormat(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return bytes.TrimSpace(b)
	}
	d, err := Decode(read("v1-cost.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.HasCosts() || !d.HasHours() {
		t.Fatal("the costs golden has no costs or hours")
	}
	if b := encode(t, d); !bytes.Equal(b, read("v1-cost.json")) {
		t.Fatalf("the costs golden encodes to\n%s", b)
	}
	without := d.WithoutCosts()
	if without.HasCosts() || !without.HasHours() {
		t.Fatal("WithoutCosts has costs, or lost the hours")
	}
	if b := encode(t, without); !bytes.Equal(b, read("v1-hours.json")) {
		t.Fatalf("WithoutCosts encodes to\n%s", b)
	}
	for name, body := range map[string]Doc{"WithoutHours": d.WithoutHours(), "WithoutCycles": d.WithoutCycles(), "Legacy": d.Legacy()} {
		if body.HasCosts() {
			t.Fatalf("%s has costs", name)
		}
	}
	// The bodies share no slice with d.
	without.Accounts[0].Hours[0] = 99
	without.Sources[0].Status = "error"
	if d.Accounts[0].Hours[0] == 99 || d.Sources[0].Status == "error" {
		t.Fatal("WithoutCosts shares a slice with the document")
	}
	if d.WithoutCosts().Extended() != d.Extended() || !d.Extended() {
		t.Fatal("the costs count as a field v0.4.0 added")
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
	} {
		d := validDoc()
		c.edit(&d)
		if got := d.Extended(); got != c.want {
			t.Errorf("%s: Extended = %v, want %v", name, got, c.want)
		}
		if l := d.Legacy(); l.Extended() || l.Validate() != nil {
			t.Errorf("%s: Legacy is extended or invalid", name)
		}
		newer := name == "cycles" || strings.HasSuffix(name, "hours")
		if w := d.WithoutCycles(); w.HasCycles() || w.HasHours() || w.Validate() != nil || w.Extended() != (c.want && !newer) {
			t.Errorf("%s: WithoutCycles has cycles or hours, is invalid, or lost a v0.4.0 field", name)
		}
		if w := d.WithoutHours(); w.HasHours() || w.Validate() != nil || w.HasCycles() != d.HasCycles() {
			t.Errorf("%s: WithoutHours has hours, is invalid, or changed the cycles", name)
		}
	}
}

// Legacy shares no slice with the document it came from, so fitting or
// trimming one never changes the other.
func TestLegacySharesNothing(t *testing.T) {
	at := t0.Add(-time.Hour)
	d := validDoc()
	d.LastError, d.LastErrorAt = sealed(8), &at
	d.Aliases = []Alias{{Provider: "claude", Label: sealed(8), At: at}}
	a := &d.Accounts[0]
	a.Days, a.BurnDays = []int64{1, 2}, []int64{10, 20}
	a.Recent = []Recent{{Window: "5h", Start: at, Tokens: 3, Burn: 9}}
	a.Linked = []Linked{{Provider: "hermes", Label: sealed(8)}}
	a.Cycles = []Cycle{{Window: "5h", ResetsAt: at, Percent: 90, LastReadAt: at.Add(-time.Hour)}}
	before := encode(t, d)

	w := d.WithoutCycles()
	w.Accounts[0].BurnDays[0] = 99
	w.Accounts[0].Recent[0].Burn = 99
	w.Accounts[0].Days[0] = 99
	w.Accounts[0].Windows[0].Percent = 99
	if after := encode(t, d); !bytes.Equal(after, before) {
		t.Fatalf("changing WithoutCycles changed the document:\n%s\n%s", before, after)
	}
	if len(d.Accounts[0].Cycles) != 1 {
		t.Fatal("WithoutCycles cleared the document's own cycles")
	}

	d.Accounts[0].Hours, d.Accounts[0].BurnHours = []int64{1}, []int64{5}
	before = encode(t, d)
	h := d.WithoutHours()
	h.Accounts[0].Cycles[0].Percent = 99
	h.Accounts[0].Days[0] = 99
	if after := encode(t, d); !bytes.Equal(after, before) || len(d.Accounts[0].Hours) != 1 {
		t.Fatalf("changing WithoutHours changed the document:\n%s\n%s", before, after)
	}
	d.Accounts[0].Hours, d.Accounts[0].BurnHours = nil, nil
	before = encode(t, d)

	l := d.Legacy()
	l.Accounts[0].Days[0] = 99
	l.Accounts[0].Recent[0].Tokens = 99
	l.Accounts[0].Windows[0].Percent = 99
	l.Accounts[0].Projects[0].Sessions = 99
	l.Accounts[0].Linked[0].Sessions = 99
	l.Accounts[0].Plan = "changed"
	l.Accounts = append(l.Accounts, l.Accounts[0])
	l.Sources[0].Status = "error"
	l.Aliases[0].Name = sealed(4)
	if after := encode(t, d); !bytes.Equal(after, before) {
		t.Fatalf("changing Legacy changed the document:\n%s\n%s", before, after)
	}
	if d.Accounts[0].Recent[0].Burn != 9 || d.LastErrorAt == nil || len(d.Accounts[0].BurnDays) != 2 {
		t.Fatal("Legacy cleared the document's own fields")
	}
	// Empty arrays stay arrays, which Validate requires.
	d.Accounts[0].Projects = []Project{}
	if l := d.Legacy(); l.Accounts[0].Projects == nil {
		t.Fatal("Legacy made an empty array null")
	}
}
