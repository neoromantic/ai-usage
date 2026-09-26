package logs

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// readCodexHomes sums <home>/sessions and <home>/archived_sessions of every
// home together, and records each home's read in reads.
//
// A rollout file is one thread. Its token_count lines carry the thread's
// cumulative total and the usage of the last request, and a request counts
// once, by that last usage. The same (total, last) pair seen again is a
// repeated notification, a fork replaying its parent's history, or the same
// file kept twice. A fork's cumulative total starts at its parent's, so
// neither the replay nor the seeded total is new usage.
//
// Replays are matched within a family of threads linked by the ids in the
// first session_meta, and the thread that started first keeps the request,
// whichever home holds it. A stale file that a fresh thread links to is read
// only for that matching.
//
// Newer Codex also writes a token_usage_record line for each response, before
// the token_count that repeats it. A compaction's record is the one the next
// token_count leaves out, so a record no token_count repeats counts once per
// response id in the family.
//
// A request's tokens go to the hour of the line that counts it, in the thread
// that counts it.
//
// OpenAI counts cached input inside input_tokens, so cached tokens are moved
// out of Input. token_count lines also carry the rate limits the harness last
// saw; each home's newest reading is kept as a fallback quota reading for
// whoever is logged in at that home.
//
// One file linked into several homes, as Orca links each rollout into every
// account's home, is read once, from the first of them. Its readings are no
// home's fallback, since the file does not say which account it ran under.
func readCodexHomes(homes []string, since time.Time, reads map[string]HomeRead) []Session {
	var files []*codexFile
	stale := map[string]string{}
	linked := sameFiles{}
	for _, h := range homes {
		fs, hr := codexFiles(h, since, stale, linked)
		files = append(files, fs...)
		reads[h] = hr
	}
	files = append(files, linkedCodexFiles(files, stale)...)
	sessions := countCodex(files)
	for _, h := range homes {
		var own []*codexFile
		for _, f := range files {
			if f.home == h && len(f.mirrors) == 0 {
				own = append(own, f)
			}
		}
		hr := reads[h]
		hr.Limits = codexLimits(own)
		reads[h] = hr
	}
	return sessions
}

// codexFiles parses the fresh rollout files of one home and indexes its stale
// ones in stale. A file an earlier home already holds is noted on that one.
func codexFiles(home string, since time.Time, stale map[string]string, linked sameFiles) ([]*codexFile, HomeRead) {
	var out HomeRead
	var files []*codexFile
	for _, leaf := range []string{"sessions", "archived_sessions"} {
		root, err := resolveDir(filepath.Join(home, leaf))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, HomeRead{Err: err}
		}
		files = append(files, walkCodex(root, home, since, stale, linked, &out)...)
	}
	return files, out
}

// walkCodex parses the fresh rollout files under root and indexes the stale
// ones by the thread id in their name.
func walkCodex(root, home string, since time.Time, stale map[string]string, linked sameFiles, out *HomeRead) []*codexFile {
	var files []*codexFile
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			out.Unreadable++
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") || deniedFile(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			out.Unreadable++
			return nil
		}
		if first, ok := linked.find(d.Name(), info); ok {
			if first != nil && first.home != home && !slices.Contains(first.mirrors, home) {
				first.mirrors = append(first.mirrors, home)
			}
			return nil
		}
		if !freshEnough(info.ModTime(), since) {
			linked.add(d.Name(), info, nil)
			if id := codexNameID(d.Name()); id != "" {
				stale[id] = path
			}
			return nil
		}
		f, bad, err := parseCodex(path)
		out.Malformed += bad
		if err != nil {
			out.Unreadable++
		}
		f.id = cmp.Or(f.id, strings.TrimSuffix(d.Name(), ".jsonl"))
		f.home = home
		f.updated = info.ModTime().UTC()
		f.fresh = true
		linked.add(d.Name(), info, f)
		files = append(files, f)
		return nil
	})
	return files
}

// sameFiles finds a file already read under another path, by name and then
// by identity: a hard link, not a copy, is the same file.
type sameFiles map[string][]sameFile

type sameFile struct {
	info fs.FileInfo
	// file is nil for a file that was too old to read.
	file *codexFile
}

func (s sameFiles) find(name string, info fs.FileInfo) (*codexFile, bool) {
	for _, c := range s[name] {
		if os.SameFile(c.info, info) {
			return c.file, true
		}
	}
	return nil, false
}

func (s sameFiles) add(name string, info fs.FileInfo, f *codexFile) {
	s[name] = append(s[name], sameFile{info: info, file: f})
}

