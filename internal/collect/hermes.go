package collect

import (
	"cmp"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// hermes labels each Hermes session read with the billing provider its row
// names, as its account, attributes the sessions' growth, and links the
// accounts to the logins they bill through. It returns the sessions, and the
// homes whose current account stays: Hermes keeps its current account under
// no home, and forgets it when no Hermes home is found.
func (s *sampler) hermes(homes []string, res logs.Result, partial bool) (read []readSession, probed []string) {
	for _, rs := range res.Sessions {
		label := rs.Account
		if label == "" {
			label = UnknownAccount
		}
		read = append(read, readSession{s: rs, label: label})
	}
	for _, r := range read {
		touchAccount(s.st, "hermes", r.label)
		grown := attribute(s.st, "hermes", r.s, r.label, partial, s.now, s.growth)
		for l := range grown {
			touchAccount(s.st, "hermes", l)
		}
		s.linkGrowth(r.s, grown)
	}
	markHermesCurrent(s.st, read)
	s.linkHermes(read, partial)
	if len(homes) > 0 {
		probed = []string{""}
	}
	return read, probed
}

// hermesLinks are the Hermes billing providers that log in to the same
// subscription as another harness, and that harness. Hermes keeps its own
// login for them, so the account is assumed, not read: the one logged in to
// the home Config.QuotaFrom names for the Hermes home, else to the harness's
// default home. Hermes never marks Anthropic usage as billed to a
// subscription, so it is not linked.
var hermesLinks = map[string]string{
	"openai-codex": "codex",
	"xai-oauth":    "grok",
}

// BillsThrough reports whether Hermes can bill a subscription through the
// login of harness p.
func BillsThrough(p string) bool {
	return HermesBilling(p) != ""
}

// HermesBilling is the Hermes billing provider that bills through the login
// of harness p, or "".
func HermesBilling(p string) string {
	for b, v := range hermesLinks {
		if v == p {
			return b
		}
	}
	return ""
}

// paths resolves symlinks in paths, once per path and run, so a home named
// by one path still matches when it is found by another.
type paths map[string]string

func (r paths) resolve(p string) string {
	if p == "" || r == nil {
		return p
	}
	if v, ok := r[p]; ok {
		return v
	}
	v := fsutil.RealPath(filepath.Clean(p))
	r[p] = v
	return v
}

// quotaLinks is Config.QuotaFrom keyed by resolved Hermes home.
func quotaLinks(named map[string]map[string]string, r paths) map[string]map[string]string {
	// Two spellings of one home merge in a fixed order, so the same entry
	// wins every run.
	out := map[string]map[string]string{}
	for _, h := range slices.Sorted(maps.Keys(named)) {
		k := r.resolve(h)
		for p, at := range named[h] {
			if out[k] == nil {
				out[k] = map[string]string{}
			}
			out[k][p] = at
		}
	}
	return out
}

// quotaHome is the home of harness p named for a Hermes home, or for the
// home a profile is kept in, or "". named is keyed by resolved home.
func quotaHome(named map[string]map[string]string, r paths, hermesHome, p string) string {
	if at := named[r.resolve(hermesHome)][p]; at != "" {
		return at
	}
	// A profile is in its home's profiles folder even when it is a link to
	// somewhere else, so the parent is taken from the path it was found by.
	if parent := filepath.Dir(filepath.Clean(hermesHome)); filepath.Base(parent) == profiles["hermes"].dir {
		return named[r.resolve(filepath.Dir(parent))][p]
	}
	return ""
}

// QuotaHomesOf is, by harness, the home named for a Hermes home or for the
// home a profile is kept in.
func QuotaHomesOf(named map[string]map[string]string, hermesHome string) map[string]string {
	r := paths{}
	links := quotaLinks(named, r)
	out := map[string]string{}
	for _, p := range snapshot.Providers {
		if at := quotaHome(links, r, hermesHome, p); at != "" {
			out[p] = at
		}
	}
	return out
}

// linkedAccount is the account a Hermes billing provider in the Hermes home
// is assumed to bill through: the one logged in now to the home the person
// named for it, or to the linked harness's default home.
func (s *sampler) linkedAccount(hermesHome, billing string) *state.Link {
	p, ok := hermesLinks[billing]
	if !ok {
		return nil
	}
	at := cmp.Or(quotaHome(s.quotaFrom, s.paths, hermesHome, p), probe.DefaultHome(s.UserHome, p))
	if at == "" {
		return nil
	}
	label := s.st.Current[state.Key(p, at)]
	if label == "" {
		// The home may be found under another path than it was named by.
		want := s.paths.resolve(at)
		for k, l := range s.st.Current {
			if parts := state.SplitKey(k); len(parts) == 2 && parts[0] == p && s.paths.resolve(parts[1]) == want {
				label = l
				break
			}
		}
	}
	if label == "" {
		return nil
	}
	return &state.Link{Provider: p, Label: label}
}

// linkHermes points each Hermes account billed through a subscription at the
// login most of its tokens read this run went through, or at none when
// nobody is logged in there. Homes can bill one route through different
// logins, as bots on a shared login beside the person's own Hermes; the
// account shows the quota of the login it uses most, which holds from run to
// run. An account with no session read this run keeps its link, and so does
// one whose homes were not all read: the rest would not be the majority.
func (s *sampler) linkHermes(read []readSession, partial bool) {
	seen := map[string]bool{}
	uses := map[string]map[state.Link]*use{}
	for _, r := range read {
		for billing, t := range partsOf(r.s, r.label) {
			seen[billing] = true
			link := s.linkedAccount(r.s.Home, billing)
			if link == nil {
				continue
			}
			addUse(uses, billing, *link, t.Total(), r.s.Updated)
		}
	}
	for _, acct := range s.st.Accounts {
		if acct.Provider != "hermes" {
			continue
		}
		if _, ok := hermesLinks[acct.Label]; !ok {
			acct.Link = nil
			continue
		}
		if !seen[acct.Label] || (partial && acct.Link != nil) {
			continue
		}
		acct.Link = mostUsed(uses[acct.Label])
	}
}

// use is how many tokens read this run went through one login, and the
// newest update of the sessions that sent them.
type use struct {
	tokens int64
	last   time.Time
}

// addUse adds tokens sent through link to billing's uses, with the update
// time of the session that sent them.
func addUse(uses map[string]map[state.Link]*use, billing string, link state.Link, tokens int64, updated time.Time) {
	if uses[billing] == nil {
		uses[billing] = map[state.Link]*use{}
	}
	u := uses[billing][link]
	if u == nil {
		u = &use{}
		uses[billing][link] = u
	}
	u.tokens += tokens
	if updated.After(u.last) {
		u.last = updated
	}
}

// mostUsed is the login the most tokens went through, then the one used
// last, then the first by label. It is nil when there is none.
func mostUsed(uses map[state.Link]*use) *state.Link {
	var best *state.Link
	var top *use
	for l, u := range uses {
		if top == nil || u.tokens > top.tokens ||
			(u.tokens == top.tokens && (u.last.After(top.last) || (u.last.Equal(top.last) && l.Label < best.Label))) {
			best, top = &l, u
		}
	}
	return best
}

// linkGrowth records a Hermes session's growth on each subscription against
// the account it is assumed to have used, so that account can show what
// Hermes spent on it.
func (s *sampler) linkGrowth(rs logs.Session, grown map[string]snapshot.Tokens) {
	for billing, g := range grown {
		link := s.linkedAccount(rs.Home, billing)
		if link == nil {
			continue
		}
		e := s.st.Sessions[state.Key("hermes", rs.ID)]
		if e.Via == nil {
			e.Via = map[string]snapshot.Tokens{}
		}
		k := state.Key(link.Provider, link.Label)
		e.Via[k] = e.Via[k].Add(g)
	}
}

// markHermesCurrent marks the billing provider of the newest Hermes session
// read this run as current. It is the account the session bills now, not
// whichever account earlier growth went to. A session whose row names no
// provider and holds no tokens of its own, as one Hermes made for auxiliary
// calls alone, does not say. With no sessions read, the last known one stays.
func markHermesCurrent(st *state.State, read []readSession) {
	var newest *readSession
	for i := range read {
		r := &read[i]
		if strings.TrimSpace(r.s.Account) == "" && r.s.Parts[r.s.Account].Zero() {
			continue
		}
		if newest == nil || r.s.Updated.After(newest.s.Updated) ||
			(r.s.Updated.Equal(newest.s.Updated) && r.s.ID > newest.s.ID) {
			newest = r
		}
	}
	if newest != nil {
		st.Current[state.Key("hermes", "")] = newest.label
	}
}
