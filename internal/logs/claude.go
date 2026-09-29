package logs

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// claudeFiles parses the session files in <home>/projects, for countClaude.
// Streaming snapshots that share a message id count once, as the last snapshot.
// Files anywhere under <session>/subagents roll into that session, and so does
// a top-level sidechain file that names another session.
// Anthropic reports input_tokens without cache reads, so no adjustment is needed.
func claudeFiles(home string, since time.Time) ([]*claudeFile, HomeRead) {
	root, err := resolveDir(filepath.Join(home, "projects"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, HomeRead{}
	}
	if err != nil {
		return nil, HomeRead{Err: err}
	}
	projEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, HomeRead{Err: err}
	}

	var out HomeRead
	var files []*claudeFile
	read := func(path, id, parent string, mod time.Time) {
		f, bad, err := parseClaude(path, id, parent)
		f.sess.Updated = f.updated(mod)
		f.sess.Home = home
		out.add(bad, err)
		files = append(files, f)
	}
	for _, proj := range projEntries {
		if !proj.IsDir() {
			continue
		}
		dir := filepath.Join(root, proj.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			out.Unreadable++
			continue
		}
		for _, ent := range entries {
			name := ent.Name()
			if ent.IsDir() || !strings.HasSuffix(name, ".jsonl") || deniedFile(name) {
				continue
			}
			info, err := ent.Info()
			if err != nil {
				out.Unreadable++
				continue
			}
			if !freshEnough(info.ModTime(), since) {
				continue
			}
			id := strings.TrimSuffix(name, ".jsonl")
			read(filepath.Join(dir, name), id, "", info.ModTime())
			walkLogs(filepath.Join(dir, id, "subagents"), &out.Unreadable, isJSONL, func(file, rel string, info fs.FileInfo) {
				read(file, id+"/"+rel, id, info.ModTime())
			})
		}
	}
	return files, out
}

// countClaude sums each file's messages, from one home or several. A resumed
// or forked session copies earlier messages into its own file, so a message
// with a request id counts once, in the session that started first, whichever
// home holds it. Files that share a session id (one session under two project
// directories or in two homes) become one session, and a message both hold
// counts once. A refused request belongs to the session its message counts
// in, and so does the hour of the message. A session is raised to the usage
// Claude Code tracked for it, when that is more, in the hour it tracked it.
func countClaude(files []*claudeFile) []Session {
	copied := claimMessages(files)
	rows := make([]Session, len(files))
	for i, f := range files {
		rows[i] = f.sess
	}
	out := addTracked(mergeByID(rows, addCopy), files, copied)
	addFamilies(out, files)
	return out
}

// claimMessages adds each file's messages to its session, taking the files in
// the order they start, and skips a message an earlier file already claimed.
// It returns the sessions whose files repeat another session's messages.
func claimMessages(files []*claudeFile) (copied map[string]bool) {
	order := slices.Clone(files)
	slices.SortStableFunc(order, func(x, y *claudeFile) int {
		// A copy that kept the original times still names the original
		// session in its lines; the original keeps the messages.
		if x.start.Equal(y.start) && x.native != y.native {
			if x.native {
				return -1
			}
			return 1
		}
		return cmp.Or(x.start.Compare(y.start), cmp.Compare(x.path, y.path))
	})
	// claimed names the session each message counts in. A session whose file
	// repeats another session's messages is a copy of that session.
	claimed := map[string]string{}
	copied = map[string]bool{}
	for _, f := range order {
		for _, m := range f.msgs {
			if key := m.key(f.sess.ID); key != "" {
				if owner, ok := claimed[key]; ok {
					if owner != f.root() {
						copied[f.root()] = true
					}
					continue
				}
				claimed[key] = f.root()
			}
			f.sess.Tokens = f.sess.Tokens.Add(m.use.Tokens())
			f.sess.Hours.Add(m.at, m.use)
			if m.null {
				f.nulls++
			}
			if m.rejected != nil {
				f.sess.Rejected = AddRejected(f.sess.Rejected, m.rejected)
			}
		}
	}
	return copied
}

