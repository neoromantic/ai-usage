package view

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// attentionOrder is the kinds, most urgent first.
var attentionOrder = []string{
	AttentionOut, AttentionOver, AttentionTight, AttentionOutside, AttentionError,
	AttentionNoReading, AttentionSilent, AttentionDuplicate, AttentionOld, AttentionUnder, AttentionIdle,
}

// attention is what needs attention now, most urgent first: windows that
// are out, will run out, or will end nearly full, windows someone outside
// the team fills, devices that fail, subscriptions in use with no reading,
// devices that are silent or were reinstalled, devices on an older
// release, windows past half that will be left mostly unused, and paid
// subscriptions no team device uses.
func attention(t Team, c Collector, now time.Time) []Attention {
	out := append(append([]Attention{}, quotaAttention(t, now)...), deviceAttention(t, c)...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := slices.Index(attentionOrder, a.Kind), slices.Index(attentionOrder, b.Kind); ra != rb {
			return ra < rb
		}
		switch a.Kind {
		case AttentionOut, AttentionOver, AttentionSilent, AttentionIdle:
			if ta, tb := when(a), when(b); !sameTime(ta, tb) {
				return before(ta, tb)
			}
		case AttentionTight, AttentionOutside:
			// The fuller forecast, or the bigger rise, first.
			if *a.Percent != *b.Percent {
				return *a.Percent > *b.Percent
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
// those of an account logged in here that will end nearly full, the main
// windows past half that will be left mostly unused, the subscriptions
// someone outside the team fills, those in use with no reading, and the
// idle ones.
func quotaAttention(t Team, now time.Time) []Attention {
	var out []Attention
	for _, p := range t.Providers {
		for _, a := range p.Accounts {
			if !a.Subscription {
				continue
			}
			of := Attention{Provider: p.Provider, Account: a.Label, Name: a.Name}
			if a.Outside != nil {
				at := of
				pts := a.Outside.Points
				at.Kind, at.At, at.Percent = AttentionOutside, timePtr(a.Outside.Since), &pts
				out = append(out, at)
			}
			// A reading that does not come says nothing new while the
			// subscription is not used.
			if a.NoReading != nil && a.Burn.Week > 0 {
				at := of
				at.Kind, at.Message = AttentionNoReading, *a.NoReading
				out = append(out, at)
			}
			idle := idleSince(a, now)
			if idle != nil {
				at := of
				at.Kind, at.At = AttentionIdle, idle
				out = append(out, at)
			}
			if a.Quota == nil {
				continue
			}
			for _, w := range a.Quota.Windows {
				if !w.Limits {
					continue
				}
				at := of
				if !w.Main {
					at.Window = w.Name
				}
				// A full window is not stale, as it stays full until it
				// resets, but the provider can reset it early: an old
				// reading of it says how old too.
				if w.Stale || w.State == StateOut && now.Sub(w.ObservedAt) > StaleAfter {
					at.ReadingAge = int64(now.Sub(w.ObservedAt).Seconds())
				}
				switch {
				case w.State == StateOut:
					at.Kind, at.At = AttentionOut, w.ResetsAt
				case w.State == StateOver:
					at.Kind, at.At, at.ResetsAt = AttentionOver, w.Forecast.RunsOutAt, w.ResetsAt
				case w.State == StateTight && a.Current:
					// Only one's own account: another's is its user's to
					// watch.
					at.Kind, at.ResetsAt = AttentionTight, w.ResetsAt
				case w.State == StateUnder && w.Main && w.Forecast.Elapsed >= 0.5 && idle == nil:
					// Of an idle subscription, IDLE says more.
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

// idleSince is when a paid subscription that no team device has used for
// IdleAfter, as its LastActiveAt says, was last used, or nil. It has a
// reading, and nothing says that someone outside the team uses it: it has
// no Outside, and its main window, read since it began after that last use,
// is under 1%. The unknown account is no subscription to pay for, and a
// free plan costs nothing.
func idleSince(a TeamAccount, now time.Time) *time.Time {
	last := a.LastActiveAt
	switch {
	case !a.Subscription, a.Label == collect.UnknownAccount, a.Quota == nil, a.Outside != nil,
		last == nil, now.Sub(*last) < IdleAfter, a.Plan != nil && strings.EqualFold(*a.Plan, "free"):
		return nil
	}
	if m := knownMain(a.Quota); m != nil && m.Percent >= 1 && m.ResetsAt != nil {
		if l := windowLength(*m); l > 0 && m.ResetsAt.Add(-l).After(*last) {
			return nil
		}
	}
	return last
}

// deviceAttention is the failing, silent, reinstalled, and old devices.
// This device's failed run, relay, and update check are errors of this
// device, and another's failed update check is its error while it is old.
// A reinstalled device's old record is a duplicate, not a silent or old
// device: forgetting it is all there is to do.
func deviceAttention(t Team, c Collector) []Attention {
	var out []Attention
	var stuck, old []TeamDevice
	for _, d := range t.Devices {
		switch {
		case d.ReplacedBy != nil:
			out = append(out, Attention{Kind: AttentionDuplicate, Devices: []string{d.Label}, DeviceID: d.Device, Message: *d.ReplacedBy, At: timePtr(d.CollectedAt)})
			continue
		case d.Silent:
			// A silent device's error is the one it last reported, not what
			// fails on it now.
			at := Attention{Kind: AttentionSilent, Devices: []string{d.Label}, At: timePtr(d.CollectedAt)}
			if d.Error != nil {
				at.Message = *d.Error
			}
			out = append(out, at)
		case d.Error != nil:
			out = append(out, Attention{Kind: AttentionError, Devices: []string{d.Label}, Message: *d.Error, At: errorAt(d)})
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
		switch {
		case d.Old && len(d.Limitations) > 0:
			stuck = append(stuck, d)
		case d.Old:
			old = append(old, d)
		}
	}
	if t.Latest != nil {
		// The devices whose release cannot do something come first: they
		// need a reinstall.
		out = append(out, oldAttention(stuck, *t.Latest)...)
		out = append(out, oldAttention(old, *t.Latest)...)
	}
	return out
}

// oldAttention is the one entry for devices on an older release than
// latest, with what their releases cannot do, or none.
func oldAttention(ds []TeamDevice, latest string) []Attention {
	if len(ds) == 0 {
		return nil
	}
	at := Attention{Kind: AttentionOld, Message: latest}
	var behind *time.Time
	for _, d := range ds {
		at.Devices = append(at.Devices, d.Label)
		for _, l := range d.Limitations {
			if !slices.Contains(at.Limitations, l) {
				at.Limitations = append(at.Limitations, l)
			}
		}
		if notUpdating(d) && (behind == nil || d.BehindSince.Before(*behind)) {
			behind = d.BehindSince
		}
	}
	sort.Strings(at.Devices)
	sort.Strings(at.Limitations)
	at.At = timeOf(behind)
	return []Attention{at}
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
// window resets, when a silent device last reported, when an idle
// subscription was last used, so the longest idle comes first, and when an
// over window runs out, which is its reset when it runs out at its reset and
// has no time of its own.
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
