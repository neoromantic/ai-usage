package collect

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// ExtSince is the first release that reads the snapshot fields v0.4.0
// added. An older one drops a document that has them as undecodable, so a
// device sends them only while the whole team reads them.
const ExtSince = "v0.4.0"

// CyclesSince is the first release that reads the quota cycles a snapshot
// carries. An older one drops a document that has them as undecodable, so a
// device sends them only while the whole team reads them.
const CyclesSince = "v0.5.0"

// HoursSince is the first release that reads the hours a snapshot carries.
// An older one drops a document that has them as undecodable, so a device
// sends them only while the whole team reads them.
const HoursSince = "v0.7.0"

// legacyDev is the version a build from source of a release before ExtSince
// reports. One from ExtSince on reports "devel", which does not parse as a
// release either.
const legacyDev = "dev"

// BeforeExt says a collector of version runs a release before ExtSince, or
// is a build from source of one: one that reports legacyDev, or a version
// with a suffix whose release is before ExtSince, as v0.0.0-local, which the
// docs of those releases said to build by hand. Any other version that is
// not a release, such as "devel", counts as new.
func BeforeExt(version string) bool { return before(version, ExtSince) }

// BeforeCycles says a collector of version runs a release before
// CyclesSince, or is a build from source of one, as BeforeExt tells. A
// build from source of a release from ExtSince on, and before CyclesSince,
// reports "devel" as a newer one does, so it counts as new.
func BeforeCycles(version string) bool { return before(version, CyclesSince) }

// BeforeHours says a collector of version runs a release before HoursSince,
// or is a build from source of one, as BeforeExt tells. A build from source
// of a release from ExtSince on, and before HoursSince, reports "devel" as
// a newer one does, so it counts as new.
func BeforeHours(version string) bool { return before(version, HoursSince) }

// before says a collector of version runs a release before since, or is a
// build from source of one.
func before(version, since string) bool {
	base, _, _ := strings.Cut(strings.TrimSpace(version), "-")
	base, _, _ = strings.Cut(base, "+")
	return version == legacyDev || selfupdate.Newer(since, base)
}

// Why the gate is closed.
const (
	GateTeam     = "team"     // the cached read is of another team, or of none
	GateStale    = "stale"    // the cached read is too old to vouch for the team
	GateRejected = "rejected" // the relay refused the fields not long ago
	GateOld      = "old"      // a device of the team runs a release before the one the gate waits for
)

// gateReadAge is how old a team read may be for the gate to go by it.
const gateReadAge = 3 * time.Hour

// Gate says whether this device may send the snapshot fields a release
// added: those ExtSince added, the cycles CyclesSince added, or the hours
// HoursSince added.
type Gate struct {
	Open bool
	// HeldBack is the other devices of the team on a release before the
	// one the gate waits for, by id, sorted.
	HeldBack []string
	// Reason is why the gate is closed: one of the Gate constants.
	Reason string
	// Until is, when the relay refused the fields, when to try them again.
	Until time.Time
}

// ExtendedWire is the gate for this device, self, in the team with
// fingerprint, as the cached team read c shows it, for the fields ExtSince
// added. It is open when that read is of this team, at most gateReadAge
// old, the relay has not refused the fields since noExtUntil was set, and
// every other device the read found or still remembers in Known runs
// ExtSince or later. A development build counts as new, unless BeforeExt
// tells it is one of an older release. Documents the read left out, as not
// verified or from a newer release, do not close it: no reader of this
// team's documents sent them.
func ExtendedWire(c TeamCache, fingerprint, self string, noExtUntil, now time.Time) Gate {
	return wireGate(c, fingerprint, self, noExtUntil, now, BeforeExt)
}

// CyclesWire is the gate for the quota cycles, as ExtendedWire is for the
// fields ExtSince added: every other device must run CyclesSince or later,
// as BeforeCycles tells, and the relay must not have refused them since
// noCyclesUntil was set.
func CyclesWire(c TeamCache, fingerprint, self string, noCyclesUntil, now time.Time) Gate {
	return wireGate(c, fingerprint, self, noCyclesUntil, now, BeforeCycles)
}