// addTracked raises each session, field by field, to the usage Claude Code
// tracked for it. The tracker also counts calls that never become transcript
// messages, such as compaction and titles, and it covers the session's
// sub-agents, so their rows count toward what the session already has. A
// fork's tracker starts from its parent's, so a session that copies another's
// messages keeps its message count. What the tracker has beyond the messages
// is the session's Side, spent in the hour of the tracker's line: it is not
// spread over hours whose messages the transcript already counts in full.
func addTracked(out []Session, files []*claudeFile, copied map[string]bool) []Session {
	tracked := map[string]Tokens{}
	at := map[string]time.Time{}
	for _, f := range files {
		if f.tracked != nil && !copied[f.sess.ID] {
			// One session under two project directories: the larger of each field.
			t := tracked[f.sess.ID]
			tracked[f.sess.ID] = t.Add(f.tracked.Growth(t))
			if f.trackedAt.After(at[f.sess.ID]) {
				at[f.sess.ID] = f.trackedAt
			}
		}
	}
	if len(tracked) == 0 {
		return out
	}
	counted := map[string]Tokens{}
	for _, s := range out {
		root := cmp.Or(s.ParentID, s.ID)
		counted[root] = counted[root].Add(s.Tokens)
	}
	for i, s := range out {
		if t, ok := tracked[s.ID]; ok && s.ParentID == "" {
			side := t.Growth(counted[s.ID])
			out[i].Tokens = s.Tokens.Add(side)
			out[i].Side = side
			out[i].Hours.Add(cmp.Or(at[s.ID], s.Updated), meter.FromTokens(side))
		}
	}
	return out
}

// addFamilies gives each session the null replies of the files rollup adds
// to it, and the version of the newest message among them: rollup keeps the
// root's own fields and not its sub-agents'. It finds each root as rollup
// does.
func addFamilies(out []Session, files []*claudeFile) {
	at := map[string]int{}
	for i, s := range out {
		if s.ID != "" {
			at[s.ID] = i
		}
	}
	root := func(i int) int {
		seen := map[int]bool{}
		for !seen[i] {
			seen[i] = true
			j, ok := at[out[i].ParentID]
			if out[i].ParentID == "" || !ok || j == i {
				return i
			}
			i = j
		}
		return i
	}
	newest := make([]time.Time, len(out))
	for _, f := range files {
		i, ok := at[f.sess.ID]
		if !ok {
			continue
		}
		r := root(i)
		out[r].Nulls += f.nulls
		if f.last.After(newest[r]) || (f.last.Equal(newest[r]) && f.version > out[r].Version) {
			newest[r], out[r].Version = f.last, f.version
		}
	}
}

type claudeFile struct {
	path  string
	sess  Session
	start time.Time
	// native is a file whose first line names the session the file is for.
	native bool
	msgs   []claudeMsg
	// nulls is how many of the file's counted messages are null replies.
	nulls int
	// last is the newest message's time, and version the Claude Code
	// version that message recorded.
	last    time.Time
	version string
	// tracked is the session's usage from its last cost-state line, which
	// Claude Code writes when it exits. trackedAt is when: the line's own
	// time, or else that of the newest message before it.
	tracked   *Tokens
	trackedAt time.Time
}

// updated is when the file's session was last active: the newest time its
// usage records, else mod, when the file was written. A file without usage,
// such as a workflow journal, dates nothing.
func (f *claudeFile) updated(mod time.Time) time.Time {
	switch {
	case f.trackedAt.After(f.last):
		return f.trackedAt
	case !f.last.IsZero():
		return f.last
	case len(f.msgs) > 0 || f.tracked != nil:
		return mod.UTC()
	}
	return time.Time{}
}

