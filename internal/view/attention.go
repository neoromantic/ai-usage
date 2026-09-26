package view

import (
	"slices"
	"sort"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// attentionOrder is the kinds, most urgent first.
var attentionOrder = []string{AttentionOut, AttentionOver, AttentionError, AttentionSilent, AttentionOld, AttentionUnder}

// attention is what needs attention now, most urgent first: windows that
// are out or will run out, devices that fail or are silent, devices on an
// older release, and windows past half that will be left mostly unused.
func attention(t Team, c Collector, now time.Time) []Attention {
	out := append(append([]Attention{}, quotaAttention(t, now)...), deviceAttention(t, c)...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := slices.Index(attentionOrder, a.Kind), slices.Index(attentionOrder, b.Kind); ra != rb {
			return ra < rb
		}
		switch a.Kind {
		case AttentionOut, AttentionOver, AttentionSilent:
			if ta, tb := when(a), when(b); !sameTime(ta, tb) {
				return before(ta, tb)
			}
		case AttentionUnder:
			if *a.Percent != *b.Percent {
				return *a.Percent < *b.Percent
			}
		case AttentionError:
			return a.Devices[0] < b.Devices[0]
		}
		return false
	})
	return out
}

// quotaAttention is the subscription windows that are out or will run out,
// and the main windows past half that will be left mostly unused.
func quotaAttention(t Team, now time.Time) []Attention {
	var out []Attention
	for _, p := range t.Providers {
		for _, a := range p.Accounts {
			if !a.Subscription || a.Quota == nil {
				continue
			}
			main := mainWindow(a.Quota.Windows)
			if main == nil {
				continue
			}
			for _, w := range a.Quota.Windows {
				if !w.Main && !limitsMore(w, *main) {
					continue
				}
				at := Attention{Provider: p.Provider, Account: a.Label, Name: a.Name}
				if !w.Main {
					at.Window = w.Name
				}
				if w.Stale {
					at.ReadingAge = int64(now.Sub(w.ObservedAt).Seconds())
				}
				switch {
				case w.State == StateOut:
					at.Kind, at.At = AttentionOut, w.ResetsAt
				case w.State == StateOver:
					at.Kind, at.At, at.ResetsAt = AttentionOver, w.Forecast.RunsOutAt, w.ResetsAt
				case w.State == StateUnder && w.Main && w.Forecast.Elapsed >= 0.5:
					at.Kind, at.ResetsAt = AttentionUnder, w.ResetsAt
				default:
					continue
				}
				if at.Kind != AttentionOut {
					pct := w.Forecast.Percent
					at.Percent = &pct
				}
				out = append(out, at)
			}
		}
	}
	return out
}

// deviceAttention is the failing, silent, and old devices. This device's
// failed run, relay, and update check are errors of this device, and
// another's failed update check is its error while it is old.
func deviceAttention(t Team, c Collector) []Attention {
	var out []Attention
	var old []string
	var behind *time.Time
	for _, d := range t.Devices {
		switch {
		case d.Silent:
			// A silent device's error is the one it last reported, not what
			// fails on it now.
			at := Attention{Kind: AttentionSilent, Devices: []string{d.Label}, At: timePtr(d.CollectedAt)}
			if d.Error != nil {
				at.Message = *d.Error
			}
			out = append(out, at)
		case d.Error != nil:
			out = append(out, Attention{Kind: AttentionError, Devices: []string{d.Label}, Message: *d.Error})
		}
		if d.This {
			for _, e := range collectorErrors(c, d.CollectedAt) {
				out = append(out, Attention{Kind: AttentionError, Devices: []string{d.Label}, Message: e})
			}
		}
		// On a device that is not old, a failed release check is only a
		// note in the status view.
		if d.Old && d.UpdateError != nil && !d.Silent {
			out = append(out, Attention{Kind: AttentionError, Devices: []string{d.Label}, Message: sourceError("update", *d.UpdateError)})
		}
		if d.Old {
			old = append(old, d.Label)
		}
		if notUpdating(d) && (behind == nil || d.BehindSince.Before(*behind)) {
			behind = d.BehindSince
		}
	}
	if len(old) > 0 && t.Latest != nil {
		sort.Strings(old)
		out = append(out, Attention{Kind: AttentionOld, Devices: old, Message: *t.Latest, At: timeOf(behind)})
	}
	return out
}

// notUpdating is an old device that does not update itself: it has
// reported on its release for BehindAfter since its BehindSince, so it had
// the runs to update. A silent device is silent instead.
func notUpdating(d TeamDevice) bool {
	return d.Old && !d.Silent && d.BehindSince != nil && d.CollectedAt.Sub(*d.BehindSince) >= BehindAfter
}

// collectorErrors are the errors behind the header's failed run, failing
// relay, and failed update check that this device's report, made at
// reported, does not carry, each named once. A run that failed is its
// device's error already, unless it failed after the report, as a run a bug
// stopped does. A run whose sources were read counts as a success, whatever
// its relay or update check did.
func collectorErrors(c Collector, reported time.Time) []string {
	var out []string
	if failed(c) && c.LastErrorAt.After(reported) {
		out = append(out, *c.LastError)
	}
	if c.Relay.URL != nil && c.Relay.LastError != nil {
		out = append(out, sourceError("relay", *c.Relay.LastError))
	}
	if c.Update.Error != nil && c.Update.Staged == nil && !selfupdate.Dev(c.Version) {
		out = append(out, sourceError("update", *c.Update.Error))
	}
	return out
}

// failed is whether the last run ended in an error: the error is newer than
// the last success.
func failed(c Collector) bool {
	return c.LastError != nil && c.LastErrorAt != nil && (c.LastSuccessAt == nil || c.LastErrorAt.After(*c.LastSuccessAt))
}

// when is the time an entry is ordered by within its kind: when an out
// window resets, when a silent device last reported, and when an over window
// runs out, which is its reset when it runs out at its reset and has no time
// of its own.
func when(a Attention) *time.Time {
	if a.Kind == AttentionOver && a.At == nil {
		return a.ResetsAt
	}
	return a.At
}

// before orders times with an unknown one last.
func before(a, b *time.Time) bool {
	if a == nil || b == nil {
		return b == nil && a != nil
	}
	return a.Before(*b)
}