// linkedCodexFiles reads stale files that loaded threads name as parent,
// root, or fork source, so a fork of an old thread does not count the history
// it replays. They report no session of their own.
func linkedCodexFiles(files []*codexFile, stale map[string]string) []*codexFile {
	loaded := map[string]bool{}
	for _, f := range files {
		loaded[f.id] = true
	}
	var extra []*codexFile
	queue := append([]*codexFile(nil), files...)
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		for _, id := range f.links {
			path, ok := stale[id]
			if !ok || loaded[id] {
				continue
			}
			loaded[id] = true
			// A stale file that fails to parse only loses its matching.
			p, _, _ := parseCodex(path)
			if p.id == "" {
				p.id = id
			}
			extra = append(extra, p)
			queue = append(queue, p)
		}
	}
	return extra
}

// countCodex gives each fresh file the requests no earlier thread in its
// family already logged, and merges files of one thread into one session.
func countCodex(files []*codexFile) []Session {
	fam := unionFind{}
	for _, f := range files {
		fam.add(f.id)
		for _, id := range f.links {
			fam.union(f.id, id)
		}
	}
	families := map[string][]int{}
	for i, f := range files {
		root := fam.find(f.id)
		families[root] = append(families[root], i)
	}
	own := make([]Tokens, len(files))
	hours := make([]map[int64]int64, len(files))
	for _, members := range families {
		sort.SliceStable(members, func(a, b int) bool {
			x, y := files[members[a]], files[members[b]]
			if !x.start.Equal(y.start) {
				return x.start.Before(y.start)
			}
			if x.id != y.id {
				return x.id < y.id
			}
			return x.path < y.path
		})
		seen := map[codexEvent]bool{}
		responses := map[string]bool{}
		for _, i := range members {
			var prev codexUsage
			for _, c := range files[i].events {
				ev := c.event
				delta := ev.last
				if !ev.hasLast {
					delta = ev.total.since(prev)
				}
				prev = ev.total
				if seen[ev] {
					continue
				}
				seen[ev] = true
				t := delta.tokens()
				own[i] = own[i].Add(t)
				AddHour(&hours[i], c.at, InOut(t))
			}
			for _, r := range files[i].unrepeated {
				if responses[r.response] {
					continue
				}
				responses[r.response] = true
				t := r.usage.tokens()
				own[i] = own[i].Add(t)
				AddHour(&hours[i], r.at, InOut(t))
			}
		}
	}

	var rows []Session
	for i, f := range files {
		if !f.fresh {
			continue
		}
		s := Session{ID: f.id, ParentID: f.parent, Project: f.project, Tokens: own[i], Hours: hours[i], Updated: f.updated, Home: f.home, Limits: f.limits[codexMainLimit]}
		if len(f.mirrors) > 0 {
			s.Homes = append([]string{f.home}, f.mirrors...)
		}
		rows = append(rows, s)
	}
	return mergeByID(rows, addCopy)
}

// codexLimitSkew is how long before the main limit's reading another limit's
// reading still describes the same moment.
const codexLimitSkew = 15 * time.Minute

// codexLimits reports the newest reading of the main codex limit, with the
// other limits read around the same time. A side limit read later must not
// hide the main one, and one read long before would look fresher than it is.
// Without a main reading the newest reading of any limit is used.
func codexLimits(files []*codexFile) *Limits {
	newest := map[string]*Limits{}
	for _, f := range files {
		for b, l := range f.limits {
			newest[b] = later(newest[b], l)
		}
	}
	buckets := slices.Sorted(maps.Keys(newest))
	anchor := newest[codexMainLimit]
	if anchor == nil {
		for _, b := range buckets {
			anchor = later(anchor, newest[b])
		}
	}
	if anchor == nil {
		return nil
	}
	out := &Limits{ObservedAt: anchor.ObservedAt, Plan: anchor.Plan, Windows: append([]snapshot.Window(nil), anchor.Windows...)}
	for _, b := range buckets {
		l := newest[b]
		if l == anchor || l.ObservedAt.Before(anchor.ObservedAt.Add(-codexLimitSkew)) {
			continue
		}
		out.Windows = append(out.Windows, l.Windows...)
		if out.Plan == "" {
			out.Plan = l.Plan
		}
	}
	return out
}

// codexFile is what one rollout file says about its own thread.
type codexFile struct {
	path string
	home string
	// mirrors are the other homes the same file is linked into.
	mirrors []string
	id      string
	parent  string
	project string
	// links are other thread ids whose history this file may replay.
	links   []string
	start   time.Time
	updated time.Time
	events  []codexCount
	// unrepeated are this thread's usage records no token_count repeats.
	unrepeated []codexRecord
	// pending is the last record, until the next token_count settles it.
	pending *codexRecord
	// limits is the newest reading per limit id.
	limits map[string]*Limits
	fresh  bool
}