// root is the session this file's usage rolls into.
func (f *claudeFile) root() string { return cmp.Or(f.sess.ParentID, f.sess.ID) }

type claudeMsg struct {
	id      string
	request string
	// anon is a message with neither an id nor a line uuid; it cannot be matched.
	anon bool
	use  meter.Use
	// at is when the message's last snapshot was written.
	at time.Time
	// null is a reply whose last snapshot has no stop reason and next to no
	// output: the stream ended before Claude Code wrote the final count.
	null bool
	// rejected is set on a request Claude refused because a window was full.
	rejected *Limits
}

// key matches the message across files: by its request id when it has one,
// otherwise by its id within the session. An anon message without a request
// id has no key.
func (m claudeMsg) key(session string) string {
	switch {
	case m.request != "":
		return "r\x00" + m.id + "\x00" + m.request
	case !m.anon:
		return "s\x00" + session + "\x00" + m.id
	}
	return ""
}

func parseClaude(path, id, parent string) (*claudeFile, int, error) {
	p := &claudeParse{
		f:      &claudeFile{path: path, sess: Session{ID: id, ParentID: parent}},
		id:     id,
		parent: parent,
		// Sub-agent lines carry their parent's session id.
		own:   cmp.Or(parent, id),
		index: map[string]int{},
	}
	long, err := ForEachLine(path, maxLineBytes, p.line)
	// Older Claude Code wrote sub-agent transcripts beside the session they belong to.
	if parent == "" && p.sidechainOf != "" && p.sidechainOf != id {
		p.f.sess.ParentID = p.sidechainOf
	}
	return p.f, p.malformed + long, err
}

type claudeParse struct {
	f               *claudeFile
	id, parent, own string
	index           map[string]int
	anon, malformed int
	sidechainOf     string
	sawSession      bool
}

func (p *claudeParse) line(line []byte) {
	if !bytes.Contains(line, []byte(`"usage"`)) && !bytes.Contains(line, []byte(`"cwd"`)) && !bytes.Contains(line, []byte(`"cost-state"`)) {
		return
	}
	var row claudeLine
	if json.Unmarshal(line, &row) != nil {
		p.malformed++
		return
	}
	if row.Type == "cost-state" {
		p.costState(row)
		return
	}
	p.header(row)
	if row.Type != "assistant" || row.Message == nil || row.Message.Usage == nil {
		return
	}
	p.message(row)
}

func (p *claudeParse) costState(row claudeLine) {
	if p.parent == "" && row.SessionID == p.id {
		t := row.tracked()
		p.f.tracked = &t
		p.f.trackedAt = cmp.Or(parseTime(row.Timestamp), p.f.last)
	}
}

func (p *claudeParse) header(row claudeLine) {
	if row.Cwd != "" && p.f.sess.Project == "" {
		p.f.sess.Project = strings.TrimSpace(row.Cwd)
	}
	if p.f.start.IsZero() {
		p.f.start = parseTime(row.Timestamp)
	}
	if !p.sawSession && row.SessionID != "" {
		p.sawSession = true
		p.f.native = row.SessionID == p.own
	}
	if row.IsSidechain && row.SessionID != "" && p.sidechainOf == "" {
		p.sidechainOf = row.SessionID
	}
}

