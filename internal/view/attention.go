package view

import (
	"sort"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// attentionRank orders the kinds, most urgent first.
var attentionRank = map[string]int{
	AttentionOut:    0,
	AttentionOver:   1,
	AttentionError:  2,
	AttentionSilent: 3,
	AttentionOld:    4,
	AttentionUnder:  5,
}

// attention is what needs attention now, most urgent first: windows that
// are out or will run out, devices that fail or are silent, devices on an
// older release, and windows past half that will be left mostly unused.
// This device's relay and update check fail as errors of this device.
func attention(t Team, c Collector, now time.Time) []Attention {
	out := []Attention{}
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
					pct := w.Forecast.Percent
					at.Percent = &pct
				case w.State == StateUnder && w.Main && w.Forecast.Elapsed >= 0.5:
					at.Kind, at.ResetsAt = AttentionUnder, w.ResetsAt
					pct := w.Forecast.Percent
					at.Percent = &pct
				default:
					continue
				}
				out = append(out, at)
			}
		}
	}
	var old []string
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
			for _, e := range collectorErrors(c) {
				out = append(out, Attention{Kind: AttentionError, Devices: []string{d.Label}, Message: e})
			}
		}
		if d.Old {
			old = append(old, d.Label)
		}
	}
	if len(old) > 0 && t.Latest != nil {
		sort.Strings(old)
		out = append(out, Attention{Kind: AttentionOld, Devices: old, Message: *t.Latest})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if attentionRank[a.Kind] != attentionRank[b.Kind] {
			return attentionRank[a.Kind] < attentionRank[b.Kind]
		}
		switch a.Kind {
		case AttentionOut, AttentionOver, AttentionSilent:
			if !sameTime(a.At, b.At) {
				return before(a.At, b.At)
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

// collectorErrors are the errors behind the header's failing relay and
// update check, each named once. A run that failed is its device's error
// already; a run whose sources were read counts as a success, whatever its
// relay or update check did.
func collectorErrors(c Collector) []string {
	var out []string
	if c.Relay.URL != nil && c.Relay.LastError != nil {
		out = append(out, sourceError("relay", *c.Relay.LastError))
	}
	if c.Update.Error != nil && c.Update.Staged == nil && !selfupdate.Dev(c.Version) {
		out = append(out, sourceError("update", *c.Update.Error))
	}
	return out
}

// before orders times with an unknown one last.
func before(a, b *time.Time) bool {
	if a == nil || b == nil {
		return b == nil && a != nil
	}
	return a.Before(*b)
}
