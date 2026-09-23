package logs

import (
	"database/sql"
	"errors"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
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
func readHermes(home string, since time.Time) (Result, error) {
	dbPath := filepath.Join(home, "state.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return Result{}, nil
	} else if err != nil {
		return Result{}, err
	}

	uri, cleanup, err := hermesURI(dbPath)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()

	cols, err := tableColumns(db, "sessions")
	if err != nil {
		return Result{}, err
	}
	if len(cols) == 0 {
		return Result{}, errors.New("hermes state.db has no sessions table")
	}
	usage, err := hermesUsage(db)
	if err != nil {
		return Result{}, err
	}
	// Columns were added over Hermes releases. Token and time columns an older
	// table lacks read as zero or unknown; a table without the basic token
	// counts is not one this reader knows, and the query fails visibly.
	optional := func(name, fallback string) string {
		if cols[name] {
			return name
		}
		return fallback
	}
	count := func(name string) string {
		if cols[name] {
			return "COALESCE(" + name + ", 0)"
		}
		return "0"
	}
	// Times are REAL seconds, but a row can hold an ISO string instead, so
	// each column is read as it is and parsed here.
	started := optional("started_at", "NULL")
	ended := optional("ended_at", "NULL")
	activity := optional("last_activity_at", "NULL")
	query := `
		SELECT id,
		       ` + optional("parent_session_id", "NULL") + `,
		       ` + optional("cwd", "NULL") + `,
		       ` + optional("billing_provider", "NULL") + `,
		       COALESCE(input_tokens, 0),
		       COALESCE(output_tokens, 0),
		       ` + count("cache_read_tokens") + `,
		       ` + count("cache_write_tokens") + `,
		       ` + started + `,
		       ` + ended + `,
		       ` + activity + `
		FROM sessions
		ORDER BY id`
	rows, err := db.Query(query)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()

	var out Result
	for rows.Next() {
		var id, parent, cwd, billing sql.NullString
		var input, output, cacheRead, cacheWrite int64
		var started, ended, active any
		if err := rows.Scan(&id, &parent, &cwd, &billing, &input, &output, &cacheRead, &cacheWrite, &started, &ended, &active); err != nil {
			return out, err
		}
		if id.String == "" {
			out.Malformed++
			continue
		}
		last := math.Max(hermesTime(started), math.Max(hermesTime(ended), hermesTime(active)))
		u := usage[id.String]
		if u != nil && u.last > last {
			last = u.last
		}
		// A row with no time stays. Dropping it would hide usage we cannot date.
		if !since.IsZero() && last > 0 && last < float64(since.Unix()) {
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
		}
		if last > 0 {
			sec, frac := math.Modf(last)
			sess.Updated = time.Unix(int64(sec), int64(frac*1e9)).UTC()
		}
		sess.Parts = u.parts(sess.Account, sess.Tokens)
		sess.Tokens = Tokens{}
		for _, t := range sess.Parts {
			sess.Tokens = sess.Tokens.Add(t)
		}
		out.Sessions = append(out.Sessions, sess)
	}
	return out, rows.Err()
}

// hermesSessionUsage is one session's session_model_usage rows by billing
// provider.
type hermesSessionUsage struct {
	main map[string]Tokens
	aux  map[string]Tokens
	// last is the newest last_seen, in Unix seconds.
	last float64
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
func hermesUsage(db *sql.DB) (map[string]*hermesSessionUsage, error) {
	cols, err := tableColumns(db, "session_model_usage")
	if err != nil || len(cols) == 0 {
		return nil, err
	}
	count := func(name string) string {
		if cols[name] {
			return "COALESCE(SUM(" + name + "), 0)"
		}
		return "0"
	}
	aux := "0"
	if cols["task"] {
		aux = "COALESCE(task, '') <> ''"
	}
	// MAX over a column that mixes numbers and ISO strings returns a string,
	// so each kind has its own.
	seen, seenText := "0", "NULL"
	if cols["last_seen"] {
		seen = "COALESCE(MAX(CASE WHEN typeof(last_seen) IN ('integer', 'real') THEN last_seen END), 0)"
		seenText = "MAX(CASE WHEN typeof(last_seen) = 'text' THEN last_seen END)"
	}
	provider := "''"
	if cols["billing_provider"] {
		provider = "COALESCE(billing_provider, '')"
	}
	rows, err := db.Query(`
		SELECT session_id, ` + provider + `, ` + aux + `,
		       ` + count("input_tokens") + `,
		       ` + count("output_tokens") + `,
		       ` + count("cache_read_tokens") + `,
		       ` + count("cache_write_tokens") + `,
		       ` + seen + `,
		       ` + seenText + `
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
		last = math.Max(last, hermesTime(lastText))
		u := out[id.String]
		if u == nil {
			u = &hermesSessionUsage{main: map[string]Tokens{}, aux: map[string]Tokens{}}
			out[id.String] = u
		}
		provider = strings.TrimSpace(provider)
		if isAux {
			// Hermes leaves the route of a fallback call empty rather than
			// credit the main loop's route with it.
			if provider == "" {
				provider = UnknownAccount
			}
			u.aux[provider] = u.aux[provider].Add(t)
		} else {
			u.main[provider] = u.main[provider].Add(t)
		}
		if last > u.last {
			u.last = last
		}
	}
	return out, rows.Err()
}

// hermesTime is a time Hermes stored, in Unix seconds, or 0 when there is
// none or it cannot be read. Hermes writes REAL seconds; some rows hold an
// ISO 8601 string instead.
func hermesTime(v any) float64 {
	var s string
	switch x := v.(type) {
	case float64:
		return math.Max(x, 0)
	case int64:
		return math.Max(float64(x), 0)
	case []byte:
		s = string(x)
	case string:
		s = x
	default:
		return 0
	}
	s = strings.TrimSpace(s)
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return math.Max(f, 0)
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"} {
		if at, err := time.Parse(layout, s); err == nil {
			return math.Max(float64(at.UnixNano())/1e9, 0)
		}
	}
	return 0
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// hermesURI is how to read state.db without changing the harness directory.
// Hermes keeps it in WAL mode. While Hermes has it open, the -wal and -shm
// files exist, and a read-only connection uses them as every reader does,
// creating nothing. With neither file, nothing is writing it, and it is read
// as immutable, which creates neither. Otherwise, as with a -wal left
// without its -shm, a private copy is read. Copying is the exception, not
// the rule: on a server Hermes databases run to gigabytes, and the
// scheduler reads them every 15 minutes.
func hermesURI(dbPath string) (string, func(), error) {
	wal, err := exists(dbPath + "-wal")
	if err != nil {
		return "", nil, err
	}
	shm, err := exists(dbPath + "-shm")
	if err != nil {
		return "", nil, err
	}
	switch {
	case wal && shm:
		return sqliteURI(dbPath), func() {}, nil
	case !wal && !shm:
		return sqliteURI(dbPath) + "&immutable=1", func() {}, nil
	}
	snapshot, cleanup, err := snapshotSQLite(dbPath)
	if err != nil {
		return "", nil, err
	}
	return sqliteURI(snapshot), cleanup, nil
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func snapshotSQLite(dbPath string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "ai-usage-hermes-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	base := filepath.Base(dbPath)
	if err := copyFile(filepath.Join(tmp, base), dbPath); err != nil {
		cleanup()
		return "", nil, err
	}
	wal := dbPath + "-wal"
	if _, err := os.Stat(wal); err == nil {
		if err := copyFile(filepath.Join(tmp, base+"-wal"), wal); err != nil {
			cleanup()
			return "", nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		cleanup()
		return "", nil, err
	}
	return filepath.Join(tmp, base), cleanup, nil
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func sqliteURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: "mode=ro"}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String()
}
