package logs

import (
	"cmp"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/meter"
)

// readHermes reads token columns from <home>/state.db.
// The query names those columns. Message text and prompts stay in the table.
// billing_provider names the account the session billed, when the table has it.
//
// A sessions row counts the main loop only. Where session_model_usage exists,
// its rows split that count by billing provider, and its rows with a task
// (vision, compression, title generation, background review) are auxiliary
// calls no sessions row counts. Each session's Parts are its tokens by
// billing provider, main loop and auxiliary calls together.
//
// Both tables hold running totals, not a time for each call, so Hours is an
// estimate: see hermesHours.
func readHermes(home string, since time.Time) ([]Session, HomeRead) {
	dbPath := filepath.Join(home, "state.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return nil, HomeRead{}
	} else if err != nil {
		return nil, HomeRead{Err: err}
	}
	var sessions []Session
	var malformed int
	err := readSQLite(dbPath, func(tx *sql.Tx) (err error) {
		sessions, malformed, err = readHermesTx(tx, since)
		return err
	})
	return sessions, HomeRead{Err: err, Malformed: malformed}
}

// betweenHermesQueries runs between the usage and sessions queries of a
// state.db. Tests change the database there.
var betweenHermesQueries = func() {}

// hermesSession is a sessions row as read, before its chain is known.
type hermesSession struct {
	s Session
	// span is when the row says the session ran, from its start to its end
	// or last activity.
	span [2]time.Time
}

func readHermesTx(tx *sql.Tx, since time.Time) (sessions []Session, malformed int, err error) {
	cols, err := tableColumns(tx, "sessions")
	if err != nil {
		return nil, 0, err
	}
	if len(cols) == 0 {
		return nil, 0, errors.New("hermes state.db has no sessions table")
	}
	usage, err := hermesUsage(tx)
	if err != nil {
		return nil, 0, err
	}
	betweenHermesQueries()
	// Columns were added over Hermes releases. Token and time columns an older
	// table lacks read as zero or unknown; a table without the basic token
	// counts is not one this reader knows, and the query fails visibly.
	//
	// Times are REAL seconds, but a row can hold an ISO string instead, so
	// each column is read as it is and parsed here.
	started := columnOr(cols, "started_at", "started_at", "NULL")
	ended := columnOr(cols, "ended_at", "ended_at", "NULL")
	activity := columnOr(cols, "last_activity_at", "last_activity_at", "NULL")
	query := `
		SELECT id,
		       ` + columnOr(cols, "parent_session_id", "parent_session_id", "NULL") + `,
		       ` + columnOr(cols, "cwd", "cwd", "NULL") + `,
		       ` + columnOr(cols, "billing_provider", "billing_provider", "NULL") + `,
		       COALESCE(input_tokens, 0),
		       COALESCE(output_tokens, 0),
		       ` + columnOr(cols, "cache_read_tokens", "COALESCE(cache_read_tokens, 0)", "0") + `,
		       ` + columnOr(cols, "cache_write_tokens", "COALESCE(cache_write_tokens, 0)", "0") + `,
		       ` + started + `,
		       ` + ended + `,
		       ` + activity + `
		FROM sessions
		ORDER BY id`
	rows, err := tx.Query(query)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var read []hermesSession
	for rows.Next() {
		var id, parent, cwd, billing sql.NullString
		var input, output, cacheRead, cacheWrite int64
		var startedAt, endedAt, active any
		if err := rows.Scan(&id, &parent, &cwd, &billing, &input, &output, &cacheRead, &cacheWrite, &startedAt, &endedAt, &active); err != nil {
			return sessions, malformed, err
		}
		if id.String == "" {
			malformed++
			continue
		}
		start, end := looseTime(startedAt), later2(looseTime(endedAt), looseTime(active))
		last := later2(start, end)
		u := usage[id.String]
		if u != nil && u.last.After(last) {
			last = u.last
		}
		sess := Session{
			ID:       id.String,
			ParentID: parent.String,
			Project:  strings.TrimSpace(cwd.String),
			Account:  strings.TrimSpace(billing.String),
			Tokens: Tokens{
				Input:      input,
				Output:     output,
				CacheRead:  cacheRead,
				CacheWrite: cacheWrite,
			},
			Updated: last,
		}
		read = append(read, hermesSession{s: sess, span: [2]time.Time{firstSet(start, end), firstSet(end, start)}})
	}
	if err := rows.Err(); err != nil {
		return sessions, malformed, err
	}
	read = activeChains(read, since)
	messages, err := hermesMessageHours(tx, since)
	if err != nil {
		return sessions, malformed, err
	}
	for _, r := range read {
		sess := r.s
		calls := usage[sess.ID].calls(sess.Account, sess.Tokens, r.span)
		sess.Parts, sess.Tokens = map[string]Tokens{}, Tokens{}
		for _, c := range calls {
			if !c.t.Zero() {
				sess.Parts[c.route] = sess.Parts[c.route].Add(c.t)
				sess.Tokens = sess.Tokens.Add(c.t)
			}
		}
		sess.Hours = hermesHours(calls, messages[sess.ID], since)
		sessions = append(sessions, sess)
	}
	// A release before v0.4.0 read a fresh child of a root older than since
	// as a root of its own; the ledger moves what it kept for it onto the root.
	foldFamilies(sessions, nil)
	return sessions, malformed, nil
}

