package logs

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// codexThreads is what Codex's own thread table in one home says about who
// created its threads: the account id that created each, by thread id, and
// the one that created the home's newest thread, which is the account
// creating threads there now.
type codexThreads struct {
	newest   string
	creators map[string]string
}

var codexStateRe = regexp.MustCompile(`^state_([0-9]+)\.sqlite$`)

// readCodexThreads reads the account ids in <home>/state_N.sqlite, the
// newest N, read-only. The query names the thread id, its creator's account
// id and its times, never a title, message or path. The ids are OpenAI's
// account ids, not credentials, and Codex writes them only for some
// threads; a home without the database, or one older than the column, or
// one that cannot be read, says nothing.
func readCodexThreads(home string) codexThreads {
	var out codexThreads
	db := codexStateDB(home)
	if db == "" {
		return out
	}
	// A database that cannot be read, locked or damaged, costs only the ids.
	_ = readSQLite(db, func(tx *sql.Tx) error {
		out = codexThreads{}
		cols, err := tableColumns(tx, "threads")
		if err != nil || !cols["id"] || !cols["creator_account_id"] {
			return err
		}
		// A row's last update, or its creation where it has none.
		at := "NULL"
		switch {
		case cols["updated_at"] && cols["created_at"]:
			at = "COALESCE(updated_at, created_at)"
		case cols["updated_at"]:
			at = "updated_at"
		case cols["created_at"]:
			at = "created_at"
		}
		rows, err := tx.Query(`SELECT CAST(id AS TEXT), CAST(creator_account_id AS TEXT) FROM threads
			WHERE creator_account_id IS NOT NULL AND TRIM(creator_account_id) <> '' ORDER BY ` + at + ` DESC, rowid DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, acct sql.NullString
			if rows.Scan(&id, &acct) != nil {
				continue
			}
			a := strings.TrimSpace(acct.String)
			if a == "" || len(a) > 200 {
				continue
			}
			if out.newest == "" {
				out.newest = a
			}
			if t := strings.TrimSpace(id.String); t != "" {
				if out.creators == nil {
					out.creators = map[string]string{}
				}
				out.creators[t] = a
			}
		}
		return rows.Err()
	})
	return out
}

// codexStateDB is the path of the newest state_N.sqlite in home, or "".
func codexStateDB(home string) string {
	entries, err := os.ReadDir(home)
	if err != nil {
		return ""
	}
	best, bestN := "", -1
	for _, e := range entries {
		m := codexStateRe.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > bestN {
			best, bestN = e.Name(), n
		}
	}
	if best == "" {
		return ""
	}
	return filepath.Join(home, best)
}
