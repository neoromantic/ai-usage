package collect

import (
	"cmp"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

// recent is this device's tokens on an account, and what they burn, since
// each window of its reading began, for the windows that had not reset at
// now.
func recent(hours, burn map[int64]int64, ws []snapshot.Window, now time.Time) []snapshot.Recent {
	var out []snapshot.Recent
	for _, w := range ws {
		start, ok := w.Start()
		if !ok || !w.ResetsAt.After(now) || len(out) == snapshot.MaxWindows {
			continue
		}
		out = append(out, snapshot.Recent{Window: w.Name, Start: start.UTC(), Tokens: SinceStart(hours, start), Burn: SinceStart(burn, start)})
	}
	return out
}

// maxSealedPlain keeps a sealed string under snapshot.MaxSealed.
const maxSealedPlain = 300

// maxUpdateError is the most of a failed release check's error last_error
// carries.
const maxUpdateError = 160

// lastError is what last_error carries: the last run's error on one line,
// then, while the last release check failed and no newer release waits for
// the next run, snapshot.UpdateLine and why, without the IP addresses a
// network error names. Sealing keeps the end of a long one, so the check's
// error stays whole.
func lastError(st *state.State, version string) string {
	s := strings.ReplaceAll(st.LastError, "\n", " ")
	if e := st.Update.Error; e != "" && !selfupdate.Dev(version) && !selfupdate.Newer(st.Update.Installed, version) {
		e = strings.Join(strings.Fields(selfupdate.WithoutAddresses(snapshot.Printable(e))), " ")
		s += snapshot.UpdateLine + snapshot.Truncate(e, maxUpdateError)
	}
	return s
}

// BuildDoc makes this device's snapshot with every name and path sealed,
// and each account's quota cycles. It has the fields ExtSince and
// CyclesSince added; Legacy and WithoutCycles are the bodies releases
// before them read.
func BuildDoc(st *state.State, cycles Cycles, key *team.Key, cfg state.Config, hostname, osUser, version string, now time.Time) snapshot.Doc {
	seal := func(s string) string { return key.Seal(clip(s, maxSealedPlain)) }
	doc := snapshot.Doc{
		V:                snapshot.Version,
		Team:             key.Fingerprint(),
		Device:           cfg.Device,
		DeviceLabel:      seal(cmp.Or(hostname, "unknown")),
		OSUser:           seal(cmp.Or(osUser, "unknown")),
		CollectorVersion: cmp.Or(snapshot.PlainLabel(version), "unknown"),
		CollectedAt:      now,
		LastSuccessAt:    st.LastSuccessAt,
		LastError:        seal(lastError(st, version)),
		Accounts:         []snapshot.Account{},
		Sources:          []snapshot.Source{},
	}
	// A last_error that no run dated goes without a time, and a time ahead
	// of this snapshot's, from a clock that went back, is its time.
	if st.LastError != "" && !st.LastErrorAt.IsZero() {
		at := st.LastErrorAt
		if at.After(now) {
			at = now
		}
		at = at.UTC()
		doc.LastErrorAt = &at
	}
	for _, p := range snapshot.Providers {
		src, ok := st.Sources[p]
		if !ok {
			continue
		}
		doc.Sources = append(doc.Sources, snapshot.Source{Provider: p, Status: src.Status, Error: seal(src.Error)})
	}
	// The snapshot keeps each working folder's usage, as the relay protocol
	// has it: no view reads another device's projects.
	for _, a := range Totals(st, nil) {
		if len(doc.Accounts) == snapshot.MaxAccounts {
			break
		}
		sa := snapshot.Account{
			Provider: a.Provider,
			Label:    seal(a.Label),
			Current:  a.Current,
			Plan:     a.Plan,
			Windows:  []snapshot.Window{},
			Sessions: a.Sessions,
			Tokens:   a.Tokens,
			Projects: []snapshot.Project{},
		}
		if a.Quota != nil && len(a.Quota.Windows) > 0 {
			at := a.Quota.At
			sa.QuotaAt = &at
			sa.QuotaFrom = a.QuotaFrom
			sa.Windows = append(sa.Windows, a.Quota.Windows...)
			sa.Recent = recent(a.Hours.Series(meter.Use.InOut), a.Burn, a.Quota.Windows, now)
		}
		sa.Days = DaysOf(a.Hours.Series(meter.Use.InOut), now)
		sa.BurnDays = DaysOf(a.Burn, now)
		sa.Cycles = slices.Clone(cycles[HistoryKey(a.Provider, a.Label)])
		if !a.LastActive.IsZero() {
			la := a.LastActive
			sa.LastActiveAt = &la
		}
		for _, p := range a.Projects {
			if len(sa.Projects) == snapshot.MaxProjects {
				break
			}
			sa.Projects = append(sa.Projects, snapshot.Project{Path: seal(p.Path), Sessions: p.Sessions, Tokens: p.Tokens})
		}
		// Only Hermes bills through another harness's login, by one route
		// per harness.
		if b := HermesBilling(a.Provider); b != "" && a.LinkedSessions > 0 {
			sa.Linked = []snapshot.Linked{{Provider: "hermes", Label: seal(b), Sessions: a.LinkedSessions, Tokens: a.Linked}}
		}
		doc.Accounts = append(doc.Accounts, sa)
	}
	doc.Aliases = aliases(cfg.Aliases, seal)
	fitDoc(&doc)
	return doc
}

// aliases are the short names this device gave accounts, newest first, as
// many as a snapshot holds.
func aliases(set map[string]state.Alias, seal func(string) string) []snapshot.Alias {
	var out []snapshot.Alias
	for k, a := range set {
		parts := state.SplitKey(k)
		if len(parts) != 2 || parts[1] == "" || a.At.IsZero() || !snapshot.KnownProvider(parts[0]) {
			continue
		}
		sa := snapshot.Alias{Provider: parts[0], Label: parts[1], Name: a.Name, At: a.At.UTC()}
		out = append(out, sa)
	}
	slices.SortFunc(out, func(a, b snapshot.Alias) int {
		return cmp.Or(b.At.Compare(a.At), cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Label, b.Label))
	})
	if len(out) > snapshot.MaxAliases {
		out = out[:snapshot.MaxAliases]
	}
	for i := range out {
		out[i].Label = seal(out[i].Label)
		if out[i].Name != "" {
			out[i].Name = seal(out[i].Name)
		}
	}
	return out
}

