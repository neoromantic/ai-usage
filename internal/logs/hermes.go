package logs

import (
	"database/sql"
	"errors"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// readHermes reads token columns from <home>/state.db.
// The query names those columns. Message text and prompts stay in the table.
// billing_provider names the account the session billed, when the table has it.
func readHermes(home string, since time.Time) (Result, error) {
	dbPath := filepath.Join(home, "state.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return Result{}, nil
	} else if err != nil {
		return Result{}, err
	}

	// Opening the live file creates a sqlite shm beside it. Read a private copy
	// so the harness directory stays unchanged.
	snapshot, cleanup, err := snapshotSQLite(dbPath)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	db, err := sql.Open("sqlite", sqliteURI(snapshot))
	if err != nil {
		return Result{}, err
	}
	defer db.Close()

	cols, err := tableColumns(db, "sessions")
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
	started := optional("started_at", "NULL")
	activity := "COALESCE(" + started + ", 0)"
	for _, c := range []string{"ended_at", "last_activity_at"} {
		if cols[c] {
			activity = "MAX(COALESCE(" + c + ", 0), " + activity + ")"
		}
	}
	// Id order makes the account a parent takes from its sub-agents stable.
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
		var started, active sql.NullFloat64
		if err := rows.Scan(&id, &parent, &cwd, &billing, &input, &output, &cacheRead, &cacheWrite, &started, &active); err != nil {
			return out, err
		}
		if id.String == "" {
			out.Malformed++
			continue
		}
		last := started
		if active.Valid && active.Float64 > 0 {
			last = active
		}
		// A row with no time stays. Dropping it would hide usage we cannot date.
		if !since.IsZero() && last.Valid && last.Float64 > 0 && last.Float64 < float64(since.Unix()) {
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
		if last.Valid && last.Float64 > 0 {
			sec, frac := math.Modf(last.Float64)
			sess.Updated = time.Unix(int64(sec), int64(frac*1e9)).UTC()
		}
		out.Sessions = append(out.Sessions, sess)
	}
	return out, rows.Err()
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
	if len(cols) == 0 {
		return nil, errors.New("hermes state.db has no sessions table")
	}
	return cols, rows.Err()
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
