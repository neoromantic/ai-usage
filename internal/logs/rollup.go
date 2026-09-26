package logs

import (
	"cmp"
	"slices"
)

// UnknownProject is the project of a session whose log names no directory.
const UnknownProject = "unknown"

// UnknownAccount is the account of a part of a session that the log bills to
// no account and marks as not the session's own (Hermes: an auxiliary call
// on a fallback route).
const UnknownAccount = "unknown"

// mergeByID keeps one row per session id, in first-seen order, and merges
// each later row with the same id into it. Rows with no id never merge.
func mergeByID(in []Session, merge func(have *Session, s Session)) []Session {
	at := map[string]int{}
	out := make([]Session, 0, len(in))
	for _, s := range in {
		if i, ok := at[s.ID]; ok {
			merge(&out[i], s)
			continue
		}
		if s.ID != "" {
			at[s.ID] = len(out)
		}
		out = append(out, s)
	}
	return out
}

// dedupeSessions keeps one row per session id, the one with the most tokens.
// The same session can appear twice when two homes share a directory.
func dedupeSessions(in []Session) []Session {
	return mergeByID(in, func(have *Session, s Session) {
		if s.Tokens.Total() > have.Tokens.Total() {
			*have, s = s, *have
		}
		fillFrom(have, s)
	})
}

// addCopy adds a copy that counts only its own part (a page, or the same
// session in another project directory or home); the session grows in the
// copy written last.
func addCopy(have *Session, s Session) {
	have.Tokens = have.Tokens.Add(s.Tokens)
	have.Hours = AddHours(have.Hours, s.Hours)
	if s.Updated.After(have.Updated) {
		have.Home = s.Home
	}
	fillFrom(have, s)
}

func fillFrom(dst *Session, src Session) {
	dst.ParentID = cmp.Or(dst.ParentID, src.ParentID)
	dst.Project = cmp.Or(dst.Project, src.Project)
	dst.Account = cmp.Or(dst.Account, src.Account)
	if src.Updated.After(dst.Updated) {
		dst.Updated = src.Updated
	}
	for _, h := range src.Homes {
		if !slices.Contains(dst.Homes, h) {
			dst.Homes = append(dst.Homes, h)
		}
	}
	dst.Limits = later(dst.Limits, src.Limits)
	dst.Rejected = AddRejected(dst.Rejected, src.Rejected...)
}

// later is the reading observed last.
func later(a, b *Limits) *Limits {
	if b != nil && (a == nil || b.ObservedAt.After(a.ObservedAt)) {
		return b
	}
	return a
}

// AddRejected adds refusals to have, keeping the newest of each window. It
// returns a new list, since copies of one session share have.
func AddRejected(have []*Limits, add ...*Limits) []*Limits {
	out := slices.Clone(have)
	for _, x := range add {
		if x == nil || len(x.Windows) == 0 {
			continue
		}
		i := slices.IndexFunc(out, func(y *Limits) bool { return y.Windows[0].Name == x.Windows[0].Name })
		if i < 0 {
			out = append(out, x)
		} else {
			out[i] = later(out[i], x)
		}
	}
	return out
}

// addParts adds src's parts onto dst's.
func addParts(dst *Session, src Session) {
	if src.Parts == nil {
		return
	}
	if dst.Parts == nil {
		dst.Parts = map[string]Tokens{}
	}
	for a, t := range src.Parts {
		dst.Parts[a] = dst.Parts[a].Add(t)
	}
}

// rollup adds each sub-agent's tokens onto its root session.
// A parent that was not loaded keeps the child as its own session.
// Sessions with no tokens are dropped.
func rollup(in []Session) []Session {
	byID := map[string]int{}
	for i, s := range in {
		if s.ID != "" {
			byID[s.ID] = i
		}
	}
	rootOf := func(i int) int {
		seen := map[int]bool{}
		for {
			if seen[i] {
				return i
			}
			seen[i] = true
			j, ok := byID[in[i].ParentID]
			if in[i].ParentID == "" || !ok || j == i {
				return i
			}
			i = j
		}
	}

	acc := map[int]*Session{}
	var order []int
	for i := range in {
		r := rootOf(i)
		dst, ok := acc[r]
		if !ok {
			cp := in[r]
			cp.Tokens = Tokens{}
			cp.Parts = nil
			cp.Hours = nil
			cp.ParentID = ""
			dst = &cp
			acc[r] = dst
			order = append(order, r)
		}
		dst.Tokens = dst.Tokens.Add(in[i].Tokens)
		addParts(dst, in[i])
		dst.Hours = AddHours(dst.Hours, in[i].Hours)
		if i != r {
			fillFrom(dst, in[i])
			dst.ParentID = ""
		}
	}

	out := []Session{}
	for _, i := range order {
		s := *acc[i]
		if s.Tokens.Zero() {
			continue
		}
		if s.Project == "" {
			s.Project = UnknownProject
		}
		out = append(out, s)
	}
	return out
}
