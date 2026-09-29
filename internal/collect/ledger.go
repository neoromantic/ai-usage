package collect

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// claimUnknown gives the account a home first names the usage counted there
// while that home had never answered, as the first run gives an account the
// history before it. Until then, as when an old binary cannot answer, that
// usage is unknown rather than someone else's. A home that said nobody is
// logged in has answered: what grew there then is no one's, and stays so.
// Each session read this run is claimed by the home it was read from, or,
// for a file several homes hold, by the account servedBy names when a home
// logged in to it claims this run. A home whose logs were not all read this
// run claims at a later one, since the sessions it missed would stay unknown.
// labels are this run's accounts by home.
func claimUnknown(st *state.State, p string, homes []string, answers []answer, res logs.Result, labels map[string]string) {
	claim := claims(st, p, homes, answers, res)
	if len(claim) == 0 {
		return
	}
	for _, rs := range res.Sessions {
		label, ok := claim[rs.Home]
		if l := servedBy(st, p, rs, labels); l != "" {
			label, ok = l, slices.ContainsFunc(rs.Homes, func(h string) bool { return claim[h] == l })
		}
		if !ok {
			continue
		}
		s := st.Sessions[state.Key(p, rs.ID)]
		if s == nil {
			continue
		}
		moveShare(s, UnknownAccount, label)
	}
}

// claims records the homes of p that answer for the first time, and returns
// the account each one that claims this run names, by home.
func claims(st *state.State, p string, homes []string, answers []answer, res logs.Result) map[string]string {
	claim := map[string]string{}
	for i, home := range homes {
		label := strings.TrimSpace(answers[i].reading.Account)
		if label == "" && !errors.Is(answers[i].err, probe.ErrNotLoggedIn) {
			continue
		}
		k := state.Key(p, home)
		if st.Answered == nil {
			st.Answered = map[string]bool{}
		}
		if st.Answered[k] {
			continue
		}
		if label != "" && label != UnknownAccount {
			if hr := res.Homes[home]; hr.Err != nil || hr.Unreadable > 0 {
				continue // It claims at a run that reads them all.
			}
			claim[home] = label
		}
		st.Answered[k] = true
	}
	return claim
}

// moveShare moves from's share of a session onto to: its tokens, its hours
// and its last write.
func moveShare(s *state.Session, from, to string) {
	t, ok := s.By[from]
	if !ok {
		return
	}
	s.By[to] = s.By[to].Add(t)
	delete(s.By, from)
	if h, ok := s.UB[from]; ok {
		s.UB[to] = meter.AddHours(s.UB[to], h)
		delete(s.UB, from)
	}
	if last, ok := s.Last[from]; ok {
		if last.After(s.Last[to]) {
			s.Last[to] = last
		}
		delete(s.Last, from)
	}
}

func touchAccount(st *state.State, p, label string) *state.Account {
	k := state.Key(p, label)
	acct := st.Accounts[k]
	if acct == nil {
		acct = &state.Account{Provider: p, Label: label}
		st.Accounts[k] = acct
	}
	return acct
}

