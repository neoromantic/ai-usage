// Package snapshot is the document one device publishes to the relay.
//
// The relay must be able to see that a body is a usage snapshot, so the shape
// is fixed and small: counts, percents, timestamps, and short strings. Strings
// that name a person, a machine, or a path are sealed with the team key before
// they leave the device. Numbers, provider names, and window names stay plain.
package snapshot

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Version is the wire format. The relay rejects any other value.
const Version = 1

// Limits. The relay enforces all of them. The collector trims to fit.
const (
	MaxBytes          = 64 << 10
	MaxAccounts       = 24
	MaxWindows        = 8
	MaxProjects       = 12
	MaxLinked         = 4
	MaxSources        = 12
	MaxDays           = 90
	MaxHours          = 48
	MaxAliases        = 48
	MaxCosts          = 24
	MaxCycles         = 64
	MaxSealed         = 512
	MaxPlain          = 40
	MaxTokenCount     = int64(1) << 50
	MaxSessionCount   = 1 << 24
	MaxWindowMinutes  = 60 * 24 * 62
	futureSkewAllowed = 10 * time.Minute
	// lastErrorSkew is how far last_error_at may be past collected_at.
	lastErrorSkew = 5 * time.Minute
)

// Providers the collector knows, in display order. The relay rejects any
// other name.
var Providers = []string{"claude", "codex", "grok", "hermes"}

// Doc is one device's snapshot. Sealed fields hold team-encrypted text.
type Doc struct {
	V                int       `json:"v"`
	Team             string    `json:"team"`
	Device           string    `json:"device"`
	DeviceLabel      string    `json:"device_label"`
	OSUser           string    `json:"os_user"`
	CollectorVersion string    `json:"collector_version"`
	CollectedAt      time.Time `json:"collected_at"`
	LastSuccessAt    time.Time `json:"last_success_at"`
	LastError        string    `json:"last_error,omitempty"`
	// LastErrorAt is when the device last had last_error, never after
	// collected_at. A collector before v0.4.0 neither sends nor reads it.
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	Accounts    []Account  `json:"accounts"`
	Sources     []Source   `json:"sources"`
	// Aliases are the short names this device gave accounts with
	// `ai-usage alias`. The newest one for an account, from any device in
	// the team, names it everywhere.
	Aliases []Alias `json:"aliases,omitempty"`
	// Costs are what people typed on this device of the cost of
	// subscriptions, such as a price or the day one charges. A collector
	// before v0.10.0 neither sends nor reads them.
	Costs []Cost `json:"costs,omitempty"`
}

// Extended reports whether d carries any field a collector before v0.4.0
// does not know, and so would drop d as undecodable: those v0.4.0 added,
// the cycles v0.5.0 added, and the hours v0.7.0 added. The costs v0.10.0
// added are not counted: every body without the hours is without them.
func (d Doc) Extended() bool {
	if d.LastErrorAt != nil || d.HasCycles() || d.HasHours() {
		return true
	}
	for _, a := range d.Accounts {
		if len(a.BurnDays) > 0 || slices.ContainsFunc(a.Recent, func(r Recent) bool { return r.Burn != 0 }) {
			return true
		}
	}
	return false
}

// HasCycles reports whether d carries quota cycles, which a collector before
// v0.5.0 does not know, and so would drop d as undecodable.
func (d Doc) HasCycles() bool {
	return slices.ContainsFunc(d.Accounts, func(a Account) bool { return len(a.Cycles) > 0 })
}

// HasHours reports whether d carries hours, which a collector before v0.7.0
// does not know, and so would drop d as undecodable.
func (d Doc) HasHours() bool {
	return slices.ContainsFunc(d.Accounts, func(a Account) bool { return len(a.Hours) > 0 || len(a.BurnHours) > 0 })
}

// HasCosts reports whether d carries costs, which a collector before
// v0.10.0 does not know, and so would drop d as undecodable.
func (d Doc) HasCosts() bool { return len(d.Costs) > 0 }

