package logs

// UnknownProject is the project of a session whose log names no directory.
const UnknownProject = "unknown"

// UnknownAccount is the account of a part of a session that the log bills to
// no account and marks as not the session's own (Hermes: an auxiliary call
// on a fallback route).
const UnknownAccount = "unknown"

// dedupeSessions keeps one row per session id, the one with the most tokens.
// The same session can appear twice when two homes share a directory.
func dedupeSessions(in []Session) []Session {
	seen := map[string]int{}
	out := []Session{}
	for _, s := range in {
		if s.ID == "" {
			out = append(out, s)
			continue
		}
		i, ok := seen[s.ID]
		if !ok {
			seen[s.ID] = len(out)
			out = append(out, s)
			continue
		}
		keep, other := out[i], s
		if s.Tokens.Total() > out[i].Tokens.Total() {
			keep, other = s, out[i]
		}
		fillFrom(&keep, other)
		out[i] = keep
	}
	return out
}

func fillFrom(dst *Session, src Session) {
	if dst.ParentID == "" {
		dst.ParentID = src.ParentID
	}
	if dst.Project == "" {
		dst.Project = src.Project
	}
	if dst.Account == "" {
		dst.Account = src.Account
	}
	if src.Updated.After(dst.Updated) {
		dst.Updated = src.Updated
	}
	for _, h := range src.Homes {
		if !containsString(dst.Homes, h) {
			dst.Homes = append(dst.Homes, h)
		}
	}
	if src.Limits != nil && (dst.Limits == nil || src.Limits.ObservedAt.After(dst.Limits.ObservedAt)) {
		dst.Limits = src.Limits
	}
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

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
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
			cp.ParentID = ""
			dst = &cp
			acc[r] = dst
			order = append(order, r)
		}
		dst.Tokens = dst.Tokens.Add(in[i].Tokens)
		addParts(dst, in[i])
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
