package view

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/relay"
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
	// A device whose snapshot the relay no longer has is known by its id,
	// and has not reported since its last one.
	var hs []TeamDevice
	for _, id := range g.HeldBack {
		k := m.in.Team.Known[id]
		h := TeamDevice{Device: id, Label: id, CollectorVersion: k.Version, CollectedAt: k.Last, Silent: true}
		for _, dv := range m.devs {
			if dv.dev.Device == id {
				h = *dv.dev
			}
		}
		hs = append(hs, h)
	}
	// The busiest first, as the matrix lists them.
	by := byBurn(Week, func(d TeamDevice) Usage { return d.Burn }, func(d TeamDevice) string { return d.Label })
	slices.SortStableFunc(hs, by)
	for _, h := range hs {
		t.HeldBackBy = append(t.HeldBackBy, h.Label)
	}
	var reason string
	switch {
	case g.Reason == collect.GateStale:
		reason = "the team was last read " + age(m.now.Sub(m.in.Team.PulledAt)) + " ago"
	case g.Reason == collect.GateRejected:
		reason = "the relay did not accept weighted days; trying again " + g.Until.In(m.loc()).Format("Jan 2 15:04")
	case len(hs) == 1:
		reason = m.heldBackReason(hs[0])
	case len(hs) > 1:
		reason = strconv.Itoa(len(hs)) + " devices run releases before " + collect.ExtSince + ": " + m.heldBackSteps(hs)
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

// What a device that holds back weighted days does to send them: it is
// forgotten, reinstalled, built again from source, updated by hand, or
// updates itself.
const (
	stepForget = iota
	stepReinstall
	stepRebuild
	stepUpdate
	stepSelf
)

// heldBackStep is what held-back device h does to send weighted days. A
// device the relay no longer has a record of, or that has not reported for
// as long as the relay keeps the least of records, may never come back, as
// when it was reinstalled. A release before v0.2.5, which cannot say its
// update failed, is reinstalled; a later one whose update fails is updated
// by hand. A build from source never updates itself.
func (m *merger) heldBackStep(h TeamDevice) int {
	switch {
	case m.gone(h) || h.ReplacedBy != nil || m.now.Sub(h.CollectedAt) > relay.DefaultLimits().MinRecordTTL:
		return stepForget
	case selfupdate.Newer("v0.2.5", h.CollectorVersion):
		return stepReinstall
	case selfupdate.Dev(h.CollectorVersion):
		return stepRebuild
	case h.NotUpdating || h.UpdateError != nil:
		return stepUpdate
	}
	return stepSelf
}

// gone says the relay no longer has a record of device h.
func (m *merger) gone(h TeamDevice) bool {
	return !slices.ContainsFunc(m.devs, func(dv *device) bool { return dv.dev.Device == h.Device })
}

// heldBackReason is why the one held-back device h holds back weighted
// days, and what it takes.
func (m *merger) heldBackReason(h TeamDevice) string {
	runs := h.Label + " runs " + h.CollectorVersion + ": "
	switch m.heldBackStep(h) {
	case stepForget:
		// The relay forgets a record for everyone, but only the device
		// that forgets a record the relay let expire stops waiting for it.
		out := h.Label + " has not reported since " + h.CollectedAt.In(m.loc()).Format("Jan 2") + ": forget it with ai-usage team forget-device " + shortDevice(h.Device)
		if m.gone(h) {
			out += " on each device"
		}
		return out
	case stepReinstall:
		return runs + "reinstall with install.sh"
	case stepRebuild:
		return h.Label + " runs a build from source before " + collect.ExtSince + ": build it again"
	case stepUpdate:
		return runs + "its update fails; run ai-usage update on it"
	}
	if h.Silent {
		return h.Label + " has not reported since " + h.CollectedAt.In(m.loc()).Format("Jan 2") + ": it updates itself when it next runs"
	}
	return runs + "it updates itself within 15 minutes"
}

// heldBackSteps is what the held-back devices hs, more than one, take:
// those to reinstall first, so the action a short line keeps names them,
// then those to build again, to forget, to update by hand, and the rest, which
// update themselves.
func (m *merger) heldBackSteps(hs []TeamDevice) string {
	by := make([][]string, stepSelf+1)
	silent := false
	for _, h := range hs {
		st := m.heldBackStep(h)
		by[st] = append(by[st], h.Label)
		silent = silent || st == stepSelf && h.Silent
	}
	var steps []string
	if ns := by[stepReinstall]; len(ns) > 0 {
		steps = append(steps, "reinstall "+firstNames(ns, 3)+" with install.sh")
	}
	if ns := by[stepRebuild]; len(ns) > 0 {
		steps = append(steps, "build "+firstNames(ns, 3)+" again from source")
	}
	if ns := by[stepForget]; len(ns) > 0 {
		steps = append(steps, "forget "+firstNames(ns, 3)+" with ai-usage team forget-device")
	}
	if ns := by[stepUpdate]; len(ns) > 0 {
		steps = append(steps, "run ai-usage update on "+firstNames(ns, 3)+", whose update fails")
	}
	ns := by[stepSelf]
	then, it := "within 15 minutes", "within 15 minutes"
	if silent {
		then, it = "when they next run", "when it next runs"
	}
	switch {
	case len(ns) == 0:
	case len(steps) == 0:
		steps = append(steps, "they update themselves "+then+" ("+firstNames(ns, 3)+")")
	case len(ns) == 1:
		steps = append(steps, ns[0]+" updates itself "+it)
	default:
		steps = append(steps, "the other "+strconv.Itoa(len(ns))+" update themselves "+then)
	}
	return strings.Join(steps, "; ")
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