// Legacy is d without the fields a collector before v0.4.0 does not know:
// the body such a collector decodes. It shares no slice with d, so either
// can be changed without the other.
func (d Doc) Legacy() Doc {
	d = d.WithoutCycles()
	d.LastErrorAt = nil
	for i := range d.Accounts {
		a := &d.Accounts[i]
		a.BurnDays = nil
		for j := range a.Recent {
			a.Recent[j].Burn = 0
		}
	}
	return d
}

// WithoutCycles is d without the quota cycles a collector before v0.5.0
// does not know, and without its hours: the body a collector from v0.4.0
// on, and before v0.5.0, decodes. It shares no slice with d, so either can
// be changed without the other.
func (d Doc) WithoutCycles() Doc {
	d = d.WithoutHours()
	for i := range d.Accounts {
		d.Accounts[i].Cycles = nil
	}
	return d
}

// WithoutHours is d without the hours a collector before v0.7.0 does not
// know, and without its costs: the body a collector from v0.5.0 on, and
// before v0.7.0, decodes. It shares no slice with d, so either can be
// changed without the other.
func (d Doc) WithoutHours() Doc {
	d = d.WithoutCosts()
	for i := range d.Accounts {
		d.Accounts[i].Hours, d.Accounts[i].BurnHours = nil, nil
	}
	return d
}

// WithoutCosts is d without the costs a collector before v0.10.0 does not
// know: the body a collector from v0.7.0 on, and before v0.10.0, decodes.
// It shares no slice with d, so either can be changed without the other.
func (d Doc) WithoutCosts() Doc {
	d.Costs = nil
	d.Sources = slices.Clone(d.Sources)
	d.Aliases = slices.Clone(d.Aliases)
	d.Accounts = slices.Clone(d.Accounts)
	for i := range d.Accounts {
		a := &d.Accounts[i]
		a.Hours, a.BurnHours = slices.Clone(a.Hours), slices.Clone(a.BurnHours)
		a.Cycles = slices.Clone(a.Cycles)
		a.Windows = slices.Clone(a.Windows)
		a.Projects = slices.Clone(a.Projects)
		a.Linked = slices.Clone(a.Linked)
		a.Days = slices.Clone(a.Days)
		a.BurnDays = slices.Clone(a.BurnDays)
		a.Recent = slices.Clone(a.Recent)
	}
	return d
}

// UpdateLine starts the last line of a last_error that says why the device's
// last release check failed, which it carries while no newer release waits
// for its next run. The rest of last_error has no line break.
const UpdateLine = "\nupdate: "

// SplitLastError splits an opened last_error into the last run's error and
// why the last release check failed. An older collector sends no release
// check's error.
func SplitLastError(s string) (run, update string) {
	if i := strings.LastIndex(s, UpdateLine); i >= 0 {
		return s[:i], s[i+len(UpdateLine):]
	}
	return s, ""
}

// Alias is a short name for an account, set on one device for the whole
// team. Label and Name are sealed. A cleared name has no Name, so that the
// clearing outranks an older name set on another device.
type Alias struct {
	Provider string    `json:"provider"`
	Label    string    `json:"label"`
	Name     string    `json:"name,omitempty"`
	At       time.Time `json:"at"`
}

// Cost is what people typed on one device of one subscription's cost.
// Label and Data are sealed: Data is JSON with short keys, which a reader
// reads leniently, so that later answers need no gate. At is when the
// newest of them was given.
type Cost struct {
	Provider string    `json:"provider"`
	Label    string    `json:"label"`
	Data     string    `json:"data"`
	At       time.Time `json:"at"`
}

