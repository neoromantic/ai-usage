package snapshot

import (
	"bytes"
	"encoding/json"
	"math"
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

// TestWireFormat pins the snapshot bytes deployed collectors sign. The relay
// takes a body only when it is exactly the encoding of what Decode reads from
// it, so a change that fails this test makes it refuse their snapshots. The
// golden uses every member and is written by hand: never regenerate it.
func TestWireFormat(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden = bytes.TrimSpace(golden)
	d, err := Decode(golden)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if b := encode(t, d); !bytes.Equal(b, golden) {
		t.Fatalf("the wire format changed:\n%s\n%s", golden, b)
	}
}

func TestDecodeTrailingAndSize(t *testing.T) {
	body := encode(t, validDoc())
	cases := []struct {
		name string
		body []byte
		ok   bool
	}{
		{"trailing whitespace", append(bytes.Clone(body), " \n\t"...), true},
		{"second value", append(bytes.Clone(body), "{}"...), false},
		{"trailing garbage", append(bytes.Clone(body), " x"...), false},
		{"trailing brace", append(bytes.Clone(body), '}'), false},
		{"exactly the limit", append(bytes.Clone(body), bytes.Repeat([]byte(" "), MaxBytes-len(body))...), true},
		{"one byte over", append(bytes.Clone(body), bytes.Repeat([]byte(" "), MaxBytes-len(body)+1)...), false},
		{"empty", nil, false},
		{"null", []byte("null"), false},
		{"array", []byte("[]"), false},
		{"truncated", body[:len(body)-1], false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode(c.body)
			if (err == nil) != c.ok {
				t.Fatalf("Decode err = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	body := string(encode(t, validDoc()))
	cases := map[string]string{
		"top level":                     strings.Replace(body, `{"v":1,`, `{"v":1,"note":"hi",`, 1),
		"account":                       strings.Replace(body, `"provider":"claude","label"`, `"provider":"claude","email":"a@b.c","label"`, 1),
		"window":                        strings.Replace(body, `"name":"5h",`, `"name":"5h","extra":[1],`, 1),
		"project":                       strings.Replace(body, `"path":`, `"file":"x","path":`, 1),
		"source":                        strings.Replace(body, `"status":"ok"`, `"status":"ok","blob":"x"`, 1),
		"tokens":                        strings.Replace(body, `"cache_write":300}`, `"cache_write":300,"cost":1}`, 1),
		"wrong type":                    strings.Replace(body, `"sessions":3`, `"sessions":"3"`, 1),
		"float count":                   strings.Replace(body, `"sessions":3`, `"sessions":3.5`, 1),
		"nested object in string field": strings.Replace(body, `"plan":"max 20x"`, `"plan":{"a":1}`, 1),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if b == body {
				t.Fatal("test did not change the body")
			}
			if _, err := Decode([]byte(b)); err == nil {
				t.Fatal("Decode accepted it")
			}
		})
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Doc)
		ok   bool
	}{
		{"valid", func(*Doc) {}, true},
		{"version 0", func(d *Doc) { d.V = 0 }, false},
		{"version 2", func(d *Doc) { d.V = 2 }, false},

		// The shapes themselves are in TestValidTeamAndDevice.
		{"team not a fingerprint", func(d *Doc) { d.Team = d.Team[:31] }, false},
		{"device id bad", func(d *Doc) { d.Device = "abcd123" }, false},

		{"device_label missing", func(d *Doc) { d.DeviceLabel = "" }, false},
		{"os_user missing", func(d *Doc) { d.OSUser = "" }, false},
		{"sealed at limit", func(d *Doc) { d.DeviceLabel = sealed(MaxSealed) }, true},
		{"sealed over limit", func(d *Doc) { d.DeviceLabel = sealed(MaxSealed + 1) }, false},
		{"sealed std base64", func(d *Doc) { d.OSUser = "ab+/" }, false},
		{"sealed plain text", func(d *Doc) { d.OSUser = "ann@example.com" }, false},
		{"sealed with padding", func(d *Doc) { d.OSUser = "abc=" }, false},
		{"sealed non-ascii", func(d *Doc) { d.OSUser = "анна" }, false},
		{"last_error optional", func(d *Doc) { d.LastError = "" }, true},
		{"last_error sealed", func(d *Doc) { d.LastError = sealed(30) }, true},
		{"last_error plain text", func(d *Doc) { d.LastError = "exit status 1" }, false},

		{"collector_version missing", func(d *Doc) { d.CollectorVersion = "" }, false},
		{"plain at limit", func(d *Doc) { d.CollectorVersion = strings.Repeat("v", MaxPlain) }, true},
		{"plain over limit", func(d *Doc) { d.CollectorVersion = strings.Repeat("v", MaxPlain+1) }, false},
		{"plain allowed punctuation", func(d *Doc) { d.CollectorVersion = "v1.2.3-rc_1+dev (x/y): z" }, true},
		{"plain angle bracket", func(d *Doc) { d.CollectorVersion = "<script>" }, false},
		{"plain quote", func(d *Doc) { d.CollectorVersion = `v1"` }, false},
		{"plain newline", func(d *Doc) { d.CollectorVersion = "v1\nv2" }, false},

		{"collected_at missing", func(d *Doc) { d.CollectedAt = time.Time{} }, false},
		{"last_success_at missing", func(d *Doc) { d.LastSuccessAt = time.Time{} }, true},

		{"accounts null", func(d *Doc) { d.Accounts = nil }, false},
		{"sources null", func(d *Doc) { d.Sources = nil }, false},
		{"accounts empty", func(d *Doc) { d.Accounts = []Account{} }, true},
		{"sources empty", func(d *Doc) { d.Sources = []Source{} }, true},
		{"too many accounts", func(d *Doc) { d.Accounts = repeat(d.Accounts[0], MaxAccounts+1) }, false},
		{"most accounts", func(d *Doc) { d.Accounts = repeat(d.Accounts[0], MaxAccounts) }, true},
		{"too many sources", func(d *Doc) { d.Sources = repeat(d.Sources[0], MaxSources+1) }, false},

		{"source unknown provider", func(d *Doc) { d.Sources[0].Provider = "gemini" }, false},
		{"source unknown status", func(d *Doc) { d.Sources[0].Status = "fine" }, false},
		{"source statuses", func(d *Doc) {
			d.Sources = []Source{{"claude", "ok", ""}, {"codex", "partial", ""}, {"grok", "error", ""}, {"hermes", "skipped", ""}}
		}, true},
		{"source error plain text", func(d *Doc) { d.Sources[1].Error = "open /Users/me: denied" }, false},

		{"account unknown provider", func(d *Doc) { d.Accounts[0].Provider = "Claude" }, false},
		{"account label missing", func(d *Doc) { d.Accounts[0].Label = "" }, false},
		{"account label plain email", func(d *Doc) { d.Accounts[0].Label = "me@example.com" }, false},
		{"plan optional", func(d *Doc) { d.Accounts[0].Plan = "" }, true},
		{"plan bad", func(d *Doc) { d.Accounts[0].Plan = "max\t20x" }, false},
		{"windows null", func(d *Doc) { d.Accounts[0].Windows = nil }, false},
		{"projects null", func(d *Doc) { d.Accounts[0].Projects = nil }, false},
		{"windows without quota_at", func(d *Doc) { d.Accounts[0].QuotaAt = nil }, false},
		{"no windows, no quota_at", func(d *Doc) { d.Accounts[0].QuotaAt, d.Accounts[0].Windows = nil, []Window{} }, true},
		{"too many windows", func(d *Doc) { d.Accounts[0].Windows = repeat(d.Accounts[0].Windows[0], MaxWindows+1) }, false},
		{"window name missing", func(d *Doc) { d.Accounts[0].Windows[0].Name = "" }, false},
		{"window name bad", func(d *Doc) { d.Accounts[0].Windows[0].Name = "5h;drop" }, false},
		{"percent 0", func(d *Doc) { d.Accounts[0].Windows[0].Percent = 0 }, true},
		{"percent 1000", func(d *Doc) { d.Accounts[0].Windows[0].Percent = 1000 }, true},
		{"percent negative", func(d *Doc) { d.Accounts[0].Windows[0].Percent = -0.01 }, false},
		{"percent over", func(d *Doc) { d.Accounts[0].Windows[0].Percent = 1000.5 }, false},
		{"percent NaN", func(d *Doc) { d.Accounts[0].Windows[0].Percent = math.NaN() }, false},
		{"minutes 0", func(d *Doc) { d.Accounts[0].Windows[0].Minutes = 0 }, true},
		{"minutes max", func(d *Doc) { d.Accounts[0].Windows[0].Minutes = MaxWindowMinutes }, true},
		{"minutes negative", func(d *Doc) { d.Accounts[0].Windows[0].Minutes = -1 }, false},
		{"minutes over", func(d *Doc) { d.Accounts[0].Windows[0].Minutes = MaxWindowMinutes + 1 }, false},

		{"sessions max", func(d *Doc) { d.Accounts[0].Sessions = MaxSessionCount }, true},
		{"sessions negative", func(d *Doc) { d.Accounts[0].Sessions = -1 }, false},
		{"sessions over", func(d *Doc) { d.Accounts[0].Sessions = MaxSessionCount + 1 }, false},
		{"tokens max", func(d *Doc) { d.Accounts[0].Tokens.CacheRead = MaxTokenCount }, true},
		{"input negative", func(d *Doc) { d.Accounts[0].Tokens.Input = -1 }, false},
		{"output over", func(d *Doc) { d.Accounts[0].Tokens.Output = MaxTokenCount + 1 }, false},
		{"cache read negative", func(d *Doc) { d.Accounts[0].Tokens.CacheRead = -1 }, false},
		{"cache write over", func(d *Doc) { d.Accounts[0].Tokens.CacheWrite = MaxTokenCount + 1 }, false},

		{"too many projects", func(d *Doc) { d.Accounts[0].Projects = repeat(d.Accounts[0].Projects[0], MaxProjects+1) }, false},
		{"most projects", func(d *Doc) { d.Accounts[0].Projects = repeat(d.Accounts[0].Projects[0], MaxProjects) }, true},
		{"project path missing", func(d *Doc) { d.Accounts[0].Projects[0].Path = "" }, false},
		{"project path plain", func(d *Doc) { d.Accounts[0].Projects[0].Path = "/Users/me/src" }, false},
		{"project sessions negative", func(d *Doc) { d.Accounts[0].Projects[0].Sessions = -1 }, false},
		{"project tokens negative", func(d *Doc) { d.Accounts[0].Projects[0].Tokens.Input = -5 }, false},

		{"quota_from another provider", func(d *Doc) { d.Accounts[0].Provider, d.Accounts[0].QuotaFrom = "hermes", "codex" }, true},
		{"quota_from grok", func(d *Doc) { d.Accounts[0].Provider, d.Accounts[0].QuotaFrom = "hermes", "grok" }, true},
		{"quota_from itself", func(d *Doc) { d.Accounts[0].QuotaFrom = "claude" }, false},
		{"quota_from unknown provider", func(d *Doc) { d.Accounts[0].QuotaFrom = "openai-codex" }, false},
		{"linked", func(d *Doc) {
			d.Accounts[0].Linked = []Linked{{Provider: "hermes", Label: sealed(40), Sessions: 3, Tokens: Tokens{Input: 5}}}
		}, true},
		{"linked itself", func(d *Doc) { d.Accounts[0].Linked = []Linked{{Provider: "claude", Label: sealed(40)}} }, false},
		{"linked unknown provider", func(d *Doc) { d.Accounts[0].Linked = []Linked{{Provider: "openai-codex", Label: sealed(40)}} }, false},
		{"linked plain label", func(d *Doc) { d.Accounts[0].Linked = []Linked{{Provider: "hermes", Label: "open ai"}} }, false},
		{"linked negative tokens", func(d *Doc) {
			d.Accounts[0].Linked = []Linked{{Provider: "hermes", Label: sealed(40), Tokens: Tokens{Output: -1}}}
		}, false},
		{"too many linked", func(d *Doc) {
			for range MaxLinked + 1 {
				d.Accounts[0].Linked = append(d.Accounts[0].Linked, Linked{Provider: "hermes", Label: sealed(40)})
			}
		}, false},
		{"quota_from without windows", func(d *Doc) {
			d.Accounts[0].Provider, d.Accounts[0].QuotaFrom = "hermes", "codex"
			d.Accounts[0].QuotaAt, d.Accounts[0].Windows = nil, []Window{}
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDoc()
			c.edit(&d)
			err := d.Validate(t0)
			if (err == nil) != c.ok {
				t.Fatalf("Validate err = %v, want ok=%v", err, c.ok)
			}
			// Decode must agree with Validate for anything JSON can carry.
			if b, merr := json.Marshal(d); merr == nil && len(b) <= MaxBytes {
				if _, derr := Decode(b); (derr == nil) != c.ok {
					t.Fatalf("Decode err = %v, want ok=%v", derr, c.ok)
				}
			}
		})
	}
}

func repeat[T any](v T, n int) []T {
	out := make([]T, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestValidateFutureCollectedAt(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		ok   bool
	}{
		{"now", t0, true},
		{"past", t0.Add(-30 * 24 * time.Hour), true},
		{"skew allowed", t0.Add(10 * time.Minute), true},
		{"too far ahead", t0.Add(10*time.Minute + time.Second), false},
		{"other zone, same instant", t0.Add(10 * time.Minute).In(time.FixedZone("x", 5*3600)), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDoc()
			d.CollectedAt = c.at
			if err := d.Validate(t0); (err == nil) != c.ok {
				t.Fatalf("Validate(now) err = %v, want ok=%v", err, c.ok)
			}
			// A zero now skips the check; Decode has no clock.
			if err := d.Validate(time.Time{}); err != nil {
				t.Fatalf("Validate(zero) err = %v", err)
			}
			if _, err := Decode(encode(t, d)); err != nil {
				t.Fatalf("Decode err = %v", err)
			}
		})
	}
}

func TestValidTeamAndDevice(t *testing.T) {
	for s, want := range map[string]bool{
		strings.Repeat("a", 32):            true,
		"abcdefghijklmnopqrstuvwxyz234567": true,
		strings.Repeat("a", 31):            false,
		strings.Repeat("a", 33):            false,
		strings.Repeat("A", 32):            false,
		"":                                 false,
		strings.Repeat("a", 31) + "=":      false,
		strings.Repeat("a", 31) + "0":      false,
		strings.Repeat("a", 31) + "1":      false,
		strings.Repeat("a", 31) + "8":      false,
		strings.Repeat("a", 31) + "\n":     false,
	} {
		if got := ValidTeam(s); got != want {
			t.Errorf("ValidTeam(%q) = %v, want %v", s, got, want)
		}
	}
	for s, want := range map[string]bool{
		"0123abcd":              true,
		"mac-studio-1":          true,
		"a-------":              true,
		strings.Repeat("a", 64): true,
		strings.Repeat("a", 65): false,
		"mac":                   false,
		"-mac-studio":           false,
		"MAC-STUDIO":            false,
		"mac.studio":            false,
		"abcd_1234":             false,
		"mac-studio\n":          false,
		"":                      false,
		"../../etc/passwd":      false,
	} {
		if got := ValidDevice(s); got != want {
			t.Errorf("ValidDevice(%q) = %v, want %v", s, got, want)
		}
	}
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
		if err := checkPlain("name", DurationName(minutes), true); err != nil {
			t.Errorf("DurationName(%d) is not a plain label: %v", minutes, err)
		}
	}
}

func TestPlainLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"Max 20x", "Max 20x"},
		{"v1.2.3-rc.1+dev (x/y): z", "v1.2.3-rc.1+dev (x/y): z"},
		{"a,b;c", "a-b-c"},
		{"line\nbreak\t", "line-break-"},
		{"Тариф", "-----"},
		{"bad\xffutf8", "bad-utf8"},
		{strings.Repeat("x", 100), strings.Repeat("x", MaxPlain)},
		{strings.Repeat("é", 100), strings.Repeat("-", MaxPlain)},
	}
	for _, c := range cases {
		got := PlainLabel(c.in)
		if got != c.want {
			t.Errorf("PlainLabel(%q) = %q, want %q", c.in, got, c.want)
		}
		if err := checkPlain("label", got, false); err != nil {
			t.Errorf("PlainLabel(%q) = %q, which Validate rejects: %v", c.in, got, err)
		}
	}
}

func FuzzPlainLabel(f *testing.F) {
	for _, s := range []string{"", "Max 20x", "Тариф", "\xff\xfe", strings.Repeat("ab<", 30)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if err := checkPlain("label", PlainLabel(s), false); err != nil {
			t.Fatalf("PlainLabel(%q) = %q: %v", s, PlainLabel(s), err)
		}
	})
}

// FuzzDecode checks that Decode never panics and that what it accepts is
// within the limits.
func FuzzDecode(f *testing.F) {
	body, _ := json.Marshal(validDoc())
	f.Add(body)
	f.Add([]byte(`{"v":1}`))
	f.Add([]byte(`{"v":1,"V":2}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := Decode(b)
		if err != nil {
			return
		}
		if len(b) > MaxBytes {
			t.Fatalf("accepted %d bytes", len(b))
		}
		if err := d.Validate(time.Time{}); err != nil {
			t.Fatalf("accepted a document Validate rejects: %v", err)
		}
	})
}

func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"evil\nFAKE LINE\x1b[2J": "evil FAKE LINE [2J",
		"a​b‮c":                  "abc",
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