// HoursWire is the gate for the hours, as ExtendedWire is for the fields
// ExtSince added: every other device must run HoursSince or later, as
// BeforeHours tells, and the relay must not have refused them since
// noHoursUntil was set.
func HoursWire(c TeamCache, fingerprint, self string, noHoursUntil, now time.Time) Gate {
	return wireGate(c, fingerprint, self, noHoursUntil, now, BeforeHours)
}

// NoHoursUntil is when the relay's refusal of the hours, or of what an
// older release added, which a relay that refuses refuses the hours with,
// ends.
func NoHoursUntil(r state.Relay) time.Time {
	return latest(r.NoHoursUntil, NoCyclesUntil(r))
}

// NoCyclesUntil is when the relay's refusal of the cycles, or of the fields
// ExtSince added, which a relay that refuses refuses the cycles with, ends.
func NoCyclesUntil(r state.Relay) time.Time {
	return latest(r.NoCyclesUntil, r.NoExtUntil)
}

// latest is the later of a and b.
func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// wireGate is the gate ExtendedWire and CyclesWire describe, for the
// release old tells a device runs one before.
func wireGate(c TeamCache, fingerprint, self string, until, now time.Time, old func(string) bool) Gate {
	if c.Team == "" || c.Team != fingerprint {
		return Gate{Reason: GateTeam}
	}
	g := Gate{HeldBack: heldBack(c, self, old)}
	switch age := now.Sub(c.PulledAt); {
	case age < 0 || age > gateReadAge:
		g.Reason = GateStale
	case now.Before(until):
		g.Reason, g.Until = GateRejected, until
	case len(g.HeldBack) > 0:
		g.Reason = GateOld
	default:
		g.Open = true
	}
	return g
}

// heldBack is the devices other than self that c shows on a release old
// tells is too old, sorted.
func heldBack(c TeamCache, self string, old func(string) bool) []string {
	var out []string
	add := func(device, version string) {
		if device != self && old(version) {
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

// AccountBurn is what an account burned on the device whose snapshot,
// collected at collected, holds a: by UTC day, laid out as a.Days is; since
// each window in a.Recent began, by window name; and over all the days the
// device keeps. label is the account's opened label, which names the
// provider Hermes bills to.
//
// A snapshot from ExtSince on has the burn itself. Days its burn days were
// cut short of weigh as that account's tokens do on average. A snapshot from
// an earlier release has only input plus output, so every day and window
// weighs so, and estimated is true; an account that spent only cache puts
// all it burned on the day it was last active.
func AccountBurn(a snapshot.Account, provider, label string, collected time.Time) (days []int64, recent map[string]int64, total int64, estimated bool) {
	all := meter.TokensBurn(a.Tokens, provider, label)
	inOut := a.Tokens.InOut()
	weigh := func(n int64) int64 {
		if inOut <= 0 {
			return 0
		}
		return int64(math.Round(float64(n) * float64(all) / float64(inOut)))
	}
	recent = map[string]int64{}
	if len(a.BurnDays) > 0 || slices.ContainsFunc(a.Recent, func(r snapshot.Recent) bool { return r.Burn != 0 }) {
		days = slices.Clone(a.BurnDays)
		for i := len(days); i < len(a.Days); i++ {
			days = append(days, weigh(a.Days[i]))
		}
		for _, r := range a.Recent {
			recent[r.Window] = r.Burn
		}
		total = all
		if len(a.BurnDays) >= len(a.Days) {
			total = 0
			for _, n := range a.BurnDays {
				total += n
			}
		}
		return days, recent, total, false
	}
	if inOut <= 0 {
		if all <= 0 {
			return nil, recent, 0, false
		}
		day := 0
		if a.LastActiveAt != nil {
			day = int(dayNumber(collected) - dayNumber(*a.LastActiveAt))
		}
		if day >= 0 && day < snapshot.MaxDays {
			days = make([]int64, day+1)
			days[day] = all
		}
		for _, r := range a.Recent {
			if a.LastActiveAt != nil && !a.LastActiveAt.Before(r.Start) {
				recent[r.Window] = all
			}
		}
		return days, recent, all, true
	}
	for _, n := range a.Days {
		days = append(days, weigh(n))
	}
	for _, r := range a.Recent {
		recent[r.Window] = weigh(r.Tokens)
	}
	return days, recent, all, true
}
