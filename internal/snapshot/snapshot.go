// Package snapshot is the document one device publishes to the relay.
//
// The relay must be able to see that a body is a usage snapshot, so the shape
// is fixed and small: counts, percents, timestamps, and short strings. Strings
// that name a person, a machine, or a path are sealed with the team key before
// they leave the device. Numbers, provider names, and window names stay plain.
package snapshot

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	MaxAliases        = 48
	MaxSealed         = 512
	MaxPlain          = 40
	MaxTokenCount     = int64(1) << 50
	MaxSessionCount   = 1 << 24
	MaxWindowMinutes  = 60 * 24 * 62
	futureSkewAllowed = 10 * time.Minute
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
	Accounts         []Account `json:"accounts"`
	Sources          []Source  `json:"sources"`
	// Aliases are the short names this device gave accounts with
	// `ai-usage alias`. The newest one for an account, from any device in
	// the team, names it everywhere.
	Aliases []Alias `json:"aliases,omitempty"`
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
	// Recent is the account's input plus output tokens on this device since
	// each window of its reading began, for the windows whose start is known
	// and which had not reset when the snapshot was taken.
	Recent []Recent `json:"recent,omitempty"`
}

// Recent is an account's input plus output tokens on one device since a
// quota window began.
type Recent struct {
	Window string    `json:"window"`
	Start  time.Time `json:"start"`
	Tokens int64     `json:"tokens"`
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

var (
	teamRe   = regexp.MustCompile(`^[a-z2-7]{32}$`)
	deviceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{7,63}$`)
	sealedRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	plainRe  = regexp.MustCompile(`^[A-Za-z0-9 ._:+()/-]*$`)
	statuses = map[string]bool{"ok": true, "partial": true, "error": true, "skipped": true}
)

// ValidTeam reports whether s has the fingerprint shape.
func ValidTeam(s string) bool { return teamRe.MatchString(s) }

// ValidDevice reports whether s has the device id shape.
func ValidDevice(s string) bool { return deviceRe.MatchString(s) }

// Decode parses a body strictly. Unknown fields, trailing data, and any value
// outside the limits are errors. It does not check signatures.
func Decode(body []byte) (Doc, error) {
	var d Doc
	if len(body) > MaxBytes {
		return d, fmt.Errorf("snapshot is %d bytes, limit %d", len(body), MaxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, fmt.Errorf("snapshot: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return d, errors.New("snapshot: trailing data")
	}
	return d, d.Validate()
}

// FromFuture reports whether collected_at is further ahead of now than clocks
// may drift.
func (d Doc) FromFuture(now time.Time) bool {
	return d.CollectedAt.After(now.Add(futureSkewAllowed))
}

// Validate checks limits.
func (d Doc) Validate() error {
	if d.V != Version {
		return fmt.Errorf("snapshot version %d, want %d", d.V, Version)
	}
	if !ValidTeam(d.Team) {
		return errors.New("team is not a fingerprint")
	}
	if !ValidDevice(d.Device) {
		return errors.New("device id has the wrong shape")
	}
	if err := sealedText.check("device_label", d.DeviceLabel, true); err != nil {
		return err
	}
	if err := sealedText.check("os_user", d.OSUser, true); err != nil {
		return err
	}
	if err := sealedText.check("last_error", d.LastError, false); err != nil {
		return err
	}
	if err := plainText.check("collector_version", d.CollectorVersion, true); err != nil {
		return err
	}
	if d.CollectedAt.IsZero() {
		return errors.New("collected_at is missing")
	}
	if d.Accounts == nil || d.Sources == nil {
		return errors.New("accounts and sources must be arrays")
	}
	return cmp.Or(
		checkList("accounts", d.Accounts, MaxAccounts, Account.validate),
		checkList("aliases", d.Aliases, MaxAliases, Alias.validate),
		checkList("sources", d.Sources, MaxSources, Source.validate),
	)
}

func (a Account) validate() error {
	if !KnownProvider(a.Provider) {
		return errors.New("unknown provider")
	}
	if err := sealedText.check("label", a.Label, true); err != nil {
		return err
	}
	if err := plainText.check("plan", a.Plan, false); err != nil {
		return err
	}
	if a.Windows == nil || a.Projects == nil {
		return errors.New("windows and projects must be arrays")
	}
	if len(a.Windows) > 0 && a.QuotaAt == nil {
		return errors.New("windows without quota_at")
	}
	if a.QuotaFrom != "" && (!KnownProvider(a.QuotaFrom) || a.QuotaFrom == a.Provider || len(a.Windows) == 0) {
		return errors.New("quota_from must name another provider and come with its windows")
	}
	return cmp.Or(
		checkCounts(a.Sessions, a.Tokens),
		checkList("windows", a.Windows, MaxWindows, Window.validate),
		checkList("projects", a.Projects, MaxProjects, Project.validate),
		checkList("days", a.Days, MaxDays, checkDay),
		checkList("recent", a.Recent, MaxWindows, Recent.validate),
		checkList("linked", a.Linked, MaxLinked, func(l Linked) error { return l.validate(a.Provider) }),
	)
}

func (w Window) validate() error {
	if err := plainText.check("window name", w.Name, true); err != nil {
		return err
	}
	if w.Percent < 0 || w.Percent > 1000 || w.Percent != w.Percent {
		return errors.New("percent out of range")
	}
	if w.Minutes < 0 || w.Minutes > MaxWindowMinutes {
		return errors.New("minutes out of range")
	}
	return nil
}

func (p Project) validate() error {
	if err := sealedText.check("path", p.Path, true); err != nil {
		return err
	}
	return checkCounts(p.Sessions, p.Tokens)
}

func (r Recent) validate() error {
	if err := plainText.check("window name", r.Window, true); err != nil {
		return err
	}
	if r.Start.IsZero() {
		return errors.New("start is missing")
	}
	if r.Tokens < 0 || r.Tokens > MaxTokenCount {
		return errors.New("token count out of range")
	}
	return nil
}

// validate checks l as spent through an account of provider owner.
func (l Linked) validate(owner string) error {
	if !KnownProvider(l.Provider) || l.Provider == owner {
		return errors.New("must name another provider")
	}
	if err := sealedText.check("label", l.Label, true); err != nil {
		return err
	}
	return checkCounts(l.Sessions, l.Tokens)
}

func (a Alias) validate() error {
	if !KnownProvider(a.Provider) {
		return errors.New("unknown provider")
	}
	if err := sealedText.check("label", a.Label, true); err != nil {
		return err
	}
	if err := sealedText.check("name", a.Name, false); err != nil {
		return err
	}
	if a.At.IsZero() {
		return errors.New("at is missing")
	}
	return nil
}

func (s Source) validate() error {
	if !KnownProvider(s.Provider) {
		return errors.New("unknown provider")
	}
	if !statuses[s.Status] {
		return errors.New("unknown status")
	}
	return sealedText.check("error", s.Error, false)
}

func checkDay(n int64) error {
	if n < 0 || n > MaxTokenCount {
		return errors.New("day count out of range")
	}
	return nil
}

// checkList checks how many items there are, then each one, and names the
// first that fails by its index.
func checkList[T any](name string, items []T, limit int, check func(T) error) error {
	if len(items) > limit {
		return fmt.Errorf("%d %s, limit %d", len(items), name, limit)
	}
	for i, item := range items {
		if err := check(item); err != nil {
			return fmt.Errorf("%s[%d]: %w", name, i, err)
		}
	}
	return nil
}

func checkCounts(sessions int, t Tokens) error {
	if sessions < 0 || sessions > MaxSessionCount {
		return errors.New("session count out of range")
	}
	for _, n := range []int64{t.Input, t.Output, t.CacheRead, t.CacheWrite} {
		if n < 0 || n > MaxTokenCount {
			return errors.New("token count out of range")
		}
	}
	return nil
}

// textRule is what a string field may hold: at most max bytes that re
// matches. what names that kind of text in errors.
type textRule struct {
	re   *regexp.Regexp
	max  int
	what string
}

var (
	sealedText = textRule{sealedRe, MaxSealed, "sealed label"}
	plainText  = textRule{plainRe, MaxPlain, "short label"}
)

func (t textRule) check(name, s string, required bool) error {
	if s == "" {
		if required {
			return fmt.Errorf("%s is missing", name)
		}
		return nil
	}
	if len(s) > t.max || !t.re.MatchString(s) {
		return fmt.Errorf("%s is not a %s", name, t.what)
	}
	return nil
}

// KnownProvider reports whether p is one of Providers.
func KnownProvider(p string) bool { return slices.Contains(Providers, p) }

// PlainLabel trims s to a string Validate accepts in a plain field.
func PlainLabel(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if len(out) >= MaxPlain {
			break
		}
		if plainRe.MatchString(string(r)) {
			out = append(out, r)
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}

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
