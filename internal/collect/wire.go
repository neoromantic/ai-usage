package collect

import (
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// ExtSince is the first release that reads the snapshot fields v0.4.0
// added. An older one drops a document that has them as undecodable, so a
// device sends them only while the whole team reads them.
const ExtSince = "v0.4.0"

// Why the gate is closed.
const (
	GateTeam     = "team"     // the cached read is of another team, or of none
	GateStale    = "stale"    // the cached read is too old to vouch for the team
	GateRejected = "rejected" // the relay refused the fields not long ago
	GateOld      = "old"      // a device of the team runs a release before ExtSince
)

// gateReadAge is how old a team read may be for the gate to go by it.
const gateReadAge = 3 * time.Hour

// Gate says whether this device may send the snapshot fields ExtSince added.
type Gate struct {
	Open bool
	// HeldBack is the other devices of the team on a release before
	// ExtSince, by id, sorted.
	HeldBack []string
	// Reason is why the gate is closed: one of the Gate constants.
	Reason string
	// Until is, when the relay refused the fields, when to try them again.
	Until time.Time
}

// ExtendedWire is the gate for this device, self, in the team with
// fingerprint, as the cached team read c shows it. It is open when that read
// is of this team, at most gateReadAge old, the relay has not refused the
// fields since noExtUntil was set, and every other device the read found or
// still remembers in Known runs ExtSince or later. A development build
// counts as new. Documents the read left out, as not verified or from a
// newer release, do not close it: no reader of this team's documents sent
// them.
func ExtendedWire(c TeamCache, fingerprint, self string, noExtUntil, now time.Time) Gate {
	if c.Team == "" || c.Team != fingerprint {
		return Gate{Reason: GateTeam}
	}
	g := Gate{HeldBack: heldBack(c, self)}
	switch age := now.Sub(c.PulledAt); {
	case age < 0 || age > gateReadAge:
		g.Reason = GateStale
	case now.Before(noExtUntil):
		g.Reason, g.Until = GateRejected, noExtUntil
	case len(g.HeldBack) > 0:
		g.Reason = GateOld
	default:
		g.Open = true
	}
	return g
}

// heldBack is the devices other than self that c shows on a release before
// ExtSince, sorted.
func heldBack(c TeamCache, self string) []string {
	var out []string
	add := func(device, version string) {
		// Newer is false for a version that does not parse, such as a
		// development build's.
		if device != self && selfupdate.Newer(ExtSince, version) {
			out = append(out, device)
		}
	}
	for _, d := range c.Docs {
		add(d.Device, d.CollectorVersion)
	}
	for id, k := range c.Known {
		add(id, k.Version)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
