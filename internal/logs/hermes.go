package logs

import (
	"cmp"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
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
// Both tables hold running totals, not a time for each call, so a session
// leaves Hours nil and the ledger places its growth at the run that sees it.
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

	for rows.Next() {
		var id, parent, cwd, billing sql.NullString
		var input, output, cacheRead, cacheWrite int64
		var started, ended, active any
		if err := rows.Scan(&id, &parent, &cwd, &billing, &input, &output, &cacheRead, &cacheWrite, &started, &ended, &active); err != nil {
			return sessions, malformed, err
		}
		if id.String == "" {
			malformed++
			continue
		}
		last := slices.MaxFunc([]time.Time{looseTime(started), looseTime(ended), looseTime(active)}, time.Time.Compare)
		u := usage[id.String]
		if u != nil && u.last.After(last) {
			last = u.last
		}
		// A row with no time stays. Dropping it would hide usage we cannot date.
		if !since.IsZero() && !last.IsZero() && last.Unix() < since.Unix() {
			continue
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
		sess.Parts = u.parts(sess.Account, sess.Tokens)
		sess.Tokens = Tokens{}
		for _, t := range sess.Parts {
			sess.Tokens = sess.Tokens.Add(t)
		}
		sessions = append(sessions, sess)
	}
	return sessions, malformed, rows.Err()
}

// hermesSessionUsage is one session's session_model_usage rows by billing
// provider.
type hermesSessionUsage struct {
	main map[string]Tokens
	aux  map[string]Tokens
	// last is the newest last_seen.
	last time.Time
}

// parts splits a session's tokens by billing provider. The main loop's rows
// split the sessions row's count; what they do not cover, as in a row from
// before Hermes kept them, is the sessions row's own billing provider's.
// Auxiliary calls are added on top. u may be nil.
func (u *hermesSessionUsage) parts(billing string, row Tokens) map[string]Tokens {
	out := map[string]Tokens{}
	var covered Tokens
	if u != nil {
		for p, t := range u.main {
			out[p] = out[p].Add(t)
			covered = covered.Add(t)
		}
	}
	if rest := row.Growth(covered); !rest.Zero() {
		out[billing] = out[billing].Add(rest)
	}
	if u != nil {
		for p, t := range u.aux {
			out[p] = out[p].Add(t)
		}
	}
	for p, t := range out {
		if t.Zero() {
			delete(out, p)
		}
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
	// MAX over a column that mixes numbers and ISO strings returns a string,
	// so each kind has its own.
	rows, err := tx.Query(`
		SELECT session_id,
		       ` + columnOr(cols, "billing_provider", "COALESCE(billing_provider, '')", "''") + `,
		       ` + columnOr(cols, "task", "COALESCE(task, '') <> ''", "0") + `,
		       ` + columnOr(cols, "input_tokens", "COALESCE(SUM(input_tokens), 0)", "0") + `,
		       ` + columnOr(cols, "output_tokens", "COALESCE(SUM(output_tokens), 0)", "0") + `,
		       ` + columnOr(cols, "cache_read_tokens", "COALESCE(SUM(cache_read_tokens), 0)", "0") + `,
		       ` + columnOr(cols, "cache_write_tokens", "COALESCE(SUM(cache_write_tokens), 0)", "0") + `,
		       ` + columnOr(cols, "last_seen", "COALESCE(MAX(CASE WHEN typeof(last_seen) IN ('integer', 'real') THEN last_seen END), 0)", "0") + `,
		       ` + columnOr(cols, "last_seen", "MAX(CASE WHEN typeof(last_seen) = 'text' THEN last_seen END)", "NULL") + `
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
		var last float64
		var lastText any
		if err := rows.Scan(&id, &provider, &isAux, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &last, &lastText); err != nil {
			return nil, err
		}
		u := out[id.String]
		if u == nil {
			u = &hermesSessionUsage{main: map[string]Tokens{}, aux: map[string]Tokens{}}
			out[id.String] = u
		}
		provider = strings.TrimSpace(provider)
		if isAux {
			// Hermes leaves the route of a fallback call empty rather than
			// credit the main loop's route with it.
			provider = cmp.Or(provider, UnknownAccount)
			u.aux[provider] = u.aux[provider].Add(t)
		} else {
			u.main[provider] = u.main[provider].Add(t)
		}
		u.last = slices.MaxFunc([]time.Time{u.last, looseTime(last), looseTime(lastText)}, time.Time.Compare)
	}
	return out, rows.Err()
}

// columnOr is expr when the table has the column name, and fallback when it
// is from a Hermes release before that column.
func columnOr(cols map[string]bool, name, expr, fallback string) string {
	if cols[name] {
		return expr
	}
	return fallback
}
