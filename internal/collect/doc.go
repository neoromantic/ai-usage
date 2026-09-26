package collect

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

// recent is this device's tokens on an account since each window of its
// reading began, for the windows that had not reset at now.
func recent(hours map[int64]int64, ws []snapshot.Window, now time.Time) []snapshot.Recent {
	var out []snapshot.Recent
	for _, w := range ws {
		start, ok := w.Start()
		if !ok || !w.ResetsAt.After(now) || len(out) == snapshot.MaxWindows {
			continue
		}
		out = append(out, snapshot.Recent{Window: w.Name, Start: start.UTC(), Tokens: SinceStart(hours, start)})
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

// BuildDoc makes this device's snapshot with every name and path sealed.
func BuildDoc(st *state.State, key *team.Key, cfg state.Config, hostname, osUser, version string, now time.Time) snapshot.Doc {
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
	for _, p := range snapshot.Providers {
		src, ok := st.Sources[p]
		if !ok {
			continue
		}
		doc.Sources = append(doc.Sources, snapshot.Source{Provider: p, Status: src.Status, Error: seal(src.Error)})
	}
	for _, a := range Totals(st) {
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
			sa.Recent = recent(a.Hours, a.Quota.Windows, now)
		}
		sa.Days = DaysOf(a.Hours, now)
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

// fitDoc drops the smallest projects, then the oldest days, then the
// smallest accounts, until the document is under the relay's size limit.
// The report shows no other device's projects, and the device matrix needs
// the days.
func fitDoc(doc *snapshot.Doc) {
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
