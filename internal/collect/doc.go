package collect

import (
	"encoding/json"
	"maps"
	"sort"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
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
	// Hours is the account's input plus output tokens by the UTC hour they
	// were spent in, keyed by logs.HourOf.
	Hours map[int64]int64
}

type ProjectTotals struct {
	Path     string
	Sessions int
	Tokens   snapshot.Tokens
	// Hours is the project's input plus output tokens by UTC hour.
	Hours      map[int64]int64
	LastActive time.Time
	// Providers are the harnesses that used the project, most tokens first.
	// Only Projects fills it.
	Providers []string
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
			last := lastActive(s, label)
			if last.After(a.LastActive) {
				a.LastActive = last
			}
			hours := labelHours(s, label)
			a.Hours = addHours(a.Hours, hours)
			k := state.Key(s.Provider, label)
			p := projects[k][s.Project]
			if p == nil {
				p = &ProjectTotals{Path: s.Project}
				projects[k][s.Project] = p
			}
			p.Sessions++
			p.Tokens = p.Tokens.Add(tok)
			p.Hours = addHours(p.Hours, hours)
			if last.After(p.LastActive) {
				p.LastActive = last
			}
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

// Projects sums the ledger per project over every account and harness on
// this device.
func Projects(st *state.State) []ProjectTotals {
	byPath := map[string]*ProjectTotals{}
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
			hours = addHours(hours, labelHours(s, label))
			if l := lastActive(s, label); l.After(last) {
				last = l
			}
		}
		if tok.Zero() {
			continue
		}
		p := byPath[s.Project]
		if p == nil {
			p = &ProjectTotals{Path: s.Project}
			byPath[s.Project] = p
			byProvider[s.Project] = map[string]int64{}
		}
		p.Sessions++
		p.Tokens = p.Tokens.Add(tok)
		p.Hours = addHours(p.Hours, hours)
		if last.After(p.LastActive) {
			p.LastActive = last
		}
		byProvider[s.Project][s.Provider] += tok.Total()
	}
	out := make([]ProjectTotals, 0, len(byPath))
	for path, p := range byPath {
		for prov := range byProvider[path] {
			p.Providers = append(p.Providers, prov)
		}
		used := byProvider[path]
		sort.Slice(p.Providers, func(i, j int) bool {
			a, b := p.Providers[i], p.Providers[j]
			if used[a] != used[b] {
				return used[a] > used[b]
			}
			return a < b
		})
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens.Total() != out[j].Tokens.Total() {
			return out[i].Tokens.Total() > out[j].Tokens.Total()
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// labelHours is label's part of a session's hours. Once a second account
// spends in a session, each keeps its own; until then they are all the one
// account's. A ledger from before accounts kept their own gives each its
// share of the session's input plus output, spread over the hours as the
// session's are. A session whose hours are not known, as one the ledger kept
// from before it recorded them, has the part at the hour label's share last
// grew.
func labelHours(s *state.Session, label string) map[int64]int64 {
	if s.ByHours != nil {
		return s.ByHours[label]
	}
	own := logs.InOut(s.By[label])
	if own <= 0 {
		return nil
	}
	var all, sum int64
	for _, t := range s.By {
		all += logs.InOut(t)
	}
	for _, n := range s.Hours {
		sum += n
	}
	if sum <= 0 {
		return map[int64]int64{logs.HourOf(lastActive(s, label)): own}
	}
	if own == all {
		return s.Hours
	}
	out := maps.Clone(s.Hours)
	logs.ScaleHours(out, int64(float64(sum)*float64(own)/float64(all)))
	return out
}

// accountHours is each account's part of a session's hours, as labelHours
// gives it.
func accountHours(s *state.Session) map[string]map[int64]int64 {
	out := map[string]map[int64]int64{}
	for l := range s.By {
		if h := labelHours(s, l); len(h) > 0 {
			out[l] = maps.Clone(h)
		}
	}
	return out
}

// addHours adds b onto a and returns a, made when needed.
func addHours(a, b map[int64]int64) map[int64]int64 {
	for h, n := range b {
		if a == nil {
			a = map[int64]int64{}
		}
		a[h] += n
	}
	return a
}

// DaysOf buckets hours by UTC day, newest first: index 0 is now's UTC day.
// It covers the retention window and leaves out trailing zeros.
func DaysOf(hours map[int64]int64, now time.Time) []int64 {
	today := dayNumber(now)
	var out []int64
	for h, n := range hours {
		i := today - dayNumber(logs.HourStart(h))
		if i < 0 || i >= snapshot.MaxDays || n <= 0 {
			continue
		}
		for int64(len(out)) <= i {
			out = append(out, 0)
		}
		out[i] += n
	}
	return out
}

// dayNumber counts UTC days since the Unix epoch.
func dayNumber(t time.Time) int64 { return t.Unix() / 86400 }

// SinceStart is the tokens of hours spent since start. An hour counts when
// most of it is past start.
func SinceStart(hours map[int64]int64, start time.Time) int64 {
	var n int64
	for h, v := range hours {
		if !logs.HourStart(h).Add(30 * time.Minute).Before(start) {
			n += v
		}
	}
	return n
}

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
func BuildDoc(st *state.State, key *team.Key, cfg state.Config, hostname, osUser, version string, now time.Time) snapshot.Doc {
	seal := func(s string) string { return key.Seal(clip(s, maxSealedPlain)) }
	doc := snapshot.Doc{
		V:                snapshot.Version,
		Team:             key.Fingerprint(),
		Device:           cfg.Device,
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
		if len(parts) != 2 || parts[1] == "" || a.At.IsZero() || !knownProvider(parts[0]) {
			continue
		}
		sa := snapshot.Alias{Provider: parts[0], Label: parts[1], At: a.At.UTC()}
		if a.Name != "" {
			sa.Name = a.Name
		}
		out = append(out, sa)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Label < out[j].Label
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

func knownProvider(p string) bool {
	for _, k := range Providers {
		if p == k {
			return true
		}
	}
	return false
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
		longest = -1
		for i, a := range doc.Accounts {
			if len(a.Days) > 7 && (longest < 0 || len(a.Days) > len(doc.Accounts[longest].Days)) {
				longest = i
			}
		}
		if longest >= 0 {
			ds := doc.Accounts[longest].Days
			doc.Accounts[longest].Days = ds[:len(ds)-1]
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
