package collect

import (
	"cmp"
	"errors"
	"maps"
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
	ids := accountIDs(res)
	for _, rs := range res.Sessions {
		label, ok := claim[rs.Home]
		if l := servedBy(st, p, rs, labels, ids); l != "" {
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
	for _, id := range s.Folded {
		if f := st.Sessions[state.Key(p, id)]; f != nil && f != e {
			fold(e, f)
			delete(st.Sessions, state.Key(p, id))
		}
	}
	grown := map[string]snapshot.Tokens{}
	if s.Parts == nil {
		g := s.Tokens.Growth(e.Seen)
		if p == "codex" && !partial {
			shrink(e, s, now)
		}
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
	// A full read of a Claude session dates it by its last message, which
	// can be before the file's time an earlier release dated it by.
	if updated.After(e.Updated) || (p == "claude" && !partial) {
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

// fold moves what the ledger kept for f, a session an earlier read took as
// a root of its own, onto e, the session a read now finds it inside: its
// counts, shares, hours and times. Growth is then only what grew past both,
// and what both counted shows as a count the log no longer holds, which
// shrink takes back.
func fold(e, f *state.Session) {
	labels := map[string]bool{}
	for l := range e.By {
		labels[l] = true
	}
	for l := range f.By {
		labels[l] = true
	}
	if e.UB != nil || f.UB != nil || len(labels) > 1 {
		ub := map[string]meter.Hours{}
		for _, s := range []*state.Session{e, f} {
			by := s.UB
			if by == nil {
				by = accountHours(s)
			}
			for l, h := range by {
				ub[l] = meter.AddHours(ub[l], h)
			}
		}
		for l := range labels {
			if ub[l] == nil {
				ub[l] = meter.Hours{}
			}
		}
		e.UB = ub
	}
	e.U = meter.AddHours(e.U, f.U)
	e.Seen = e.Seen.Add(f.Seen)
	for l, t := range f.By {
		e.By[l] = e.By[l].Add(t)
	}
	e.Via = addCounts(e.Via, f.Via)
	e.Parts = addCounts(e.Parts, f.Parts)
	for l, at := range f.Last {
		if e.Last == nil {
			e.Last = map[string]time.Time{}
		}
		if at.After(e.Last[l]) {
			e.Last[l] = at
		}
	}
	if f.Updated.After(e.Updated) {
		e.Updated = f.Updated
	}
	e.Rederive = e.Rederive || f.Rederive
}

// addCounts adds b's counts onto a's, making a when needed.
func addCounts(a, b map[string]snapshot.Tokens) map[string]snapshot.Tokens {
	for k, t := range b {
		if a == nil {
			a = map[string]snapshot.Tokens{}
		}
		a[k] = a[k].Add(t)
	}
	return a
}

// shrink takes back what the ledger counted of a Codex session beyond what a
// full read of its log now holds, as when a fork's replayed history, which
// its source counts, or a sub-agent counted both in its family and as a
// session of its own, is no longer counted twice. It comes off the account
// that grew last, from the hours the ledger keeps where the log shows less
// than the ledger placed, the newest first. A count that fell because a file
// aged out of the window was spent in hours the ledger no longer keeps, and
// stays counted.
func shrink(e *state.Session, s logs.Session, now time.Time) {
	over := meter.FromTokens(e.Seen.Growth(s.Tokens))
	if over.Zero() || len(e.By) == 0 {
		return
	}
	l := lastGrown(e)
	first := firstKept(now.Add(-state.Retention))
	excess := meter.Hours{}
	for h, u := range e.U {
		if h < first {
			continue
		}
		x := u.Sub(s.Hours[h])
		if e.UB != nil {
			x = minUse(x, e.UB[l][h])
		}
		if !x.Zero() {
			excess[h] = x
		}
	}
	taken := excess.TakeNewest(minUse(over, excess.Sum()))
	if len(taken) == 0 {
		return
	}
	takeHours(e.U, taken)
	if e.UB != nil {
		takeHours(e.UB[l], taken)
	}
	e.By[l] = meter.FromTokens(e.By[l]).Sub(taken.Sum()).Tokens()
}

// lastGrown is the account whose share of a session grew last, the first by
// name on a tie or where none has a time.
func lastGrown(e *state.Session) string {
	labels := slices.Sorted(maps.Keys(e.By))
	best := labels[0]
	for _, l := range labels[1:] {
		if e.Last[l].After(e.Last[best]) {
			best = l
		}
	}
	return best
}

// minUse is the smaller of a and b, kind by kind.
func minUse(a, b meter.Use) meter.Use {
	return a.Sub(a.Sub(b))
}

// takeHours takes each hour of taken out of hours, dropping an hour it
// empties.
func takeHours(hours, taken meter.Hours) {
	for h, t := range taken {
		u := hours[h].Sub(t)
		u.CacheWrite1h = min(u.CacheWrite1h, u.CacheWrite)
		if u.Zero() {
			delete(hours, h)
		} else {
			hours[h] = u
		}
	}
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
// hold, as Orca links each Codex rollout into every account's home. Codex's
// thread table names the account that created some threads: when exactly
// one account logged in at those homes created the home's newest thread
// with that account, it is the one. Otherwise the file's newest rate-limit
// reading says: its weekly window resets when the serving account's does,
// and accounts' windows reset apart, though sometimes only seconds apart.
// The account whose reset is nearest, within resetSkew, served it, unless
// another is about as near. With no such account, it returns "" and the
// session stays with its first home, the default home when it holds the
// file. ids are the account ids the homes' thread tables name, by home.
func servedBy(st *state.State, p string, s logs.Session, labels, ids map[string]string) string {
	if len(s.Homes) < 2 {
		return ""
	}
	if l := createdBy(s, labels, ids); l != "" {
		return l
	}
	if s.Limits == nil {
		return ""
	}
	found, best, next := "", time.Duration(-1), time.Duration(-1)
	seen := map[string]bool{}
	for _, h := range s.Homes {
		l := labels[h]
		if l == "" || l == UnknownAccount || seen[l] {
			continue
		}
		seen[l] = true
		acct := st.Accounts[state.Key(p, l)]
		if acct == nil || acct.Quota == nil {
			continue
		}
		d, ok := resetDistance(acct.Quota.Windows, s.Limits.Windows)
		if !ok || d > resetSkew {
			continue
		}
		switch {
		case best < 0 || d < best:
			found, best, next = l, d, best
		case next < 0 || d < next:
			next = d
		}
	}
	if next >= 0 && next-best < resetTie {
		return ""
	}
	return found
}

// createdBy is the account logged in at the one home, among those holding
// the session's file, whose thread table names the session's creator as the
// account creating its threads. Two homes that name the creator for
// different accounts leave it "".
func createdBy(s logs.Session, labels, ids map[string]string) string {
	if s.CreatorAccount == "" {
		return ""
	}
	found := ""
	for _, h := range s.Homes {
		l := labels[h]
		if ids[h] != s.CreatorAccount || l == "" || l == UnknownAccount || l == found {
			continue
		}
		if found != "" {
			return ""
		}
		found = l
	}
	return found
}

// accountIDs is the account id each home's thread table names, by home.
func accountIDs(res logs.Result) map[string]string {
	ids := map[string]string{}
	for h, hr := range res.Homes {
		if hr.AccountID != "" {
			ids[h] = hr.AccountID
		}
	}
	return ids
}

// resetSkew is how far apart two readings of one window's reset time may be.
// One account's readings of it differ by up to about ten seconds; two
// accounts' windows opened together can reset half a minute apart.
const resetSkew = 20 * time.Second

// resetTie is how much nearer one account's reset must be than another's to
// tell them apart.
const resetTie = 5 * time.Second

// resetDistance is how far apart the reset of the longest window of the log
// reading and the reset of the window of the same length in the quota are.
func resetDistance(quota, log []snapshot.Window) (time.Duration, bool) {
	var longest *snapshot.Window
	for i, w := range log {
		if w.ResetsAt != nil && w.Minutes > 0 && (longest == nil || w.Minutes > longest.Minutes) {
			longest = &log[i]
		}
	}
	if longest == nil {
		return 0, false
	}
	for _, w := range quota {
		if w.Minutes == longest.Minutes && w.ResetsAt != nil {
			d := w.ResetsAt.Sub(*longest.ResetsAt)
			return max(d, -d), true
		}
	}
	return 0, false
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