// codexRecord is one response's usage from a token_usage_record line.
type codexRecord struct {
	response string
	usage    codexUsage
	at       time.Time
}

func parseCodex(path string) (*codexFile, int, error) {
	f := &codexFile{path: path}
	haveMeta := false
	var malformed int
	long, err := forEachLine(path, func(line []byte) {
		// One marker finds token_count and token_usage_record lines alike.
		// Rollouts run to gigabytes, and each marker is a pass over them.
		if !bytes.Contains(line, []byte(`"session_meta"`)) && !bytes.Contains(line, []byte(`"token_`)) {
			return
		}
		var row codexLine
		if json.Unmarshal(line, &row) != nil {
			malformed++
			return
		}
		switch {
		case row.Type == "session_meta":
			// A fork copies its parent's session_meta after its own.
			if !haveMeta {
				haveMeta = true
				f.meta(row)
			}
		case row.Type == "token_usage_record":
			f.record(row)
		case row.Payload.Type == "token_count":
			f.count(row)
		}
	})
	// The newest record's token_count may not be written yet. It will repeat it.
	f.settle(nil)
	return f, malformed + long, err
}

func (f *codexFile) meta(row codexLine) {
	p := row.Payload
	f.id = cmp.Or(p.ID, p.SessionID)
	f.project = strings.TrimSpace(p.Cwd)
	f.parent = cmp.Or(spawnParent(p.Source), p.ParentThreadID)
	// session_id names the root thread of a sub-agent.
	if f.parent == "" && p.SessionID != "" && p.SessionID != f.id {
		f.parent = p.SessionID
	}
	for _, id := range []string{f.parent, p.SessionID, p.ForkedFromID, historyThread(p.HistoryBase)} {
		if id != "" && id != f.id {
			f.links = append(f.links, id)
		}
	}
	if t := parseTime(p.Timestamp); !t.IsZero() {
		f.start = t
	} else if t := parseTime(row.Timestamp); !t.IsZero() {
		f.start = t
	}
}

func (f *codexFile) count(row codexLine) {
	at := parseTime(row.Timestamp)
	if f.start.IsZero() {
		f.start = at
	}
	if bucket, l := row.Payload.RateLimits.reading(at); l != nil {
		if f.limits == nil {
			f.limits = map[string]*Limits{}
		}
		// later keeps its first argument on a tie, so a newer line read at
		// the same time wins.
		f.limits[bucket] = later(l, f.limits[bucket])
	}
	info := row.Payload.Info
	if info == nil || info.Total == nil {
		return
	}
	ev := codexEvent{total: *info.Total}
	if info.Last != nil {
		ev.last, ev.hasLast = *info.Last, true
	}
	f.settle(&ev)
	// A notification that repeats the previous line adds nothing; skip storing it.
	if n := len(f.events); n > 0 && f.events[n-1].event == ev {
		return
	}
	f.events = append(f.events, codexCount{event: ev, at: at})
}

// record holds a response's usage until the next token_count shows whether it
// repeats it. A fork replays its parent's records under the parent's thread
// id; the parent's file counts those.
func (f *codexFile) record(row codexLine) {
	p := row.Payload
	if p.Usage == nil || p.ResponseID == "" || (p.ThreadID != "" && f.id != "" && p.ThreadID != f.id) {
		return
	}
	f.settle(nil)
	f.pending = &codexRecord{response: p.ResponseID, usage: *p.Usage, at: parseTime(row.Timestamp)}
}

// settle keeps the pending record unless ev, the next token_count, repeats
// it as its last usage. A token_count without last usage counts by its total,
// which already holds the record's request.
func (f *codexFile) settle(ev *codexEvent) {
	if f.pending == nil {
		return
	}
	if ev == nil || (ev.hasLast && ev.last != f.pending.usage) {
		f.unrepeated = append(f.unrepeated, *f.pending)
	}
	f.pending = nil
}

// codexCount is one token_count line and when it was written.
type codexCount struct {
	event codexEvent
	at    time.Time
}

// codexEvent is what one token_count line counts. It is also the key that
// matches a repeated or replayed request, which a replay writes at another
// time.
type codexEvent struct {
	total   codexUsage
	last    codexUsage
	hasLast bool
}

type codexUsage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Total      int64 `json:"total_tokens"`
}

// since is the growth from prev, for lines without last usage. A lower input
// total is a counter reset, and the new count starts from zero.
func (u codexUsage) since(prev codexUsage) codexUsage {
	if u.Input < prev.Input {
		return u
	}
	return codexUsage{
		Input:      max(u.Input-prev.Input, 0),
		Cached:     max(u.Cached-prev.Cached, 0),
		Output:     max(u.Output-prev.Output, 0),
		CacheWrite: max(u.CacheWrite-prev.CacheWrite, 0),
	}
}

