package collect

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

// AccountTotals is one account's usage on this device over the retention window.
type AccountTotals struct {
	Provider string
	Label    string
	Plan     string
	Current  bool
	Quota    *state.Quota
	// Link is the account of another provider this one is assumed to bill
	// through (Hermes on a Codex or Grok subscription). When that account has
	// a quota reading, Quota is that same reading and QuotaFrom names its
	// provider.
	Link       *state.Link
	QuotaFrom  string
	Sessions   int
	Tokens     snapshot.Tokens
	LastActive time.Time
	Projects   []ProjectTotals
	// Linked is what other providers spent through this account and are
	// assumed to have billed to it, over LinkedSessions of their sessions
	// (Hermes on this Codex login). It is counted in their Tokens, not here.
	Linked         snapshot.Tokens
	LinkedSessions int
}

type ProjectTotals struct {
	Path     string
	Sessions int
	Tokens   snapshot.Tokens
}

// Totals sums the ledger per account and project.
func Totals(st *state.State) []AccountTotals {
	byKey := map[string]*AccountTotals{}
	projects := map[string]map[string]*ProjectTotals{}
	get := func(provider, label string) *AccountTotals {
		k := state.Key(provider, label)
		a := byKey[k]
		if a == nil {
			a = &AccountTotals{Provider: provider, Label: label, Current: IsCurrent(st, provider, label)}
			if acct := st.Accounts[k]; acct != nil {
				a.Plan, a.Quota = acct.Plan, acct.Quota
				linkQuota(st, a, acct.Link)
			}
			byKey[k] = a
			projects[k] = map[string]*ProjectTotals{}
		}
		return a
	}
	for _, acct := range st.Accounts {
		get(acct.Provider, acct.Label)
	}
	for _, s := range st.Sessions {
		for label, tok := range s.By {
			if tok.Zero() {
				continue
			}
			a := get(s.Provider, label)
			a.Sessions++
			a.Tokens = a.Tokens.Add(tok)
			if last := lastActive(s, label); last.After(a.LastActive) {
				a.LastActive = last
			}
			k := state.Key(s.Provider, label)
			p := projects[k][s.Project]
			if p == nil {
				p = &ProjectTotals{Path: s.Project}
				projects[k][s.Project] = p
			}
			p.Sessions++
			p.Tokens = p.Tokens.Add(tok)
		}
		for k, tok := range s.Via {
			parts := state.SplitKey(k)
			if len(parts) != 2 || tok.Zero() {
				continue
			}
			a := get(parts[0], parts[1])
			a.LinkedSessions++
			a.Linked = a.Linked.Add(tok)
		}
	}
	out := make([]AccountTotals, 0, len(byKey))
	for k, a := range byKey {
		for _, p := range projects[k] {
			a.Projects = append(a.Projects, *p)
		}
		sort.Slice(a.Projects, func(i, j int) bool {
			if a.Projects[i].Tokens.Total() != a.Projects[j].Tokens.Total() {
				return a.Projects[i].Tokens.Total() > a.Projects[j].Tokens.Total()
			}
			return a.Projects[i].Path < a.Projects[j].Path
		})
		if a.Sessions == 0 && a.Quota == nil && !a.Current && a.LinkedSessions == 0 {
			continue
		}
		out = append(out, *a)
	}
	SortAccounts(out)
	return out
}

// linkQuota shows the reading of the account a bills through, unchanged, when
// a has none of its own. With no reading there, a's quota stays unknown.
func linkQuota(st *state.State, a *AccountTotals, link *state.Link) {
	if link == nil {
		return
	}
	a.Link = link
	if a.Quota != nil {
		return
	}
	if to := st.Accounts[state.Key(link.Provider, link.Label)]; to != nil && to.Quota != nil {
		a.Quota, a.QuotaFrom = to.Quota, link.Provider
	}
}

// lastActive is when label's share of a session last grew. A ledger from
// before that was tracked has only the session's newest activity.
func lastActive(s *state.Session, label string) time.Time {
	if t, ok := s.Last[label]; ok {
		return t
	}
	return s.Updated
}

// SortAccounts orders by provider, then logged-in accounts, then tokens.
func SortAccounts(out []AccountTotals) {
	rank := map[string]int{}
	for i, p := range Providers {
		rank[p] = i
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Provider != b.Provider {
			return rank[a.Provider] < rank[b.Provider]
		}
		if a.Current != b.Current {
			return a.Current
		}
		if a.Tokens.Total() != b.Tokens.Total() {
			return a.Tokens.Total() > b.Tokens.Total()
		}
		return a.Label < b.Label
	})
}

// maxSealedPlain keeps a sealed string under snapshot.MaxSealed.
const maxSealedPlain = 300

// BuildDoc makes this device's snapshot with every name and path sealed.
func BuildDoc(st *state.State, key *team.Key, device, hostname, osUser, version string, now time.Time) snapshot.Doc {
	seal := func(s string) string { return key.Seal(clip(s, maxSealedPlain)) }
	doc := snapshot.Doc{
		V:                snapshot.Version,
		Team:             key.Fingerprint(),
		Device:           device,
		DeviceLabel:      seal(orUnknown(hostname)),
		OSUser:           seal(orUnknown(osUser)),
		CollectorVersion: orUnknown(snapshot.PlainLabel(version)),
		CollectedAt:      now,
		LastSuccessAt:    st.LastSuccessAt,
		LastError:        seal(st.LastError),
		Accounts:         []snapshot.Account{},
		Sources:          []snapshot.Source{},
	}
	for _, p := range Providers {
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
		}
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
		doc.Accounts = append(doc.Accounts, sa)
	}
	fitDoc(&doc)
	return doc
}

// fitDoc drops the smallest projects, then the smallest accounts, until the
// document is under the relay's size limit.
func fitDoc(doc *snapshot.Doc) {
	for {
		b, err := json.Marshal(doc)
		if err != nil || len(b) <= snapshot.MaxBytes {
			return
		}
		longest := -1
		for i, a := range doc.Accounts {
			if len(a.Projects) > 0 && (longest < 0 || len(a.Projects) > len(doc.Accounts[longest].Projects)) {
				longest = i
			}
		}
		if longest >= 0 {
			ps := doc.Accounts[longest].Projects
			doc.Accounts[longest].Projects = ps[:len(ps)-1]
			continue
		}
		if len(doc.Accounts) == 0 {
			return
		}
		doc.Accounts = doc.Accounts[:len(doc.Accounts)-1]
	}
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

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
