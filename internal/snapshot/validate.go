package snapshot

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

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