// fitDoc fits the document under the relay's size limit. The body a
// release before CyclesSince reads is fitted first, as such a release fits
// its own: fitExtended. The cycles of the accounts it kept then go back, and
// only they are cut further, the oldest of the window with the most first,
// while the whole is too big.
func fitDoc(doc *snapshot.Doc) {
	full := *doc
	*doc = full.WithoutCycles()
	fitExtended(doc)
	accts := doc.Accounts
	for i := range accts {
		// Accounts go from the end, so the kept ones are a prefix.
		accts[i].Cycles = slices.Clone(full.Accounts[i].Cycles)
	}
	for {
		b, err := json.Marshal(doc)
		if err != nil || len(b) <= snapshot.MaxBytes {
			return
		}
		// Marshal once for as many cycles as the excess takes.
		for over := len(b) - snapshot.MaxBytes; over > 0; {
			i := longest(accts, func(a snapshot.Account) int { return len(a.Cycles) }, 0)
			if i < 0 {
				return
			}
			cs := accts[i].Cycles
			j := oldestOfMost(cs)
			over -= cycleBytes(cs[j])
			if cs = slices.Delete(cs, j, j+1); len(cs) == 0 {
				cs, over = nil, over-len(`,"cycles":[]`)
			}
			accts[i].Cycles = cs
		}
	}
}

