package logs

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/neoromantic/ai-usage/internal/fsutil"
)

// readSQLite runs read in one read-only transaction on the database at
// dbPath, without changing the directory it is in. A read that assumed the
// database would not change is done again while it changes, up to
// hermesAttempts times, so read can run more than once.
func readSQLite(dbPath string, read func(tx *sql.Tx) error) error {
	// SQLite keeps the -wal and -shm beside the file a link points to.
	dbPath = fsutil.RealPath(dbPath)
	for attempt := 1; ; attempt++ {
		before, err := statDB(dbPath)
		if err != nil {
			return err
		}
		live, err := readSQLiteOnce(dbPath, before, read)
		afterHermesRead()
		if live || attempt == hermesAttempts {
			return err
		}
		// A read that took the database as unchanging, or a copy of it, can
		// mix pages from before and after a Hermes that opened it meanwhile.
		if after, serr := statDB(dbPath); serr == nil && after == before {
			return err
		}
	}
}

// hermesAttempts bounds the reads of a database that keeps changing under a
// read that assumed it would not.
const hermesAttempts = 3

// afterHermesRead runs after each read of a state.db. Tests change the
// database there.
var afterHermesRead = func() {}

// dbState is what a write to a database changes: its size and time, and the
// -wal and -shm files a writer holds open.
type dbState struct {
	size, mod int64
	wal, shm  bool
}

func statDB(dbPath string) (dbState, error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		return dbState{}, err
	}
	st := dbState{size: info.Size(), mod: info.ModTime().UnixNano()}
	if st.wal, err = exists(dbPath + "-wal"); err != nil {
		return st, err
	}
	st.shm, err = exists(dbPath + "-shm")
	return st, err
}

// readSQLiteOnce runs read once, on the database as st found it. live is
// whether it was read in place while Hermes had it open.
func readSQLiteOnce(dbPath string, st dbState, read func(tx *sql.Tx) error) (live bool, err error) {
	uri, live, cleanup, err := hermesURI(dbPath, st)
	if err != nil {
		return false, err
	}
	defer cleanup()

	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return live, err
	}
	defer db.Close()
	// One transaction, so all of read's queries, such as Hermes' usage and
	// sessions queries, see the same commit of a database Hermes is writing.
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return live, err
	}
	defer tx.Rollback()
	err = read(tx)
	return live, err
}

func tableColumns(tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
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
// scheduler reads them every 15 minutes. st says which files exist. live is
// whether the database is read in place with Hermes' own -wal and -shm.
func hermesURI(dbPath string, st dbState) (uri string, live bool, cleanup func(), err error) {
	switch {
	case st.wal && st.shm:
		return sqliteURI(dbPath), true, func() {}, nil
	case !st.wal && !st.shm:
		return sqliteURI(dbPath) + "&immutable=1", false, func() {}, nil
	}
	snapshot, cleanup, err := snapshotSQLite(dbPath, st)
	if err != nil {
		return "", false, nil, err
	}
	return sqliteURI(snapshot), false, cleanup, nil
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func snapshotSQLite(dbPath string, st dbState) (string, func(), error) {
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
	if st.wal {
		if err := copyFile(filepath.Join(tmp, base+"-wal"), dbPath+"-wal"); err != nil {
			cleanup()
			return "", nil, err
		}
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
