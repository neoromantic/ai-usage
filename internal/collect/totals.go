package collect

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
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
	Link      *state.Link
	QuotaFrom string
	usage
	Projects []ProjectTotals
	// Linked is what other providers spent through this account and are
	// assumed to have billed to it, over LinkedSessions of their sessions
	// (Hermes on this Codex login). It is counted in their Tokens, not here.
	Linked         snapshot.Tokens
	LinkedSessions int
}

type ProjectTotals struct {
	// Path is the project's folder, and Folders how many working folders
	// count under it.
	Path    string
	Folders int
	usage
	// Providers are the harnesses that used the project, most tokens first.
	// Only Projects fills it.
	Providers []string
}

// usage is an account's or a project's sessions and what they spent.
type usage struct {
	Sessions int
	Tokens   snapshot.Tokens
	// Hours is the input plus output tokens by the UTC hour they were spent
	// in, keyed by logs.HourOf.
	Hours      map[int64]int64
	LastActive time.Time
}

// add counts one session's share: its tokens, their hours, and when it last
// grew.
func (u *usage) add(tok snapshot.Tokens, hours map[int64]int64, last time.Time) {
	u.Sessions++
	u.Tokens = u.Tokens.Add(tok)
	u.Hours = logs.AddHours(u.Hours, hours)
	if last.After(u.LastActive) {
		u.LastActive = last
	}
}

// Totals sums the ledger per account and project, a working folder counting
// under its project in f.
func Totals(st *state.State, f Folders) []AccountTotals {
	byKey := map[string]*AccountTotals{}
	projects := map[string]*projectSums{}
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
			projects[k] = &projectSums{}
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
			last, hours := lastActive(s, label), labelHours(s, label)
			get(s.Provider, label).add(tok, hours, last)
			projects[state.Key(s.Provider, label)].add(f.of(s.Project), tok, hours, last)
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
		a.Projects = projects[k].list()
		if a.Sessions == 0 && a.Quota == nil && !a.Current && a.LinkedSessions == 0 {
			continue
		}
		out = append(out, *a)
	}
	SortAccounts(out)
	return out
}

// Projects sums the ledger per project over every account and harness on
// this device, a working folder counting under its project in f.
func Projects(st *state.State, f Folders) []ProjectTotals {
	var ps projectSums
	byProvider := map[string]map[string]int64{}
	for _, s := range st.Sessions {
		var tok snapshot.Tokens
		var hours map[int64]int64
		var last time.Time
		for label, t := range s.By {
			if t.Zero() {
				continue
			}
			tok = tok.Add(t)
			hours = logs.AddHours(hours, labelHours(s, label))
			if l := lastActive(s, label); l.After(last) {
				last = l
			}
		}
		if tok.Zero() {
			continue
		}
		p := ps.add(f.of(s.Project), tok, hours, last)
		if byProvider[p.Path] == nil {
			byProvider[p.Path] = map[string]int64{}
		}
		byProvider[p.Path][s.Provider] += tok.Total()
	}
	out := ps.list()
	for i := range out {
		p := &out[i]
		used := byProvider[p.Path]
		for prov := range used {
			p.Providers = append(p.Providers, prov)
		}
		slices.SortFunc(p.Providers, func(a, b string) int {
			return cmp.Or(cmp.Compare(used[b], used[a]), cmp.Compare(a, b))
		})
	}
	return out
}

// projectSums sums sessions per project, counting the working folders of
// each.
type projectSums struct {
	byPath  map[string]*ProjectTotals
	folders map[string]map[string]bool
}

// add counts one session's share, spent in the working folder dir, under
// dir's project.
func (ps *projectSums) add(dir Folder, tok snapshot.Tokens, hours map[int64]int64, last time.Time) *ProjectTotals {
	if ps.byPath == nil {
		ps.byPath, ps.folders = map[string]*ProjectTotals{}, map[string]map[string]bool{}
	}
	p := ps.byPath[dir.Project]
	if p == nil {
		p = &ProjectTotals{Path: dir.Project}
		ps.byPath[dir.Project] = p
		ps.folders[dir.Project] = map[string]bool{}
	}
	if !ps.folders[dir.Project][dir.Path] {
		ps.folders[dir.Project][dir.Path] = true
		p.Folders++
	}
	p.add(tok, hours, last)
	return p
}

// list is the projects, most tokens first.
func (ps *projectSums) list() []ProjectTotals {
	var out []ProjectTotals
	for _, p := range ps.byPath {
		out = append(out, *p)
	}
	sortProjects(out)
	return out
}

// sortProjects orders projects by tokens, most first, then by path.
func sortProjects(ps []ProjectTotals) {
	slices.SortFunc(ps, func(a, b ProjectTotals) int {
		return cmp.Or(cmp.Compare(b.Tokens.Total(), a.Tokens.Total()), strings.Compare(a.Path, b.Path))
	})
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
	slices.SortFunc(out, func(a, b AccountTotals) int {
		if a.Provider == b.Provider && a.Current != b.Current {
			if a.Current {
				return -1
			}
			return 1
		}
		return cmp.Or(
			cmp.Compare(slices.Index(snapshot.Providers, a.Provider), slices.Index(snapshot.Providers, b.Provider)),
			cmp.Compare(b.Tokens.Total(), a.Tokens.Total()),
			cmp.Compare(a.Label, b.Label),
		)
	})
}

// IsCurrent reports whether an account is logged in on any home now.
func IsCurrent(st *state.State, provider, label string) bool {
	for k, l := range st.Current {
		if l == label && state.SplitKey(k)[0] == provider {
			return true
		}
	}
	return false
}