// attribute moves a session's growth since the last run onto label, or onto
// each part's account when the log splits the session by account. It returns
// the growth by account.
//
// The ledger remembers what it saw of a split session by the route each part
// names, and credits a part on an auxiliary route to the route its calls bill
// to now. A route that overtakes another as the session's main one then takes
// only the auxiliary calls made after, never what was counted before.
func attribute(st *state.State, p string, s logs.Session, label string, partial bool, now time.Time, growth map[string]snapshot.Tokens) map[string]snapshot.Tokens {
	k := state.Key(p, s.ID)
	e := st.Sessions[k]
	if e == nil {
		e = &state.Session{Provider: p, By: map[string]snapshot.Tokens{}}
		st.Sessions[k] = e
	}
	if e.By == nil {
		e.By = map[string]snapshot.Tokens{}
	}
	grown := map[string]snapshot.Tokens{}
	if s.Parts == nil {
		g := s.Tokens.Growth(e.Seen)
		e.Seen = seenNow(e.Seen, s.Tokens, g, partial)
		grown[label] = g
	} else {
		if e.Parts == nil {
			e.Parts = map[string]snapshot.Tokens{}
		}
		seen, to := seenParts(s), auxBillsTo(s, label)
		if !partial {
			relabelAux(e, seen, to)
		}
		for r, t := range seen {
			g := t.Growth(e.Parts[r])
			e.Parts[r] = seenNow(e.Parts[r], t, g, partial)
			grown[billedTo(r, to)] = grown[billedTo(r, to)].Add(g)
		}
		e.Seen = snapshot.Tokens{}
		for _, t := range e.Parts {
			e.Seen = e.Seen.Add(t)
		}
	}
	e.Project = s.Project
	if !partial {
		e.Side, e.Nulls = s.Side, s.Nulls
	}
	updated := s.Updated
	if updated.IsZero() || updated.After(now) {
		updated = now
	}
	for l, g := range grown {
		if g.Zero() {
			delete(grown, l)
		}
	}
	if p == "hermes" && len(e.U) > 0 && !e.Rederive {
		// Hermes' hours spread a session's running totals over its span and
		// replies. What it grew since the ledger last saw it was spent since,
		// not in the hours the spread gives each run a little more of.
		s.Hours = s.Hours.Since(logs.HourOf(e.Updated))
	}
	placeHours(e, s, grown, updated, now)
	if updated.After(e.Updated) {
		e.Updated = updated.UTC()
	}
	for l, g := range grown {
		e.By[l] = e.By[l].Add(g)
		if e.Last == nil {
			e.Last = map[string]time.Time{}
		}
		if updated.After(e.Last[l]) {
			e.Last[l] = updated.UTC()
		}
		ak := state.Key(p, l)
		growth[ak] = growth[ak].Add(g)
	}
	return grown
}

// partsOf is a session's tokens by the account each part bills, label being
// the session's own. A part on an auxiliary route bills to auxBillsTo.
func partsOf(s logs.Session, label string) map[string]snapshot.Tokens {
	to := auxBillsTo(s, label)
	parts := map[string]snapshot.Tokens{}
	for r, t := range seenParts(s) {
		parts[billedTo(r, to)] = parts[billedTo(r, to)].Add(t)
	}
	return parts
}

// seenParts is a session's tokens by the route each part names, as the
// ledger remembers them. A part that names no route, as a row from before
// Hermes recorded billing, is the route of the session's own row.
func seenParts(s logs.Session) map[string]snapshot.Tokens {
	own := cmp.Or(strings.TrimSpace(s.Account), UnknownAccount)
	parts := map[string]snapshot.Tokens{}
	for r, t := range s.Parts {
		r = cmp.Or(strings.TrimSpace(r), own)
		parts[r] = parts[r].Add(t)
	}
	return parts
}

// billedTo is the account a part on route r bills: r, or to for an
// auxiliary route.
func billedTo(r, to string) string {
	if auxRoute(r) {
		return to
	}
	return r
}

// relabelAux moves what the ledger credited to an auxiliary route onto the
// account those calls bill to now, to: its tokens, hours and last growth. A
// ledger from before auxiliary calls billed to the session's main route has
// them apart, and a count it saw under a route this read no longer names
// was read since under to, so it is to's, or it would count again there.
func relabelAux(e *state.Session, seen map[string]snapshot.Tokens, to string) {
	for r, t := range e.Parts {
		if _, ok := seen[r]; !ok && r != to && auxRoute(r) {
			e.Parts[to] = e.Parts[to].Add(t)
			delete(e.Parts, r)
		}
	}
	for l := range e.By {
		if l != to && auxRoute(l) {
			moveShare(e, l, to)
		}
	}
}

