package collect

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// Bounds of a believable reset time: none before 2000, and none further
// ahead than the longest window plus a day of clock skew.
var minResetsAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

const maxResetsAhead = snapshot.MaxWindowMinutes*time.Minute + 24*time.Hour

// RejectionSource is the source of a reading taken from a refused request.
const RejectionSource = "rejection"

// setQuota makes q the account's reading unless the account has a newer one.
func setQuota(acct *state.Account, q *probe.Quota, now time.Time) {
	if q == nil || len(q.Windows) == 0 {
		return
	}
	// A reading stamped ahead of the clock is from now at the latest. One
	// older than the retention window is not kept, whatever produced it.
	at := q.At.UTC()
	if at.After(now) {
		at = now
	}
	if at.Before(now.Add(-state.Retention)) {
		return
	}
	// A stored reading from a clock that has since gone back would otherwise
	// block every real reading until the clock caught up. One from refused
	// requests holds only until its windows reset; after that it would hide
	// an older reading of the windows it says nothing about. Each run gathers
	// every refusal still in force, so a newer gathering replaces it whole.
	old := acct.Quota
	refused := old != nil && old.Source == RejectionSource && (q.Source == RejectionSource || allReset(old.Windows, now))
	if old == nil || old.At.After(now) || !at.Before(old.At) || refused {
		acct.Quota = &state.Quota{At: at, Source: q.Source, Windows: clampWindows(q.Windows, now)}
	}
}

// applyRejected gives each account the requests refused for a full window,
// among the sessions attributed to it, whose window has not reset: the newest
// of each window, as one reading as of the newest. A refusal says that window
// was at 100% then and stays so until it resets, and nothing about the other
// windows, so the reading is those windows alone: carrying the older
// reading's windows along would make them look newer than they are. It
// counts when it is newer than the account's reading. A refusal since the previous run goes where this run
// puts the session's growth. One from before it belongs to whoever used the
// session then, and the ledger keeps only each account's share, so it counts
// only when the whole session is this account's, as a session read for the
// first time is. Neither counts when it is older than the run that found the
// home's login changed: a full window is the usual reason to switch, so it
// is as likely the earlier account's, even in a session the ledger gives
// wholly to the new one.
func applyRejected(st *state.State, p string, read []readSession, now, prevRun time.Time) {
	held := map[string][]*logs.Limits{}
	for _, r := range read {
		if r.label == UnknownAccount {
			continue
		}
		switched := st.Switched[state.Key(p, r.s.Home)]
		for _, x := range r.s.Rejected {
			if allReset(x.Windows, now) || x.ObservedAt.Before(switched) {
				continue
			}
			if x.ObservedAt.Before(prevRun) && !soleAccount(st.Sessions[state.Key(p, r.s.ID)], r.label) {
				continue
			}
			held[r.label] = logs.AddRejected(held[r.label], x)
		}
	}
	for label, xs := range held {
		q := &probe.Quota{Source: RejectionSource}
		for _, x := range xs {
			if x.ObservedAt.After(q.At) {
				q.At = x.ObservedAt
			}
			q.Windows = append(q.Windows, x.Windows...)
		}
		slices.SortFunc(q.Windows, func(a, b snapshot.Window) int {
			return cmp.Or(cmp.Compare(a.Minutes, b.Minutes), strings.Compare(a.Name, b.Name))
		})
		setQuota(touchAccount(st, p, label), q, now)
	}
}

// soleAccount reports whether the ledger gives all of session e to label.
func soleAccount(e *state.Session, label string) bool {
	if e == nil || len(e.By) != 1 {
		return false
	}
	_, ok := e.By[label]
	return ok
}

// allReset reports whether every window has passed its reset time.
func allReset(ws []snapshot.Window, now time.Time) bool {
	for _, w := range ws {
		if w.ResetsAt == nil || w.ResetsAt.After(now) {
			return false
		}
	}
	return true
}

// clampWindows keeps windows within what the state and the snapshot can hold.
func clampWindows(ws []snapshot.Window, now time.Time) []snapshot.Window {
	out := make([]snapshot.Window, 0, len(ws))
	for _, w := range ws {
		if len(out) == snapshot.MaxWindows {
			break
		}
		w.Name = snapshot.PlainLabel(w.Name)
		if w.Name == "" {
			w.Name = "window"
		}
		// NaN would make the state and the snapshot unencodable.
		if !(w.Percent >= 0) {
			w.Percent = 0
		}
		if w.Percent > 1000 {
			w.Percent = 1000
		}
		if w.Minutes < 0 || w.Minutes > snapshot.MaxWindowMinutes {
			w.Minutes = 0
		}
		// No window resets further out than its length, and a time JSON
		// cannot hold would stop the state from saving. Such a value is a
		// unit mix-up upstream: no reset time rather than a wrong one.
		if w.ResetsAt != nil && (w.ResetsAt.Before(minResetsAt) || w.ResetsAt.After(now.Add(maxResetsAhead))) {
			w.ResetsAt = nil
		}
		out = append(out, w)
	}
	return out
}