// Account is one login on one provider, as seen from this device and OS user.
type Account struct {
	Provider string     `json:"provider"`
	Label    string     `json:"label"`
	Current  bool       `json:"current"`
	Plan     string     `json:"plan,omitempty"`
	QuotaAt  *time.Time `json:"quota_at,omitempty"`
	// QuotaFrom names the provider whose account the windows belong to, when
	// this account bills through it (Hermes on a Codex subscription). The
	// collector infers the link, so it is an assumption, not a reading.
	QuotaFrom    string     `json:"quota_from,omitempty"`
	Windows      []Window   `json:"windows"`
	Sessions     int        `json:"sessions"`
	Tokens       Tokens     `json:"tokens"`
	LastActiveAt *time.Time `json:"last_active_at,omitempty"`
	Projects     []Project  `json:"projects"`
	// Linked is what accounts of other providers on this device spent
	// through this one and are assumed to have billed to it (Hermes on this
	// Codex login). It is counted in their tokens, not in Tokens.
	Linked []Linked `json:"linked,omitempty"`
	// Days is the account's input plus output tokens on this device per UTC
	// day, newest first: index 0 is the UTC day of collected_at, index 1 the
	// day before, and so on. Trailing zeros are left out. Cache is not in it.
	Days []int64 `json:"days,omitempty"`
	// BurnDays is the account's weighted tokens on this device per UTC day,
	// laid out as Days is: index 0 is the UTC day of collected_at, and
	// trailing zeros are left out. A collector before v0.4.0 neither sends
	// nor reads it.
	BurnDays []int64 `json:"burn_days,omitempty"`
	// Hours is the account's input plus output tokens on this device per
	// UTC hour, newest first: index 0 is the UTC hour of collected_at, index
	// 1 the hour before, and so on, for up to MaxHours hours. Trailing zeros
	// are left out. With them a reader counts the device's day in its own
	// time zone. A collector before v0.7.0 neither sends nor reads it.
	Hours []int64 `json:"hours,omitempty"`
	// BurnHours is the account's weighted tokens on this device per UTC
	// hour, laid out as Hours is. A collector before v0.7.0 neither sends
	// nor reads it.
	BurnHours []int64 `json:"burn_hours,omitempty"`
	// Recent is the account's input plus output tokens on this device since
	// each window of its reading began, for the windows whose start is known
	// and which had not reset when the snapshot was taken.
	Recent []Recent `json:"recent,omitempty"`
	// Cycles are the periods of the account's quota windows this device
	// read, each with the fullest reading of it: by window, newest first. A
	// collector before v0.5.0 neither sends nor reads it.
	Cycles []Cycle `json:"cycles,omitempty"`
}

// Cycle is one period of a quota window, the one that resets at ResetsAt,
// as one device or a team read it: the fullest reading of it, and when it
// was last read in it.
type Cycle struct {
	Window   string    `json:"window"`
	ResetsAt time.Time `json:"resets_at"`
	Percent  float64   `json:"percent"`
	// LastReadAt is the newest reading of the period, never after
	// ResetsAt: one long before it may have missed the period's end.
	LastReadAt time.Time `json:"last_read_at"`
}

// SameReading says two accounts carry one quota reading: taken at the same
// time, with the same windows. A borrowed reading is the same as the one it
// was borrowed from.
func SameReading(a, b Account) bool {
	if a.QuotaAt == nil || b.QuotaAt == nil || !a.QuotaAt.Equal(*b.QuotaAt) {
		return false
	}
	return slices.EqualFunc(a.Windows, b.Windows, func(w, v Window) bool {
		same := w.ResetsAt == nil && v.ResetsAt == nil || w.ResetsAt != nil && v.ResetsAt != nil && w.ResetsAt.Equal(*v.ResetsAt)
		return w.Name == v.Name && w.Percent == v.Percent && w.Minutes == v.Minutes && same
	})
}

// Recent is an account's input plus output tokens on one device since a
// quota window began.
type Recent struct {
	Window string    `json:"window"`
	Start  time.Time `json:"start"`
	Tokens int64     `json:"tokens"`
	// Burn is the account's weighted tokens on this device over the same
	// time. A collector before v0.4.0 neither sends nor reads it.
	Burn int64 `json:"burn,omitempty"`
}