func (p *claudeParse) message(row claudeLine) {
	msgID := cmp.Or(row.Message.ID, row.UUID)
	named := msgID != ""
	if !named {
		p.anon++
		msgID = fmt.Sprintf("\x00anon-%d", p.anon)
	}
	u := row.Message.Usage
	at := parseTime(row.Timestamp)
	m := claudeMsg{
		id:      msgID,
		request: row.RequestID,
		anon:    !named,
		use: meter.Use{
			Input:      u.InputTokens,
			Output:     u.OutputTokens,
			CacheRead:  u.CacheReadInputTokens,
			CacheWrite: u.CacheCreationInputTokens,
		},
		at:       at,
		null:     string(row.Message.StopReason) == "null" && u.OutputTokens < nullOutput,
		rejected: claudeRejected(row.QuotaLimits, at),
	}
	if c := u.CacheCreation; c != nil {
		m.use.CacheWrite1h = min(max(c.Ephemeral1h, 0), m.use.CacheWrite)
	}
	if !at.Before(p.f.last) {
		p.f.last, p.f.version = at, row.Version
	}
	i, ok := p.index[msgID]
	if !ok {
		p.index[msgID] = len(p.f.msgs)
		p.f.msgs = append(p.f.msgs, m)
		return
	}
	if m.request == "" {
		m.request = p.f.msgs[i].request
	}
	if m.at.IsZero() {
		m.at = p.f.msgs[i].at
	}
	p.f.msgs[i] = m
}

// nullOutput is the output under which a reply without a stop reason is
// taken as cut short. A reply's first snapshot counts a few tokens of output
// before its stop reason is known; ending on one means the final snapshot,
// with the full count, was never written.
const nullOutput = 20

type claudeLine struct {
	Type        string `json:"type"`
	Version     string `json:"version"`
	Cwd         string `json:"cwd"`
	UUID        string `json:"uuid"`
	RequestID   string `json:"requestId"`
	SessionID   string `json:"sessionId"`
	IsSidechain bool   `json:"isSidechain"`
	Timestamp   string `json:"timestamp"`
	Message     *struct {
		ID string `json:"id"`
		// StopReason stays raw to tell null from a missing field.
		StopReason json.RawMessage `json:"stop_reason"`
		Usage      *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			// CacheCreation splits the cache writes by how long they are kept.
			CacheCreation *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
	// ModelUsage is on cost-state lines: the session's usage so far, per model.
	ModelUsage map[string]struct {
		InputTokens              int64 `json:"inputTokens"`
		OutputTokens             int64 `json:"outputTokens"`
		CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
		CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
	} `json:"modelUsage"`
	// QuotaLimits is on the message of a request Claude answered with a rate
	// limit. It stays raw so an odd value does not reject the line.
	QuotaLimits json.RawMessage `json:"quotaLimits"`
}

// ClaudeWindows are the windows Claude names by key, in its usage cache and
// in the limit it names when it refuses a request, in the order they show.
var ClaudeWindows = []struct {
	Key, Name string
	Minutes   int
}{
	{"five_hour", "5h", 300},
	{"seven_day", "7d", 10080},
	{"seven_day_opus", "7d Opus", 10080},
	{"seven_day_sonnet", "7d Sonnet", 10080},
}

// claudeRejected is the reading a refused request gives: at the time of the
// refusal, the window it names was full, and it stays full until it resets.
// A limit that was not rejected, a window not in ClaudeWindows, or a refusal
// without its times gives none.
func claudeRejected(raw json.RawMessage, at time.Time) *Limits {
	if len(raw) == 0 || at.IsZero() {
		return nil
	}
	var q struct {
		Status        string  `json:"status"`
		RateLimitType string  `json:"rateLimitType"`
		ResetsAt      float64 `json:"resetsAt"`
	}
	if json.Unmarshal(raw, &q) != nil || q.Status != "rejected" || q.ResetsAt <= 0 {
		return nil
	}
	for _, w := range ClaudeWindows {
		if w.Key == q.RateLimitType {
			reset := time.Unix(int64(q.ResetsAt), 0).UTC()
			return &Limits{ObservedAt: at, Windows: []snapshot.Window{{Name: w.Name, Percent: 100, ResetsAt: &reset, Minutes: w.Minutes}}}
		}
	}
	return nil
}

// tracked adds up a cost-state line across models. Model names there differ
// from the ones on messages, so only the totals compare.
func (row claudeLine) tracked() Tokens {
	var t Tokens
	for _, u := range row.ModelUsage {
		t = t.Add(Tokens{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens})
	}
	return t
}
