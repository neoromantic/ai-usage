package logs

import (
	"cmp"
	"maps"
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// codexLimitSkew is how long before the main limit's reading another limit's
// reading still describes the same moment.
const codexLimitSkew = 15 * time.Minute

// codexLimits reports the newest reading of the main codex limit, with the
// other limits read around the same time. A side limit read later must not
// hide the main one, and one read long before would look fresher than it is.
// Without a main reading the newest reading of any limit is used.
func codexLimits(files []*codexFile) *Limits {
	newest := map[string]*Limits{}
	for _, f := range files {
		for b, l := range f.limits {
			newest[b] = later(newest[b], l)
		}
	}
	buckets := slices.Sorted(maps.Keys(newest))
	anchor := newest[CodexMainLimit]
	if anchor == nil {
		for _, b := range buckets {
			anchor = later(anchor, newest[b])
		}
	}
	if anchor == nil {
		return nil
	}
	out := &Limits{ObservedAt: anchor.ObservedAt, Plan: anchor.Plan, Windows: append([]snapshot.Window(nil), anchor.Windows...)}
	for _, b := range buckets {
		l := newest[b]
		if l == anchor || l.ObservedAt.Before(anchor.ObservedAt.Add(-codexLimitSkew)) {
			continue
		}
		out.Windows = append(out.Windows, l.Windows...)
		if out.Plan == "" {
			out.Plan = l.Plan
		}
	}
	return out
}

// CodexMainLimit is the limit id of the main Codex bucket. Older logs leave it unset.
const CodexMainLimit = "codex"

type codexLogLimits struct {
	LimitID   string          `json:"limit_id"`
	PlanType  string          `json:"plan_type"`
	Primary   *codexLogWindow `json:"primary"`
	Secondary *codexLogWindow `json:"secondary"`
}

type codexLogWindow struct {
	UsedPercent     float64 `json:"used_percent"`
	WindowMinutes   int     `json:"window_minutes"`
	ResetsAt        int64   `json:"resets_at"`
	ResetsInSeconds int64   `json:"resets_in_seconds"`
}

// reading is the quota a token_count line carries, keyed by its limit id.
func (l *codexLogLimits) reading(at time.Time) (string, *Limits) {
	if l == nil || at.IsZero() || (l.Primary == nil && l.Secondary == nil) {
		return "", nil
	}
	bucket := cmp.Or(l.LimitID, CodexMainLimit)
	out := &Limits{ObservedAt: at.UTC(), Plan: l.PlanType}
	for _, w := range []*codexLogWindow{l.Primary, l.Secondary} {
		if w == nil {
			continue
		}
		win := snapshot.Window{Name: CodexWindowName(l.LimitID, w.WindowMinutes), Percent: w.UsedPercent, Minutes: w.WindowMinutes}
		switch {
		case w.ResetsAt > 0:
			t := time.Unix(w.ResetsAt, 0).UTC()
			win.ResetsAt = &t
		case w.ResetsInSeconds > 0:
			t := at.Add(time.Duration(w.ResetsInSeconds) * time.Second).UTC()
			win.ResetsAt = &t
		}
		out.Windows = append(out.Windows, win)
	}
	return bucket, out
}

// CodexWindowName names a Codex window by length, prefixed with the limit id
// when it is not the main codex bucket.
func CodexWindowName(limitID string, minutes int) string {
	name := snapshot.DurationName(minutes)
	if limitID != "" && limitID != CodexMainLimit {
		name = snapshot.PlainLabel(limitID + " " + name)
	}
	return name
}
