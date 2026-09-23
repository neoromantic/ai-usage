package logs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// readGrok sums turn_completed usage in ~/.grok/sessions/<cwd>/<id>/updates.jsonl.
// Each prompt id counts once, as the last turn_completed for that id, in the
// hour that line was written.
// xAI counts cached input inside inputTokens, so cached tokens are moved out of Input.
// Chat transcripts and prompt history stay closed.
func readGrok(home string, since time.Time) (Result, error) {
	sessions := filepath.Join(home, "sessions")
	root, err := resolveDir(sessions)
	if errors.Is(err, os.ErrNotExist) {
		return Result{}, nil
	}
	if err != nil {
		return Result{}, err
	}

	type files struct {
		summary string
		updates string
		mod     time.Time
	}
	found := map[string]*files{}
	var out Result
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			out.Unreadable++
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if deniedFile(name) || (name != "summary.json" && name != "updates.jsonl") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 3 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			out.Unreadable++
			return nil
		}
		dir := filepath.Dir(path)
		slot := found[dir]
		if slot == nil {
			slot = &files{}
			found[dir] = slot
		}
		if info.ModTime().After(slot.mod) {
			slot.mod = info.ModTime()
		}
		switch name {
		case "summary.json":
			slot.summary = path
		case "updates.jsonl":
			slot.updates = path
		}
		return nil
	})
	if err != nil {
		return out, err
	}

	dirs := make([]string, 0, len(found))
	for dir := range found {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		slot := found[dir]
		if !freshEnough(slot.mod, since) {
			continue
		}
		sess := Session{
			ID:      filepath.Base(dir),
			Project: decodeGrokPath(filepath.Base(filepath.Dir(dir))),
			Updated: slot.mod.UTC(),
		}
		if slot.summary != "" {
			id, project, bad, err := parseGrokSummary(slot.summary)
			out.Malformed += bad
			if err != nil {
				out.Unreadable++
			} else {
				if id != "" {
					sess.ID = id
				}
				if project != "" {
					sess.Project = project
				}
			}
		}
		if slot.updates != "" {
			tok, hours, bad, err := parseGrokUpdates(slot.updates)
			out.Malformed += bad
			if err != nil {
				out.Unreadable++
			}
			sess.Tokens, sess.Hours = tok, hours
		}
		out.Sessions = append(out.Sessions, sess)
	}
	return out, nil
}

func parseGrokSummary(path string) (id, project string, malformed int, err error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", "", 0, err
	}
	var doc struct {
		Info struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"info"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return "", "", 1, nil
	}
	return doc.Info.ID, strings.TrimSpace(doc.Info.Cwd), 0, nil
}

// grokTurn is the usage of one prompt and when it was reported.
type grokTurn struct {
	tokens Tokens
	at     time.Time
}

func parseGrokUpdates(path string) (Tokens, map[int64]int64, int, error) {
	latest := map[string]grokTurn{}
	var order []string
	var anon int
	var malformed int
	long, err := forEachLine(path, func(line []byte) {
		if !bytes.Contains(line, []byte(`"turn_completed"`)) {
			return
		}
		var row grokLine
		if json.Unmarshal(line, &row) != nil {
			malformed++
			return
		}
		if row.Params.Update.SessionUpdate != "turn_completed" || row.Params.Update.Usage == nil {
			return
		}
		usage := row.Params.Update.Usage
		prompt := row.Params.Update.PromptID
		if prompt == "" {
			anon++
			prompt = fmt.Sprintf("#%d", anon)
		}
		at := row.Timestamp.Time
		if prev, ok := latest[prompt]; !ok {
			order = append(order, prompt)
		} else if at.IsZero() {
			at = prev.at
		}
		input := usage.InputTokens - usage.CachedReadTokens
		if input < 0 {
			input = 0
		}
		latest[prompt] = grokTurn{at: at, tokens: Tokens{
			Input:      input,
			Output:     usage.OutputTokens,
			CacheRead:  usage.CachedReadTokens,
			CacheWrite: usage.CacheCreationTokens,
		}}
	})
	var total Tokens
	var hours map[int64]int64
	for _, prompt := range order {
		last := latest[prompt]
		total = total.Add(last.tokens)
		AddHour(&hours, last.at, InOut(last.tokens))
	}
	return total, hours, malformed + long, err
}

func decodeGrokPath(enc string) string {
	dec, err := url.PathUnescape(enc)
	if err != nil || strings.TrimSpace(dec) == "" {
		return enc
	}
	return dec
}

type grokLine struct {
	Timestamp unixTime `json:"timestamp"`
	Params    struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			PromptID      string `json:"prompt_id"`
			Usage         *struct {
				InputTokens         int64 `json:"inputTokens"`
				OutputTokens        int64 `json:"outputTokens"`
				CachedReadTokens    int64 `json:"cachedReadTokens"`
				CacheCreationTokens int64 `json:"cacheCreationTokens"`
			} `json:"usage"`
		} `json:"update"`
	} `json:"params"`
}

// unixTime is a time a log writes as Unix seconds or milliseconds, or as an
// RFC 3339 string. A value it cannot read is the zero time rather than an
// error, so one odd time does not reject the line.
type unixTime struct{ time.Time }

func (t *unixTime) UnmarshalJSON(b []byte) error {
	t.Time = time.Time{}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if json.Unmarshal(b, &s) == nil {
			t.Time = parseTime(strings.TrimSpace(s))
		}
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil || !(f > 0 && f < 1e15) {
		return nil
	}
	// Seconds stay below this until the year 5138; milliseconds pass it in 1973.
	if f >= 1e11 {
		f /= 1000
	}
	sec, frac := math.Modf(f)
	t.Time = time.Unix(int64(sec), int64(frac*1e9)).UTC()
	return nil
}