// firstSet is the first time that is not zero, or zero.
func firstSet(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// later2 is the later of two times.
func later2(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// activeChains keeps the sessions of each chain with a session active since
// since, and drops the chains all of whose dated sessions are older. A chain
// is a root with the sessions it continued into: sub-agents, compression
// continuations and resets. It counts under its root as one session, so it
// is kept or dropped whole: a fresh child of an old root would otherwise be
// read as a root of its own, and count again what the ledger counted under
// its root. A chain with no time at all stays: dropping it would hide usage
// we cannot date.
func activeChains(in []hermesSession, since time.Time) []hermesSession {
	if since.IsZero() {
		return in
	}
	at := map[string]int{}
	for i, r := range in {
		at[r.s.ID] = i
	}
	rootOf := func(i int) int {
		seen := map[int]bool{}
		for !seen[i] {
			seen[i] = true
			j, ok := at[in[i].s.ParentID]
			if in[i].s.ParentID == "" || !ok {
				break
			}
			i = j
		}
		return i
	}
	roots := make([]int, len(in))
	newest := map[int]time.Time{}
	for i, r := range in {
		roots[i] = rootOf(i)
		newest[roots[i]] = later2(newest[roots[i]], r.s.Updated)
	}
	var out []hermesSession
	for i, r := range in {
		if last := newest[roots[i]]; last.IsZero() || last.Unix() >= since.Unix() {
			out = append(out, r)
		}
	}
	return out
}

// hermesCall is what a session spent on one billing route, in its main loop
// or in auxiliary calls, and when it did.
type hermesCall struct {
	route string
	aux   bool
	t     Tokens
	// first and last are the first and last time the route was used, zero
	// when not known.
	first, last time.Time
}

// hermesSessionUsage is one session's session_model_usage rows by billing
// provider.
type hermesSessionUsage struct {
	rows []hermesCall
	// last is the newest last_seen.
	last time.Time
}

// calls splits a session's tokens by billing provider. The main loop's rows
// split the sessions row's count; what they do not cover, as in a row from
// before Hermes kept them, is the sessions row's own billing provider's, over
// the time the row says the session ran. Auxiliary calls are added on top.
// Each call takes the session's span for the times its rows do not record.
// u may be nil.
func (u *hermesSessionUsage) calls(billing string, row Tokens, span [2]time.Time) []hermesCall {
	var out []hermesCall
	var covered Tokens
	if u != nil {
		for _, c := range u.rows {
			if !c.aux {
				out = append(out, c)
				covered = covered.Add(c.t)
			}
		}
	}
	if rest := row.Growth(covered); !rest.Zero() {
		out = append(out, hermesCall{route: billing, t: rest})
	}
	if u != nil {
		for _, c := range u.rows {
			if c.aux {
				out = append(out, c)
			}
		}
	}
	for i := range out {
		out[i].first = firstSet(out[i].first, span[0], out[i].last)
		out[i].last = firstSet(out[i].last, span[1], out[i].first)
	}
	return out
}

// hermesUsage reads session_model_usage by session. A state.db from before
// the table has none. One from before the task column holds main-loop rows
// only.
func hermesUsage(tx *sql.Tx) (map[string]*hermesSessionUsage, error) {
	cols, err := tableColumns(tx, "session_model_usage")
	if err != nil || len(cols) == 0 {
		return nil, err
	}
	// MIN and MAX over a column that mixes numbers and ISO strings return a
	// string, so each kind has its own.
	seen := func(name, agg string) string {
		return columnOr(cols, name, agg+"(CASE WHEN typeof("+name+") IN ('integer', 'real') AND "+name+" > 0 THEN "+name+" END)", "NULL") + `,
		       ` + columnOr(cols, name, agg+"(CASE WHEN typeof("+name+") = 'text' THEN "+name+" END)", "NULL")
	}
	rows, err := tx.Query(`
		SELECT session_id,
		       ` + columnOr(cols, "billing_provider", "COALESCE(billing_provider, '')", "''") + `,
		       ` + columnOr(cols, "task", "COALESCE(task, '') <> ''", "0") + `,
		       ` + columnOr(cols, "input_tokens", "COALESCE(SUM(input_tokens), 0)", "0") + `,
		       ` + columnOr(cols, "output_tokens", "COALESCE(SUM(output_tokens), 0)", "0") + `,
		       ` + columnOr(cols, "cache_read_tokens", "COALESCE(SUM(cache_read_tokens), 0)", "0") + `,
		       ` + columnOr(cols, "cache_write_tokens", "COALESCE(SUM(cache_write_tokens), 0)", "0") + `,
		       ` + seen("first_seen", "MIN") + `,
		       ` + seen("last_seen", "MAX") + `
		FROM session_model_usage
		GROUP BY 1, 2, 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*hermesSessionUsage{}
	for rows.Next() {
		var id sql.NullString
		var provider string
		var isAux bool
		var t Tokens
		var first, firstText, last, lastText any
		if err := rows.Scan(&id, &provider, &isAux, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &first, &firstText, &last, &lastText); err != nil {
			return nil, err
		}
		u := out[id.String]
		if u == nil {
			u = &hermesSessionUsage{}
			out[id.String] = u
		}
		provider = strings.TrimSpace(provider)
		if isAux {
			// Hermes leaves the route of a fallback call empty rather than
			// credit the main loop's route with it.
			provider = cmp.Or(provider, UnknownAccount)
		}
		c := hermesCall{route: provider, aux: isAux, t: t, last: later2(looseTime(last), looseTime(lastText))}
		c.first = earlier(looseTime(first), looseTime(firstText))
		u.rows = append(u.rows, c)
		u.last = later2(u.last, c.last)
	}
	return out, rows.Err()
}

// earlier is the earlier of two times, zero times not counting.
func earlier(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

// hermesMessageHours counts each session's assistant messages by hour: one
// for each model reply, the calls that spend tokens. Only the session, role
// and time of each message are read. A state.db whose messages have no time
// has none. Hours before since count as one, the hour hermesHours keeps for
// everything before since.
func hermesMessageHours(tx *sql.Tx, since time.Time) (map[string]map[int64]int64, error) {
	cols, err := tableColumns(tx, "messages")
	if err != nil || !cols["session_id"] || !cols["timestamp"] {
		return nil, err
	}
	// Times are REAL seconds, and hours are counted in the query. One held
	// as milliseconds or as an ISO string is read here as looseTime reads it.
	rows, err := tx.Query(`
		SELECT session_id,
		       CASE WHEN typeof(timestamp) IN ('integer', 'real') AND timestamp > 0 AND timestamp < 1e15
		            THEN CAST(CASE WHEN timestamp >= 1e11 THEN timestamp / 1000 ELSE timestamp END AS INTEGER) / 3600 END,
		       CASE WHEN typeof(timestamp) = 'text' THEN timestamp END,
		       COUNT(*)
		FROM messages
		WHERE ` + columnOr(cols, "role", "role = 'assistant'", "1") + ` AND timestamp IS NOT NULL
		GROUP BY 1, 2, 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[int64]int64{}
	for rows.Next() {
		var id sql.NullString
		var hour sql.NullInt64
		var text any
		var n int64
		if err := rows.Scan(&id, &hour, &text, &n); err != nil {
			return nil, err
		}
		h := hour.Int64
		if !hour.Valid {
			at := looseTime(text)
			if at.IsZero() {
				continue
			}
			h = HourOf(at)
		}
		if out[id.String] == nil {
			out[id.String] = map[int64]int64{}
		}
		out[id.String][keptHour(h, since)] += n
	}
	return out, rows.Err()
}

// keptHour is h, or for an hour before since, the one hour hermesHours keeps
// for all of them: the ledger keeps no hour before since, and a session that
// began years ago need not have an hour for each.
func keptHour(h int64, since time.Time) int64 {
	if since.IsZero() {
		return h
	}
	// Two hours before since's, so it is never an hour the ledger keeps.
	return max(h, HourOf(since)-2)
}

// maxSpan bounds the hours a session's span covers after since, against a
// time far in the future.
const maxSpan = 400 * 24 * time.Hour

// hermesHours places a session's tokens in hours. Hermes records the time of
// each message but not what each call spent, so each call's tokens are spread
// over the hours of its span in proportion to the larger of two shares: the
// hour's share of the session's replies in the span, and its share of the
// span. The replies say when the session worked; the span keeps an hour of
// many cheap replies from taking what hours of fewer, larger ones spent. A
// call with neither has no hours, and ReadHomes spreads it with the rest.
func hermesHours(calls []hermesCall, messages map[int64]int64, since time.Time) meter.Hours {
	var hours meter.Hours
	for _, c := range calls {
		span := spanHours(c.first, c.last, since)
		inSpan := messages
		if len(span) > 0 {
			lo, hi := keptHour(HourOf(c.first), since), keptHour(HourOf(c.last), since)
			inSpan = map[int64]int64{}
			for h, n := range messages {
				if h >= min(lo, hi) && h <= max(lo, hi) {
					inSpan[h] = n
				}
			}
		}
		hours = meter.AddHours(hours, meter.Spread(meter.FromTokens(c.t), hermesShape(inSpan, span)))
	}
	return hours
}

// hermesShape weighs each hour by the larger of its share of messages and
// of span.
func hermesShape(messages, span map[int64]int64) map[int64]int64 {
	// Shares are counted in parts of shareUnit.
	const shareUnit = 1 << 30
	sumOf := func(m map[int64]int64) (s int64) {
		for _, n := range m {
			s += n
		}
		return s
	}
	ms, ss := sumOf(messages), sumOf(span)
	out := map[int64]int64{}
	if ms > 0 {
		for h, n := range messages {
			out[h] = n * shareUnit / ms
		}
	}
	if ss > 0 {
		for h, n := range span {
			out[h] = max(out[h], n*shareUnit/ss)
		}
	}
	return out
}

// spanHours is the seconds of each hour from a to b, with the hours before
// since as one. A span of an instant is that instant's hour.
func spanHours(a, b, since time.Time) map[int64]int64 {
	if a.IsZero() || b.IsZero() {
		return nil
	}
	if b.Before(a) {
		a, b = b, a
	}
	out := map[int64]int64{}
	if !b.After(a) {
		out[keptHour(HourOf(a), since)] = 1
		return out
	}
	if !since.IsZero() && a.Before(since) {
		if !b.After(since) {
			out[keptHour(HourOf(a), since)] = int64(b.Sub(a).Seconds()) + 1
			return out
		}
		out[keptHour(HourOf(a), since)] = int64(since.Sub(a).Seconds())
		a = since
	}
	if b.Sub(a) > maxSpan {
		b = a.Add(maxSpan)
	}
	for h := HourOf(a); h <= HourOf(b); h++ {
		lo, hi := later2(a, HourStart(h)), HourStart(h+1)
		if b.Before(hi) {
			hi = b
		}
		if d := int64(hi.Sub(lo).Seconds()); d > 0 {
			out[h] += d
		}
	}
	if len(out) == 0 {
		out[HourOf(a)] = 1
	}
	return out
}

// columnOr is expr when the table has the column name, and fallback when it
// is from a Hermes release before that column.
func columnOr(cols map[string]bool, name, expr, fallback string) string {
	if cols[name] {
		return expr
	}
	return fallback
}
