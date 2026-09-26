package logs

import (
	"bytes"
	"cmp"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

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
	// own and hours are what this file counts once replays are matched.
	own   Tokens
	hours map[int64]int64
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
	long, err := ForEachLine(path, maxLineBytes, func(line []byte) {
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