// fitExtended fits the document under the relay's size limit as a release
// from ExtSince on, and before CyclesSince, fits its own. The body a
// release before ExtSince reads is fitted first, as such a release fits its
// own, so the days it shows stay as long as they were: fitLegacy. The burn
// days are then cut to the days each account kept, and only they are cut
// further, oldest first, while the whole is too big. Should the fields
// ExtSince added still not fit, the document goes without them.
func fitExtended(doc *snapshot.Doc) {
	full := *doc
	*doc = full.Legacy()
	fitLegacy(doc)
	accts := doc.Accounts
	for i := range accts {
		a := &accts[i]
		// Accounts go from the end, so the kept ones are a prefix.
		from := full.Accounts[i]
		for j := range a.Recent {
			a.Recent[j].Burn = from.Recent[j].Burn
		}
		a.BurnDays = slices.Clone(from.BurnDays)
		if len(a.Days) < len(from.Days) {
			a.BurnDays = trimDays(a.BurnDays, len(a.Days))
		}
	}
	doc.LastErrorAt = full.LastErrorAt
	for {
		b, err := json.Marshal(doc)
		if err != nil || len(b) <= snapshot.MaxBytes {
			return
		}
		// Marshal once for as many days as the excess takes.
		for over := len(b) - snapshot.MaxBytes; over > 0; {
			i := longest(accts, func(a snapshot.Account) int { return len(a.BurnDays) }, 0)
			if i < 0 {
				*doc = doc.Legacy()
				return
			}
			was := daysBytes(accts[i].BurnDays)
			accts[i].BurnDays = trimDays(accts[i].BurnDays, len(accts[i].BurnDays)-1)
			over -= was - daysBytes(accts[i].BurnDays)
		}
	}
}

// daysBytes is how many bytes days take in an account's JSON, with the key
// omitempty leaves out when there are none.
func daysBytes(days []int64) int {
	if len(days) == 0 {
		return 0
	}
	n := len(`,"burn_days":[]`) + len(days) - 1
	for _, d := range days {
		n += len(strconv.FormatInt(d, 10))
	}
	return n
}

// trimDays is the first n of days without the zeros that would end them,
// and nil when none is left.
func trimDays(days []int64, n int) []int64 {
	days = days[:min(n, len(days))]
	for len(days) > 0 && days[len(days)-1] == 0 {
		days = days[:len(days)-1]
	}
	if len(days) == 0 {
		return nil
	}
	return days
}

// fitLegacy drops the smallest projects, then the oldest days, then the
// smallest accounts, until the document is under the relay's size limit.
// The report shows no other device's projects, and the device matrix needs
// the days. It is how v0.3.4 fits a document.
func fitLegacy(doc *snapshot.Doc) {
	for {
		b, err := json.Marshal(doc)
		if err != nil || len(b) <= snapshot.MaxBytes {
			return
		}
		if i := longest(doc.Accounts, func(a snapshot.Account) int { return len(a.Projects) }, 0); i >= 0 {
			ps := doc.Accounts[i].Projects
			doc.Accounts[i].Projects = ps[:len(ps)-1]
			continue
		}
		if i := longest(doc.Accounts, func(a snapshot.Account) int { return len(a.Days) }, 7); i >= 0 {
			ds := doc.Accounts[i].Days
			doc.Accounts[i].Days = ds[:len(ds)-1]
			continue
		}
		if len(doc.Accounts) == 0 {
			return
		}
		doc.Accounts = doc.Accounts[:len(doc.Accounts)-1]
	}
}

// longest returns the index of the first account with the largest n above
// above, or -1 when no account's n is above it.
func longest(accts []snapshot.Account, n func(snapshot.Account) int, above int) int {
	at, most := -1, above
	for i, a := range accts {
		if k := n(a); k > most {
			at, most = i, k
		}
	}
	return at
}

// clip keeps the end of a long string, where a path's distinctive part is.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := len(s) - n + len("…")
	for cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut++
	}
	return "…" + s[cut:]
}
