package collect

import (
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// PooledFor is how long a device counts as sending a provider's sessions
// through BB's Account Pooler after a run last found it doing so. The pool
// is set up once and kept. A run sees it in a Codex session's log, when it
// runs in a session the pool routes, as in a BB thread, or in what BB says
// of its pool; a scheduled run, which BB does not start, sees no such
// environment.
const PooledFor = 7 * 24 * time.Hour

// notePooled records in st when this run found sessions of provider p going
// through BB's Account Pooler: env says the run itself is in one, a session
// read in the last PooledFor is one, as its log says, or BB says its pool,
// which routes p, last served p at bb. The pool's hub picks one of several
// accounts for each request, and neither says which, so the tokens stay
// with the home's login, and the device says it cannot tell.
func notePooled(st *state.State, p string, env bool, sessions []logs.Session, bb, now time.Time) {
	seen := time.Time{}
	switch {
	case env || bb.After(now):
		seen = now
	case now.Sub(bb) < PooledFor:
		seen = bb
	}
	for _, s := range sessions {
		if s.Pooled && now.Sub(s.Updated) < PooledFor && s.Updated.After(seen) {
			seen = s.Updated
		}
	}
	if seen.IsZero() || !seen.After(st.Pooled[p]) {
		return
	}
	if st.Pooled == nil {
		st.Pooled = map[string]time.Time{}
	}
	st.Pooled[p] = seen
}

// Pooled is the providers whose sessions this device sent through BB's
// Account Pooler in the last PooledFor, as st recorded them, in the order
// of snapshot.Providers, or nil.
func Pooled(st *state.State, now time.Time) []string {
	var out []string
	for _, p := range snapshot.Providers {
		if at, ok := st.Pooled[p]; ok && now.Sub(at) < PooledFor {
			out = append(out, p)
		}
	}
	return out
}

// prunePooled forgets what PooledFor no longer counts.
func prunePooled(st *state.State, now time.Time) {
	for p, at := range st.Pooled {
		if now.Sub(at) >= PooledFor {
			delete(st.Pooled, p)
		}
	}
	if len(st.Pooled) == 0 {
		st.Pooled = nil
	}
}

// pooledEnv says the run's environment routes provider p through BB's
// Account Pooler.
func (s *sampler) pooledEnv(p string) bool { return slices.Contains(s.Probe.Pooled(), p) }
