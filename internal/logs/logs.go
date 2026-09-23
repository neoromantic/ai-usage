// Package logs reads token counts the harnesses already wrote to disk.
//
// It opens session logs and the Hermes session table, and nothing else in a
// harness home. It never writes there. Prompts, tool arguments, and message
// text are skipped at the line filter and never decoded into memory we keep.
package logs

import (
	"fmt"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Tokens is the shared counter type.
type Tokens = snapshot.Tokens

// Session is one root session after sub-agents are rolled in.
type Session struct {
	ID       string
	ParentID string
	Project  string
	Tokens   Tokens
	// Updated is the newest activity the log shows.
	Updated time.Time
	// Account is set only when the log itself names the account (Hermes).
	Account string
	// Parts splits Tokens by the account the log names for each part, when
	// it names one per part (Hermes: the billing provider of each model call,
	// sub-agent, and auxiliary task). The parts add up to Tokens.
	Parts map[string]Tokens
	// Home is the home the session was read from. A session kept in more
	// than one home is in the home of the copy that was written last, or of
	// the larger copy where the logs cannot say.
	Home string
	// Homes lists every home holding the session's log, Home first, when
	// one file is linked into several homes (Orca links each Codex rollout
	// into every account's home). The file does not say which of them ran it.
	Homes []string
	// Limits is the newest main quota reading the session's log recorded,
	// which is the quota of the account that served it (Codex).
	Limits *Limits
	// Rejected holds, for each window, the newest request of the session
	// that was refused because that window was full, as a reading of that
	// window alone at 100% (Claude). A weekly window refused before a 5h
	// one stays full after the 5h one resets.
	Rejected []*Limits
	// Hours is the session's input plus output tokens by the UTC hour they
	// were spent in, keyed by HourOf. A reader fills it from the times its
	// log records for each use. ReadHomes then makes it add up to Tokens:
	// what the log does not place in time, such as Claude's side calls, is
	// spread over the hours it does place, in their proportion. It stays nil
	// when the log records no times at all, and the ledger then places
	// growth at the run that saw it.
	Hours map[int64]int64
}

// HourOf is the key of the UTC hour t falls in.
func HourOf(t time.Time) int64 { return t.Unix() / 3600 }

// HourStart is when the hour with key h begins.
func HourStart(h int64) time.Time { return time.Unix(h*3600, 0).UTC() }

// AddHour adds n tokens spent at t to hours, making the map when needed.
func AddHour(hours *map[int64]int64, t time.Time, n int64) {
	if n <= 0 || t.IsZero() {
		return
	}
	if *hours == nil {
		*hours = map[int64]int64{}
	}
	(*hours)[HourOf(t)] += n
}

// InOut is the input plus output of t, the count the report's periods show.
func InOut(t Tokens) int64 { return t.Input + t.Output }

// fitHours makes a session's hours add up to its input plus output. The
// part no time was recorded for, such as Claude's side calls, is spread
// over the hours with a time in their proportion: side calls go along with
// the work that makes them, and a resumed session does not move them all
// to its newest day. With no hour placed, it all goes to the hour of the
// last activity. A sum above the total, which only a reader's bug makes, is
// scaled down.
func fitHours(s *Session) {
	if s.Hours == nil {
		return
	}
	want := InOut(s.Tokens)
	var sum int64
	for h, n := range s.Hours {
		if n <= 0 {
			delete(s.Hours, h)
			continue
		}
		sum += n
	}
	switch {
	case sum == 0 && want > 0 && !s.Updated.IsZero():
		s.Hours[HourOf(s.Updated)] = want
	case sum != want:
		ScaleHours(s.Hours, want)
	}
}

// ScaleHours scales hours in place so that they add up to total, keeping
// their shape. What rounding leaves goes to the largest hour.
func ScaleHours(hours map[int64]int64, total int64) {
	var sum int64
	var largest int64
	first := true
	for h, n := range hours {
		sum += n
		if first || n > hours[largest] || (n == hours[largest] && h > largest) {
			largest, first = h, false
		}
	}
	if sum == total || sum <= 0 {
		return
	}
	left := total
	for h, n := range hours {
		v := int64(float64(n) * float64(total) / float64(sum))
		hours[h] = v
		left -= v
	}
	hours[largest] += left
	for h, n := range hours {
		if n <= 0 {
			delete(hours, h)
		}
	}
}

// Result is a read. Malformed and Unreadable make a source partial.
type Result struct {
	Sessions   []Session
	Malformed  int
	Unreadable int
	// Limits is the newest quota the logs recorded, when they record one
	// (Codex). Read sets it; ReadHomes keeps it per home.
	Limits *Limits
	// Homes is how each home's read went, for ReadHomes.
	Homes map[string]HomeRead
}

// HomeRead is one home's part of a read.
type HomeRead struct {
	// Err is set when the home could not be read. Sessions read before the
	// error are still in the result.
	Err        error
	Malformed  int
	Unreadable int
	// Limits is the newest quota this home's logs recorded. A home belongs
	// to whoever is logged in there, so limits are never mixed across homes.
	Limits *Limits
}

// Limits is a quota reading found in a log.
type Limits struct {
	ObservedAt time.Time
	Plan       string
	Windows    []snapshot.Window
}

// Read reads one provider home. Files older than since are skipped.
// Sub-agent sessions are rolled into their parents.
func Read(provider, home string, since time.Time) (Result, error) {
	res := ReadHomes(provider, []string{home}, since)
	h := res.Homes[home]
	res.Limits = h.Limits
	res.Homes = nil
	return res, h.Err
}

// ReadHomes reads every home of one provider as one set of logs, so that a
// session kept in two homes counts once, and a sub-agent, fork, or resumed
// copy in one home of a session in another does not count the parent's usage
// again. Each session is tagged with its Home. Files older than since are
// skipped, and sub-agent sessions are rolled into their parents.
func ReadHomes(provider string, homes []string, since time.Time) Result {
	res := Result{Homes: map[string]HomeRead{}}
	var raw []Session
	switch provider {
	case "claude":
		var files []*claudeFile
		for _, h := range homes {
			fs, hr := claudeFiles(h, since)
			files = append(files, fs...)
			res.Homes[h] = hr
		}
		raw = countClaude(files)
	case "codex":
		raw = readCodexHomes(homes, since, res.Homes)
	case "grok", "hermes":
		read := readGrok
		if provider == "hermes" {
			read = readHermes
		}
		for _, h := range homes {
			r, err := read(h, since)
			for i := range r.Sessions {
				r.Sessions[i].Home = h
			}
			raw = append(raw, r.Sessions...)
			res.Homes[h] = HomeRead{Err: err, Malformed: r.Malformed, Unreadable: r.Unreadable}
		}
	default:
		for _, h := range homes {
			res.Homes[h] = HomeRead{Err: fmt.Errorf("unknown provider %s", provider)}
		}
	}
	res.Sessions = rollup(dedupeSessions(raw))
	for i := range res.Sessions {
		fitHours(&res.Sessions[i])
	}
	if provider == "hermes" {
		// A gateway session, from Telegram and the like, has no working
		// directory. Its Hermes home says which agent it was.
		for i, s := range res.Sessions {
			if s.Project == UnknownProject && s.Home != "" {
				res.Sessions[i].Project = s.Home
			}
		}
	}
	for _, hr := range res.Homes {
		res.Malformed += hr.Malformed
		res.Unreadable += hr.Unreadable
	}
	return res
}