// Linked is what one account of another provider spent through an account.
// Label is sealed.
type Linked struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Sessions int    `json:"sessions"`
	Tokens   Tokens `json:"tokens"`
}

// Window is one quota window as the harness reported it.
type Window struct {
	Name     string     `json:"name"`
	Percent  float64    `json:"percent"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Minutes  int        `json:"minutes,omitempty"`
}

// Length is how long the window runs: its minutes as the harness reported
// them, else the length its name starts with, such as 5h in "5h" or 7d in
// "7d Opus". Zero means unknown.
func (w Window) Length() time.Duration {
	if w.Minutes > 0 {
		return time.Duration(w.Minutes) * time.Minute
	}
	m := windowLength.FindStringSubmatch(w.Name)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	if d := time.Duration(n) * unit; d <= MaxWindowMinutes*time.Minute {
		return d
	}
	return 0
}

// Start is when the window's current period began: its reset time minus its
// length. It is unknown without both.
func (w Window) Start() (time.Time, bool) {
	l := w.Length()
	if w.ResetsAt == nil || l <= 0 {
		return time.Time{}, false
	}
	return w.ResetsAt.Add(-l), true
}

var windowLength = regexp.MustCompile(`^(\d+)([mhd])(?:$| )`)

// Project is usage attributed to a working directory. Path is sealed.
type Project struct {
	Path     string `json:"path"`
	Sessions int    `json:"sessions"`
	Tokens   Tokens `json:"tokens"`
}

// Source is the health of one provider reader on this device.
type Source struct {
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// Tokens are counts a harness log already recorded. Input excludes cache reads.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{
		Input:      t.Input + o.Input,
		Output:     t.Output + o.Output,
		CacheRead:  t.CacheRead + o.CacheRead,
		CacheWrite: t.CacheWrite + o.CacheWrite,
	}
}

// Growth is how much t grew past prev. A counter that went down grew by zero.
func (t Tokens) Growth(prev Tokens) Tokens {
	pos := func(a, b int64) int64 {
		if a > b {
			return a - b
		}
		return 0
	}
	return Tokens{
		Input:      pos(t.Input, prev.Input),
		Output:     pos(t.Output, prev.Output),
		CacheRead:  pos(t.CacheRead, prev.CacheRead),
		CacheWrite: pos(t.CacheWrite, prev.CacheWrite),
	}
}

func (t Tokens) Zero() bool { return t == Tokens{} }

// InOut is the input plus output of t, the count the report's periods show.
func (t Tokens) InOut() int64 { return t.Input + t.Output }

// Total is every counted token, used only to rank rows.
func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

// KnownProvider reports whether p is one of Providers.
func KnownProvider(p string) bool { return slices.Contains(Providers, p) }

// Printable is s with control characters as spaces and the invisible
// characters that change text direction or hide text left out, so that text
// from a snapshot or an error cannot move the cursor, clear the screen, break
// a line of the report, or read as something else. Joiners, which names and
// emoji need, stay.
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case hidden(r):
			return -1
		}
		return r
	}, s)
}

// hidden reports whether r is a direction mark, embedding, override, or
// isolate, or an invisible character that joins nothing.
func hidden(r rune) bool {
	switch {
	case r == 0x061c, r == 0x200b, r == 0x200e, r == 0x200f, r == 0xfeff:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069, r >= 0xfff9 && r <= 0xfffb:
		return true
	}
	return false
}

// Truncate keeps the start of s in at most n bytes, cut on a rune boundary,
// ending in … when cut.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// DurationName is the short window name for a length in minutes: 5h, 7d, 90m.
func DurationName(minutes int) string {
	switch {
	case minutes <= 0:
		return "window"
	case minutes%(60*24) == 0:
		return fmt.Sprintf("%dd", minutes/(60*24))
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