// seenNow is the count to remember after a read that found cur, g past prev.
// A partial read keeps the higher count, or the part that could not be read
// would be counted again when it reads next time.
func seenNow(prev, cur, g snapshot.Tokens, partial bool) snapshot.Tokens {
	if partial {
		return prev.Add(g)
	}
	return cur
}

// servedBy is the account that served a session whose log file several homes
// hold, as Orca links each Codex rollout into every account's home. The
// file's newest rate-limit reading is the serving account's: a weekly window
// resets at a time fixed per account, so the one account logged in at those
// homes whose quota resets at the same time served it. With none or more
// than one such account, it returns "" and the session stays with its first
// home, the default home when it holds the file.
func servedBy(st *state.State, p string, s logs.Session, labels map[string]string) string {
	if len(s.Homes) < 2 || s.Limits == nil {
		return ""
	}
	found := ""
	for _, h := range s.Homes {
		l := labels[h]
		if l == "" || l == UnknownAccount || l == found {
			continue
		}
		acct := st.Accounts[state.Key(p, l)]
		if acct == nil || acct.Quota == nil || !sameReset(acct.Quota.Windows, s.Limits.Windows) {
			continue
		}
		if found != "" {
			return ""
		}
		found = l
	}
	return found
}

// resetSkew is how far apart two readings of one window's reset time may be.
const resetSkew = time.Minute

// sameReset reports whether the longest window of the log reading resets when
// the window of the same length in the quota does.
func sameReset(quota, log []snapshot.Window) bool {
	var longest *snapshot.Window
	for i, w := range log {
		if w.ResetsAt != nil && w.Minutes > 0 && (longest == nil || w.Minutes > longest.Minutes) {
			longest = &log[i]
		}
	}
	if longest == nil {
		return false
	}
	for _, w := range quota {
		if w.Minutes == longest.Minutes && w.ResetsAt != nil {
			d := w.ResetsAt.Sub(*longest.ResetsAt)
			return d <= resetSkew && d >= -resetSkew
		}
	}
	return false
}

// prune drops sessions and idle accounts older than the retention window.
func prune(st *state.State, now time.Time) {
	cutoff := now.Add(-state.Retention)
	used := map[string]bool{}
	for k, s := range st.Sessions {
		if s.Updated.Before(cutoff) {
			delete(st.Sessions, k)
			continue
		}
		first := firstKept(cutoff)
		s.U = s.U.Since(first)
		for l, hours := range s.UB {
			s.UB[l] = keptHours(hours, first)
		}
		for l := range s.By {
			// An account's share of a session another account continued
			// ages out on its own. The session stays for its counts.
			if last, ok := s.Last[l]; ok && last.Before(cutoff) {
				dropShare(s, l)
				continue
			}
			used[state.Key(s.Provider, l)] = true
		}
		for via := range s.Via {
			used[via] = true
		}
	}
	pruneAccounts(st, used, cutoff)
}

// dropShare drops label's share of a session: its tokens, its last write and
// its hours, which leave the session's hours too.
func dropShare(s *state.Session, label string) {
	delete(s.By, label)
	delete(s.Last, label)
	for h, n := range s.UB[label] {
		if left := s.U[h].Sub(n); left.Zero() {
			delete(s.U, h)
		} else {
			s.U[h] = left
		}
	}
	delete(s.UB, label)
}

// pruneAccounts drops the accounts that no kept session uses and nobody is
// logged in to, when neither they nor their quota were seen since cutoff.
func pruneAccounts(st *state.State, used map[string]bool, cutoff time.Time) {
	current := map[string]bool{}
	for k, l := range st.Current {
		current[state.Key(state.SplitKey(k)[0], l)] = true
	}
	for k, a := range st.Accounts {
		if used[k] || current[k] {
			continue
		}
		last := a.LastSeenAt
		if a.Quota != nil && a.Quota.At.After(last) {
			last = a.Quota.At
		}
		if last.Before(cutoff) {
			delete(st.Accounts, k)
		}
	}
}
