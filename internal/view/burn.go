package view

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// accountBurn is what account a, whose opened label is label, burned on the
// device whose snapshot d is: by UTC day, laid out as a.Days is; its recent
// windows with their burn; and whether that is estimated from input plus
// output, as a snapshot from before v0.4.0 has no burn. This device's own
// snapshot always has its burn.
func (m *merger) accountBurn(d snapshot.Doc, a snapshot.Account, label string) (days []int64, recent []snapshot.Recent, estimated bool) {
	days, byWindow, _, estimated := collect.AccountBurn(a, a.Provider, label, d.CollectedAt)
	recent = slices.Clone(a.Recent)
	for i := range recent {
		recent[i].Burn = byWindow[recent[i].Window]
	}
	return days, recent, estimated && d.Device != m.in.Doc.Device
}

// estimate says, when some device's burn is estimated, which devices keep
// this one from sending its own and why, from the gate that decides it, as
// held_back_by and estimated_reason have it.
func (m *merger) estimate(t *Team) {
	for _, d := range t.Devices {
		t.Estimated = t.Estimated || d.BurnEstimated
	}
	if !t.Estimated {
		return
	}
	g := collect.ExtendedWire(m.in.Team, m.in.Key.Fingerprint(), m.in.Config.Device, m.in.State.Relay.NoExtUntil, m.now)
	type held struct{ label, version string }
	var hs []held
	for _, id := range g.HeldBack {
		h := held{label: id, version: m.in.Team.Known[id].Version}
		for _, dv := range m.devs {
			if dv.dev.Device == id {
				h = held{dv.dev.Label, dv.dev.CollectorVersion}
			}
		}
		hs = append(hs, h)
		t.HeldBackBy = append(t.HeldBackBy, h.label)
	}
	var reason string
	switch {
	case g.Reason == collect.GateStale:
		reason = "the team was last read " + age(m.now.Sub(m.in.Team.PulledAt)) + " ago"
	case g.Reason == collect.GateRejected:
		reason = "the relay did not accept weighted days; trying again " + g.Until.In(m.loc()).Format("Jan 2 15:04")
	case len(hs) == 1:
		reason = hs[0].label + " runs " + hs[0].version + ": " + upgrade(hs[0].version)
	case len(hs) > 1:
		reason = strconv.Itoa(len(hs)) + " devices run releases before " + collect.ExtSince + ": " + firstNames(t.HeldBackBy, 3)
	default:
		// Every device reads the fields, and some have not sent them yet:
		// their next runs do.
		var labels []string
		for _, d := range t.Devices {
			if d.BurnEstimated {
				labels = append(labels, d.Label)
			}
		}
		reason = labels[0] + " sent no weighted days yet: its next run sends them"
		if len(labels) > 1 {
			reason = strconv.Itoa(len(labels)) + " devices sent no weighted days yet: " + firstNames(labels, 3)
		}
	}
	t.EstimatedReason = strPtr(reason)
}

// upgrade is what a device on version does to send burn: a release that
// updates itself does so within a run, and one before v0.2.5, which cannot
// say its update failed, is reinstalled.
func upgrade(version string) string {
	if selfupdate.Newer("v0.2.5", version) {
		return "reinstall with install.sh"
	}
	return "it updates itself within 15 minutes"
}

// firstNames is up to n of names, and an ellipsis for the rest.
func firstNames(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:n], ", ") + ", …"
}

// loc is the zone the report's own times of day are in.
func (m *merger) loc() *time.Location {
	if m.in.Loc != nil {
		return m.in.Loc
	}
	return time.Local
}