func (u codexUsage) tokens() Tokens {
	input := max(u.Input-u.Cached, 0)
	return Tokens{Input: input, Output: u.Output, CacheRead: u.Cached, CacheWrite: u.CacheWrite}
}

func spawnParent(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return ""
	}
	var src struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(raw, &src) != nil {
		return ""
	}
	var sub struct {
		ThreadSpawn *struct {
			ParentThreadID string `json:"parent_thread_id"`
		} `json:"thread_spawn"`
	}
	// subagent is an object for spawned threads and a plain string for others.
	if json.Unmarshal(src.Subagent, &sub) != nil || sub.ThreadSpawn == nil {
		return ""
	}
	return sub.ThreadSpawn.ParentThreadID
}

func historyThread(raw json.RawMessage) string {
	var hb struct {
		ThreadID string `json:"thread_id"`
	}
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &hb) != nil {
		return ""
	}
	return hb.ThreadID
}

var codexNameRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// codexNameID is the thread id at the end of a rollout file name.
func codexNameID(name string) string {
	return codexNameRe.FindString(strings.TrimSuffix(name, ".jsonl"))
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// Timestamps and ids stay strings so one odd value does not reject the line.
type codexLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type           string          `json:"type"`
		ID             string          `json:"id"`
		SessionID      string          `json:"session_id"`
		Timestamp      string          `json:"timestamp"`
		Cwd            string          `json:"cwd"`
		Source         json.RawMessage `json:"source"`
		ParentThreadID string          `json:"parent_thread_id"`
		ForkedFromID   string          `json:"forked_from_id"`
		HistoryBase    json.RawMessage `json:"history_base"`
		ThreadID       string          `json:"thread_id"`
		ResponseID     string          `json:"response_id"`
		Usage          *codexUsage     `json:"usage"`
		Info           *struct {
			Total *codexUsage `json:"total_token_usage"`
			Last  *codexUsage `json:"last_token_usage"`
		} `json:"info"`
		RateLimits *codexLogLimits `json:"rate_limits"`
	} `json:"payload"`
}

// codexMainLimit is the limit id of the main Codex bucket. Older logs leave it unset.
const codexMainLimit = "codex"

type codexLogLimits struct {
	LimitID   string          `json:"limit_id"`
	PlanType  string          `json:"plan_type"`
	Primary   *codexLogWindow `json:"primary"`
	Secondary *codexLogWindow `json:"secondary"`
}

type codexLogWindow struct {
	UsedPercent     float64 `json:"used_percent"`
	WindowMinutes   int     `json:"window_minutes"`
	ResetsAt        int64   `json:"resets_at"`
	ResetsInSeconds int64   `json:"resets_in_seconds"`
}

// reading is the quota a token_count line carries, keyed by its limit id.
func (l *codexLogLimits) reading(at time.Time) (string, *Limits) {
	if l == nil || at.IsZero() || (l.Primary == nil && l.Secondary == nil) {
		return "", nil
	}
	bucket := cmp.Or(l.LimitID, codexMainLimit)
	out := &Limits{ObservedAt: at.UTC(), Plan: l.PlanType}
	for _, w := range []*codexLogWindow{l.Primary, l.Secondary} {
		if w == nil {
			continue
		}
		win := snapshot.Window{Name: CodexWindowName(l.LimitID, w.WindowMinutes), Percent: w.UsedPercent, Minutes: w.WindowMinutes}
		switch {
		case w.ResetsAt > 0:
			t := time.Unix(w.ResetsAt, 0).UTC()
			win.ResetsAt = &t
		case w.ResetsInSeconds > 0:
			t := at.Add(time.Duration(w.ResetsInSeconds) * time.Second).UTC()
			win.ResetsAt = &t
		}
		out.Windows = append(out.Windows, win)
	}
	return bucket, out
}

// CodexWindowName names a Codex window by length, prefixed with the limit id
// when it is not the main codex bucket.
func CodexWindowName(limitID string, minutes int) string {
	name := snapshot.DurationName(minutes)
	if limitID != "" && limitID != codexMainLimit {
		name = snapshot.PlainLabel(limitID + " " + name)
	}
	return name
}

// unionFind groups thread ids that may share replayed history.
type unionFind map[string]string

func (u unionFind) add(x string) {
	if _, ok := u[x]; !ok {
		u[x] = x
	}
}

func (u unionFind) find(x string) string {
	u.add(x)
	for u[x] != x {
		u[x] = u[u[x]]
		x = u[x]
	}
	return x
}

func (u unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if rb < ra {
		ra, rb = rb, ra
	}
	u[rb] = ra
}
