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
	"sort"
	"strings"
	"time"

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
		f.sess.Updated = mod.UTC()
		f.sess.Home = home
		out.Malformed += bad
		if err != nil {
			out.Unreadable++
		}
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

			subDir := filepath.Join(dir, id, "subagents")
			if _, err := os.Stat(subDir); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					out.Unreadable++
				}
				continue
			}
			_ = filepath.WalkDir(subDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					out.Unreadable++
					return nil
				}
				if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") || deniedFile(d.Name()) {
					return nil
				}
				rel, err := filepath.Rel(subDir, path)
				if err != nil {
					return nil
				}
				var mod time.Time
				if info, err := d.Info(); err == nil {
					mod = info.ModTime()
				}
				read(path, id+"/"+filepath.ToSlash(rel), id, mod)
				return nil
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
// Claude Code tracked for it, when that is more; the log gives no time for
// that part.
func countClaude(files []*claudeFile) []Session {
	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := files[order[a]], files[order[b]]
		if !x.start.Equal(y.start) {
			return x.start.Before(y.start)
		}
		// A copy that kept the original times still names the original
		// session in its lines; the original keeps the messages.
		if x.native != y.native {
			return x.native
		}
		return x.path < y.path
	})
	// claimed names the session each message counts in. A session whose file
	// repeats another session's messages is a copy of that session.
	claimed := map[string]string{}
	copied := map[string]bool{}
	for _, i := range order {
		f := files[i]
		for _, m := range f.msgs {
			key := ""
			switch {
			case m.request != "":
				key = "r\x00" + m.id + "\x00" + m.request
			case !m.anon:
				key = "s\x00" + f.sess.ID + "\x00" + m.id
			}
			if key != "" {
				if owner, ok := claimed[key]; ok {
					if owner != f.root() {
						copied[f.root()] = true
					}
					continue
				}
				claimed[key] = f.root()
			}
			f.sess.Tokens = f.sess.Tokens.Add(m.tokens)
			AddHour(&f.sess.Hours, m.at, InOut(m.tokens))
			if m.rejected != nil {
				f.sess.Rejected = AddRejected(f.sess.Rejected, m.rejected)
			}
		}
	}
	rows := make([]Session, len(files))
	for i, f := range files {
		rows[i] = f.sess
	}
	return addTracked(mergeByID(rows, addCopy), files, copied)
}

// addTracked raises each session, field by field, to the usage Claude Code
// tracked for it. The tracker also counts calls that never become transcript
// messages, such as compaction and titles, and it covers the session's
// sub-agents, so their rows count toward what the session already has. A
// fork's tracker starts from its parent's, so a session that copies another's
// messages keeps its message count.
func addTracked(out []Session, files []*claudeFile, copied map[string]bool) []Session {
	tracked := map[string]Tokens{}
	for _, f := range files {
		if f.tracked != nil && !copied[f.sess.ID] {
			// One session under two project directories: the larger of each field.
			t := tracked[f.sess.ID]
			tracked[f.sess.ID] = t.Add(f.tracked.Growth(t))
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
			out[i].Tokens = s.Tokens.Add(t.Growth(counted[s.ID]))
		}
	}
	return out
}

type claudeFile struct {
	path  string
	sess  Session
	start time.Time
	// native is a file whose first line names the session the file is for.
	native bool
	msgs   []claudeMsg
	// tracked is the session's usage from its last cost-state line, which
	// Claude Code writes when it exits.
	tracked *Tokens
}

// root is the session this file's usage rolls into.
func (f *claudeFile) root() string { return cmp.Or(f.sess.ParentID, f.sess.ID) }

type claudeMsg struct {
	id      string
	request string
	// anon is a message with neither an id nor a line uuid; it cannot be matched.
	anon   bool
	tokens Tokens
	// at is when the message's last snapshot was written.
	at time.Time
	// rejected is set on a request Claude refused because a window was full.
	rejected *Limits
}

func parseClaude(path, id, parent string) (*claudeFile, int, error) {
	f := &claudeFile{path: path, sess: Session{ID: id, ParentID: parent}}
	index := map[string]int{}
	var anon, malformed int
	sidechainOf := ""
	sawSession := false
	// Sub-agent lines carry their parent's session id.
	own := cmp.Or(parent, id)
	long, err := forEachLine(path, func(line []byte) {
		if !bytes.Contains(line, []byte(`"usage"`)) && !bytes.Contains(line, []byte(`"cwd"`)) && !bytes.Contains(line, []byte(`"cost-state"`)) {
			return
		}
		var row claudeLine
		if json.Unmarshal(line, &row) != nil {
			malformed++
			return
		}
		if row.Type == "cost-state" {
			if parent == "" && row.SessionID == id {
				t := row.tracked()
				f.tracked = &t
			}
			return
		}
		if row.Cwd != "" && f.sess.Project == "" {
			f.sess.Project = strings.TrimSpace(row.Cwd)
		}
		if f.start.IsZero() {
			f.start = parseTime(row.Timestamp)
		}
		if !sawSession && row.SessionID != "" {
			sawSession = true
			f.native = row.SessionID == own
		}
		if row.IsSidechain && row.SessionID != "" && sidechainOf == "" {
			sidechainOf = row.SessionID
		}
		if row.Type != "assistant" || row.Message == nil || row.Message.Usage == nil {
			return
		}
		msgID := cmp.Or(row.Message.ID, row.UUID)
		named := msgID != ""
		if !named {
			anon++
			msgID = fmt.Sprintf("\x00anon-%d", anon)
		}
		u := row.Message.Usage
		at := parseTime(row.Timestamp)
		m := claudeMsg{
			id:      msgID,
			request: row.RequestID,
			anon:    !named,
			tokens: Tokens{
				Input:      u.InputTokens,
				Output:     u.OutputTokens,
				CacheRead:  u.CacheReadInputTokens,
				CacheWrite: u.CacheCreationInputTokens,
			},
			at:       at,
			rejected: claudeRejected(row.QuotaLimits, at),
		}
		i, ok := index[msgID]
		if !ok {
			index[msgID] = len(f.msgs)
			f.msgs = append(f.msgs, m)
			return
		}
		if m.request == "" {
			m.request = f.msgs[i].request
		}
		if m.at.IsZero() {
			m.at = f.msgs[i].at
		}
		f.msgs[i] = m
	})
	// Older Claude Code wrote sub-agent transcripts beside the session they belong to.
	if parent == "" && sidechainOf != "" && sidechainOf != id {
		f.sess.ParentID = sidechainOf
	}
	return f, malformed + long, err
}

type claudeLine struct {
	Type        string `json:"type"`
	Cwd         string `json:"cwd"`
	UUID        string `json:"uuid"`
	RequestID   string `json:"requestId"`
	SessionID   string `json:"sessionId"`
	IsSidechain bool   `json:"isSidechain"`
	Timestamp   string `json:"timestamp"`
	Message     *struct {
		ID    string `json:"id"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
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
