// Package snapshot is the document one device publishes to the relay.
//
// The relay must be able to see that a body is a usage snapshot, so the shape
// is fixed and small: counts, percents, timestamps, and short strings. Strings
// that name a person, a machine, or a path are sealed with the team key before
// they leave the device. Numbers, provider names, and window names stay plain.
package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

// Version is the wire format. The relay rejects any other value.
const Version = 1

// Limits. The relay enforces all of them. The collector trims to fit.
const (
	MaxBytes          = 32 << 10
	MaxAccounts       = 24
	MaxWindows        = 8
	MaxProjects       = 12
	MaxLinked         = 4
	MaxSources        = 12
	MaxSealed         = 512
	MaxPlain          = 40
	MaxTokenCount     = int64(1) << 50
	MaxSessionCount   = 1 << 24
	MaxWindowMinutes  = 60 * 24 * 62
	futureSkewAllowed = 10 * time.Minute
)

// Providers the collector knows. The relay rejects any other name.
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
	return d, d.Validate(time.Time{})
}

// Validate checks limits. A non-zero now also rejects a collected_at in the future.
func (d Doc) Validate(now time.Time) error {
	if d.V != Version {
		return fmt.Errorf("snapshot version %d, want %d", d.V, Version)
	}
	if !ValidTeam(d.Team) {
		return errors.New("team is not a fingerprint")
	}
	if !ValidDevice(d.Device) {
		return errors.New("device id has the wrong shape")
	}
	for name, s := range map[string]string{"device_label": d.DeviceLabel, "os_user": d.OSUser} {
		if err := checkSealed(name, s, true); err != nil {
			return err
		}
	}
	if err := checkSealed("last_error", d.LastError, false); err != nil {
		return err
	}
	if err := checkPlain("collector_version", d.CollectorVersion, true); err != nil {
		return err
	}
	if d.CollectedAt.IsZero() {
		return errors.New("collected_at is missing")
	}
	if !now.IsZero() && d.CollectedAt.After(now.Add(futureSkewAllowed)) {
		return errors.New("collected_at is in the future")
	}
	if len(d.Accounts) > MaxAccounts {
		return fmt.Errorf("%d accounts, limit %d", len(d.Accounts), MaxAccounts)
	}
	if d.Accounts == nil || d.Sources == nil {
		return errors.New("accounts and sources must be arrays")
	}
	for i, a := range d.Accounts {
		if err := a.validate(); err != nil {
			return fmt.Errorf("accounts[%d]: %w", i, err)
		}
	}
	if len(d.Sources) > MaxSources {
		return fmt.Errorf("%d sources, limit %d", len(d.Sources), MaxSources)
	}
	for i, s := range d.Sources {
		if !knownProvider(s.Provider) {
			return fmt.Errorf("sources[%d]: unknown provider", i)
		}
		if !statuses[s.Status] {
			return fmt.Errorf("sources[%d]: unknown status", i)
		}
		if err := checkSealed("error", s.Error, false); err != nil {
			return fmt.Errorf("sources[%d]: %w", i, err)
		}
	}
	return nil
}

func (a Account) validate() error {
	if !knownProvider(a.Provider) {
		return errors.New("unknown provider")
	}
	if err := checkSealed("label", a.Label, true); err != nil {
		return err
	}
	if err := checkPlain("plan", a.Plan, false); err != nil {
		return err
	}
	if a.Windows == nil || a.Projects == nil {
		return errors.New("windows and projects must be arrays")
	}
	if len(a.Windows) > MaxWindows {
		return fmt.Errorf("%d windows, limit %d", len(a.Windows), MaxWindows)
	}
	if len(a.Windows) > 0 && a.QuotaAt == nil {
		return errors.New("windows without quota_at")
	}
	if a.QuotaFrom != "" && (!knownProvider(a.QuotaFrom) || a.QuotaFrom == a.Provider || len(a.Windows) == 0) {
		return errors.New("quota_from must name another provider and come with its windows")
	}
	for i, w := range a.Windows {
		if err := checkPlain("window name", w.Name, true); err != nil {
			return fmt.Errorf("windows[%d]: %w", i, err)
		}
		if w.Percent < 0 || w.Percent > 1000 || w.Percent != w.Percent {
			return fmt.Errorf("windows[%d]: percent out of range", i)
		}
		if w.Minutes < 0 || w.Minutes > MaxWindowMinutes {
			return fmt.Errorf("windows[%d]: minutes out of range", i)
		}
	}
	if err := checkCounts(a.Sessions, a.Tokens); err != nil {
		return err
	}
	if len(a.Projects) > MaxProjects {
		return fmt.Errorf("%d projects, limit %d", len(a.Projects), MaxProjects)
	}
	for i, p := range a.Projects {
		if err := checkSealed("path", p.Path, true); err != nil {
			return fmt.Errorf("projects[%d]: %w", i, err)
		}
		if err := checkCounts(p.Sessions, p.Tokens); err != nil {
			return fmt.Errorf("projects[%d]: %w", i, err)
		}
	}
	if len(a.Linked) > MaxLinked {
		return fmt.Errorf("%d linked, limit %d", len(a.Linked), MaxLinked)
	}
	for i, l := range a.Linked {
		if !knownProvider(l.Provider) || l.Provider == a.Provider {
			return fmt.Errorf("linked[%d]: must name another provider", i)
		}
		if err := checkSealed("label", l.Label, true); err != nil {
			return fmt.Errorf("linked[%d]: %w", i, err)
		}
		if err := checkCounts(l.Sessions, l.Tokens); err != nil {
			return fmt.Errorf("linked[%d]: %w", i, err)
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

func checkSealed(name, s string, required bool) error {
	if s == "" {
		if required {
			return fmt.Errorf("%s is missing", name)
		}
		return nil
	}
	if len(s) > MaxSealed || !sealedRe.MatchString(s) {
		return fmt.Errorf("%s is not a sealed label", name)
	}
	return nil
}

func checkPlain(name, s string, required bool) error {
	if s == "" {
		if required {
			return fmt.Errorf("%s is missing", name)
		}
		return nil
	}
	if len(s) > MaxPlain || !plainRe.MatchString(s) {
		return fmt.Errorf("%s is not a short label", name)
	}
	return nil
}

func knownProvider(p string) bool {
	for _, k := range Providers {
		if p == k {
			return true
		}
	}
	return false
}

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
