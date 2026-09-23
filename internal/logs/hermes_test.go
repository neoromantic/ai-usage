package logs

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// hermesColumns is the part of the schema-30 sessions table this reader uses,
// plus text columns it must never select.
const hermesColumns = `
	id TEXT PRIMARY KEY,
	source TEXT,
	system_prompt TEXT,
	title TEXT,
	parent_session_id TEXT,
	started_at REAL,
	ended_at REAL,
	input_tokens INTEGER,
	output_tokens INTEGER,
	cache_read_tokens INTEGER,
	cache_write_tokens INTEGER,
	reasoning_tokens INTEGER,
	cwd TEXT,
	billing_provider TEXT,
	last_activity_at REAL`

// hermesDB creates <home>/state.db with a sessions table and a messages table
// holding text that must not leak. The caller closes it.
func hermesDB(t *testing.T, home, columns string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	// One connection, so pragmas and the open write transaction stay on it.
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`CREATE TABLE sessions (` + columns + `)`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, content TEXT)`,
		`INSERT INTO messages (session_id, content) VALUES ('p', '` + secret + `')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// unix is the REAL seconds Hermes stores. UnixNano does not fit a float64 exactly.
func unix(at time.Time) float64 {
	return float64(at.Unix()) + float64(at.Nanosecond())/1e9
}

// hermesRow is a schema-30 row. Zero times are stored as NULL.
type hermesRow struct {
	id, parent, cwd, billing       string
	in, out, cacheRead, cacheWrite int64
	started, ended, active         time.Time
}

func (r hermesRow) insert(t *testing.T, db *sql.DB) {
	t.Helper()
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	at := func(v time.Time) any {
		if v.IsZero() {
			return nil
		}
		return unix(v)
	}
	exec(t, db, `INSERT INTO sessions (id, source, system_prompt, title, parent_session_id, started_at, ended_at,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, cwd, billing_provider, last_activity_at)
		VALUES (?, 'cli', ?, ?, ?, ?, ?, ?, ?, ?, ?, 99, ?, ?, ?)`,
		r.id, secret, secret, null(r.parent), at(r.started), at(r.ended),
		r.in, r.out, r.cacheRead, r.cacheWrite, null(r.cwd), null(r.billing), at(r.active))
}

func TestHermesRollsChildIntoParent(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	for _, r := range []hermesRow{
		{id: "p", cwd: "/work/hermes", in: 10, out: 1, cacheRead: 2, cacheWrite: 3, started: now.Add(-time.Hour)},
		{id: "c", parent: "p", cwd: "/work/child", billing: "openrouter", in: 4, out: 5, cacheRead: 1, cacheWrite: 1, started: now.Add(-50 * time.Minute), active: now.Add(-30 * time.Minute)},
		{id: "old", cwd: "/work/old", in: 100, out: 100, started: now.AddDate(0, 0, -200)},
		{id: "orphan", parent: "old", cwd: "/work/orphan", in: 8, started: now.Add(-time.Hour)},
		{id: "u", in: 7},
		{id: "empty", cwd: "/work/empty", started: now},
	} {
		r.insert(t, db)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, ".env"), "SECRET=1")
	deny(t, filepath.Join(home, ".env"))
	before := tree(t, home)

	res := mustRead(t, "hermes", home, since)
	sameTree(t, before, tree(t, home))
	if res.Malformed != 0 || res.Unreadable != 0 {
		t.Fatalf("malformed %d unreadable %d", res.Malformed, res.Unreadable)
	}
	if got := strings.Join(ids(res), ","); got != "orphan,p,u" {
		t.Fatalf("sessions = %s", got)
	}
	p := byID(t, res, "p")
	if want := (Tokens{Input: 14, Output: 6, CacheRead: 3, CacheWrite: 4}); p.Tokens != want {
		t.Fatalf("p tokens = %+v, want %+v", p.Tokens, want)
	}
	// The parent named no billing account, so the child's is the session's.
	if p.Project != "/work/hermes" || p.Account != "openrouter" || p.ParentID != "" {
		t.Fatalf("p = %+v", p)
	}
	if !p.Updated.Equal(now.Add(-30 * time.Minute)) {
		t.Fatalf("p updated = %v", p.Updated)
	}
	// The parent is older than the window, so the child stands alone.
	if o := byID(t, res, "orphan"); o.Tokens != (Tokens{Input: 8}) || o.Project != "/work/orphan" {
		t.Fatalf("orphan = %+v", o)
	}
	// A row with no time is kept: dropping it would hide usage. With no
	// directory either, its project is the Hermes home it came from.
	if u := byID(t, res, "u"); u.Tokens != (Tokens{Input: 7}) || u.Project != home || !u.Updated.IsZero() {
		t.Fatalf("u = %+v", u)
	}
	noLeak(t, res)
}

func TestHermesAccountThroughRollup(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	for _, r := range []hermesRow{
		{id: "own", billing: " anthropic ", in: 1, started: now},
		{id: "own-kid", parent: "own", billing: "openrouter", in: 1, started: now},
		{id: "deep", in: 1, started: now},
		{id: "deep-kid", parent: "deep", in: 1, started: now},
		{id: "deep-grandkid", parent: "deep-kid", billing: "nous", in: 1, started: now},
	} {
		r.insert(t, db)
	}
	db.Close()

	res := mustRead(t, "hermes", home, since)
	for _, want := range []Session{
		// A parent that names its account keeps it.
		{ID: "own", Account: "anthropic", Tokens: Tokens{Input: 2}},
		// One that does not takes the account a sub-agent names.
		{ID: "deep", Account: "nous", Tokens: Tokens{Input: 3}},
	} {
		if got := byID(t, res, want.ID); got.Account != want.Account || got.Tokens != want.Tokens {
			t.Errorf("%s = %+v, want account %q and %+v", want.ID, got, want.Account, want.Tokens)
		}
	}
}

func TestHermesActivityTime(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	quarter := 250 * time.Millisecond
	for _, r := range []hermesRow{
		// Started before the window but still active in it.
		{id: "long", in: 1, started: now.AddDate(0, 0, -200), active: now.Add(-time.Hour + quarter)},
		{id: "ended", in: 1, started: now.AddDate(0, 0, -200), ended: now.Add(-2 * time.Hour)},
		{id: "both", in: 1, started: now.AddDate(0, 0, -200), ended: now.Add(-3 * time.Hour), active: now.Add(-4 * time.Hour)},
		{id: "gone", in: 1, started: now.AddDate(0, 0, -200), ended: now.AddDate(0, 0, -100), active: now.AddDate(0, 0, -100)},
		{id: "edge", in: 1, started: since},
	} {
		r.insert(t, db)
	}
	db.Close()

	res := mustRead(t, "hermes", home, since)
	want := map[string]time.Time{
		"long":  now.Add(-time.Hour + quarter),
		"ended": now.Add(-2 * time.Hour),
		"both":  now.Add(-3 * time.Hour),
		"edge":  since,
	}
	if got := strings.Join(ids(res), ","); got != "both,edge,ended,long" {
		t.Fatalf("sessions = %s", got)
	}
	for id, at := range want {
		if got := byID(t, res, id).Updated; !got.Equal(at) {
			t.Errorf("%s updated = %v, want %v", id, got, at)
		}
	}
	// Without a window every row counts.
	if res := mustRead(t, "hermes", home, time.Time{}); len(res.Sessions) != 5 {
		t.Fatalf("sessions without window = %v", ids(res))
	}
}

// Hermes writes REAL seconds, but a row can hold an ISO string, as one
// ended_at in a real database did. It dates the session like a number, and
// one odd row does not stop the home from being read.
func TestHermesTimesAsText(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	exec(t, db, hermesUsageTable)
	hermesRow{id: "text", in: 5, started: now.AddDate(0, 0, -200)}.insert(t, db)
	exec(t, db, `UPDATE sessions SET ended_at = ? WHERE id = 'text'`, now.Add(-2*time.Hour).Format("2006-01-02T15:04:05.000000-07:00"))
	hermesRow{id: "usage", in: 1, started: now.AddDate(0, 0, -200)}.insert(t, db)
	exec(t, db, `INSERT INTO session_model_usage (session_id, model, billing_provider, input_tokens, first_seen, last_seen) VALUES ('usage', 'm', 'x', 1, 0, 0), ('usage', 'm2', 'x', 0, 0, ?)`, now.Add(-time.Hour).Format(time.RFC3339Nano))
	hermesRow{id: "junk", in: 2, started: now.Add(-3 * time.Hour)}.insert(t, db)
	exec(t, db, `UPDATE sessions SET last_activity_at = 'soon' WHERE id = 'junk'`)
	db.Close()

	res := mustRead(t, "hermes", home, since)
	for id, at := range map[string]time.Time{
		"text":  now.Add(-2 * time.Hour),
		"usage": now.Add(-time.Hour),
		"junk":  now.Add(-3 * time.Hour),
	} {
		if got := byID(t, res, id).Updated; !got.Equal(at) {
			t.Errorf("%s updated = %v, want %v", id, got, at)
		}
	}
}

// Hermes releases before billing and activity tracking had fewer columns.
func TestHermesOlderSchemas(t *testing.T) {
	for _, tc := range []struct {
		name, columns, insert string
		want                  Tokens
	}{
		{
			name:    "tokens only",
			columns: `id TEXT PRIMARY KEY, input_tokens INTEGER, output_tokens INTEGER`,
			insert:  `INSERT INTO sessions VALUES ('a', 3, 2)`,
			want:    Tokens{Input: 3, Output: 2},
		},
		{
			name:    "before billing",
			columns: `id TEXT PRIMARY KEY, parent_session_id TEXT, cwd TEXT, input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER, cache_write_tokens INTEGER, started_at REAL`,
			insert:  `INSERT INTO sessions VALUES ('a', NULL, '/work', 3, 2, 5, 1, NULL), ('k', 'a', '/work', 1, 1, NULL, NULL, NULL)`,
			want:    Tokens{Input: 4, Output: 3, CacheRead: 5, CacheWrite: 1},
		},
		{
			name:    "null counts",
			columns: `id TEXT PRIMARY KEY, input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER`,
			insert:  `INSERT INTO sessions VALUES ('a', NULL, 2, NULL)`,
			want:    Tokens{Output: 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			db := hermesDB(t, home, tc.columns)
			exec(t, db, tc.insert)
			db.Close()
			res := mustRead(t, "hermes", home, since)
			if len(res.Sessions) != 1 || res.Sessions[0].ID != "a" || res.Sessions[0].Tokens != tc.want {
				t.Fatalf("sessions = %+v, want a with %+v", res.Sessions, tc.want)
			}
		})
	}
}

func TestHermesRowsWithoutID(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, `id TEXT, input_tokens INTEGER, output_tokens INTEGER`)
	exec(t, db, `INSERT INTO sessions VALUES (NULL, 5, 5), ('', 5, 5), ('ok', 1, 1)`)
	db.Close()
	res := mustRead(t, "hermes", home, since)
	if res.Malformed != 2 || len(res.Sessions) != 1 || res.Sessions[0].ID != "ok" {
		t.Fatalf("res = %+v", res)
	}
}

func TestHermesUnusableDatabase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, home string)
	}{
		{"no sessions table", func(t *testing.T, home string) {
			db := hermesDB(t, home, `id TEXT, input_tokens INTEGER, output_tokens INTEGER`)
			exec(t, db, `DROP TABLE sessions`)
			db.Close()
		}},
		{"no token columns", func(t *testing.T, home string) {
			db := hermesDB(t, home, `id TEXT, title TEXT`)
			exec(t, db, `INSERT INTO sessions VALUES ('a', 'x')`)
			db.Close()
		}},
		{"not a database", func(t *testing.T, home string) {
			mustWrite(t, filepath.Join(home, "state.db"), strings.Repeat("not sqlite ", 1000))
		}},
		{"truncated database", func(t *testing.T, home string) {
			db := hermesDB(t, home, hermesColumns)
			for i := 0; i < 200; i++ {
				hermesRow{id: strings.Repeat("x", 100) + string(rune('a'+i%26)) + strings.Repeat("y", i), in: 1}.insert(t, db)
			}
			db.Close()
			path := filepath.Join(home, "state.db")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, info.Size()/2); err != nil {
				t.Fatal(err)
			}
		}},
		{"state.db is a directory", func(t *testing.T, home string) {
			if err := os.Mkdir(filepath.Join(home, "state.db"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			tc.setup(t, home)
			before := tree(t, home)
			if _, err := Read("hermes", home, since); err == nil {
				t.Fatal("no error")
			}
			sameTree(t, before, tree(t, home))
		})
	}
}

func TestHermesMissingDatabase(t *testing.T) {
	res, err := Read("hermes", t.TempDir(), since)
	if err != nil || len(res.Sessions) != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestHermesUnreadableDatabase(t *testing.T) {
	needDeny(t)
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	db.Close()
	deny(t, filepath.Join(home, "state.db"))
	if _, err := Read("hermes", home, since); err == nil {
		t.Fatal("no error")
	}
}

// Hermes keeps state.db in WAL mode. Rows a running Hermes has not
// checkpointed live only in state.db-wal, and must still count.
func TestHermesReadsUncheckpointedWAL(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	defer db.Close()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode %q: %v", mode, err)
	}
	exec(t, db, `PRAGMA wal_autocheckpoint=0`)
	hermesRow{id: "live", cwd: "/work/live", in: 6, out: 2, started: now}.insert(t, db)
	if info, err := os.Stat(filepath.Join(home, "state.db-wal")); err != nil || info.Size() == 0 {
		t.Fatalf("no wal to read: %v", err)
	}
	before := tree(t, home)

	res := mustRead(t, "hermes", home, since)
	// A reader takes its read lock in the -shm, as every SQLite reader of a
	// live database does. Nothing else changes, and nothing is created.
	after := tree(t, home)
	if _, ok := after["state.db-shm"]; !ok {
		t.Fatal("state.db-shm is gone")
	}
	delete(before, "state.db-shm")
	delete(after, "state.db-shm")
	sameTree(t, before, after)
	if got := byID(t, res, "live"); got.Tokens != (Tokens{Input: 6, Output: 2}) {
		t.Fatalf("live = %+v", got)
	}
}

// A database Hermes has open is read in place, one nobody has open is read
// as immutable, and only an odd leftover, such as a -wal without its -shm,
// is copied. Hermes databases on a server run to gigabytes.
func TestHermesReadsInPlace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"open", []string{"-wal", "-shm"}, "live"},
		{"closed", nil, "immutable"},
		{"wal only", []string{"-wal"}, "copy"},
		{"shm only", []string{"-shm"}, "copy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			db := filepath.Join(home, "state.db")
			for _, f := range append([]string{""}, tc.files...) {
				if err := os.WriteFile(db+f, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			uri, live, cleanup, err := hermesURI(db)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			got := "copy"
			switch uri {
			case sqliteURI(db):
				got = "live"
			case sqliteURI(db) + "&immutable=1":
				got = "immutable"
			}
			if live != (got == "live") {
				t.Fatalf("%s read reported live=%v", got, live)
			}
			if got != tc.want {
				t.Fatalf("read %s (%s), want %s", got, uri, tc.want)
			}
		})
	}
}

// liveHermesDB is <dir>/state.db held open in WAL mode, with row live only
// in its -wal, as a running Hermes keeps it. The caller closes it.
func liveHermesDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db := hermesDB(t, dir, hermesColumns)
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
		db.Close()
		t.Fatalf("journal mode %q: %v", mode, err)
	}
	exec(t, db, `PRAGMA wal_autocheckpoint=0`)
	hermesRow{id: "live", in: 6, out: 2, started: now}.insert(t, db)
	return db
}

// SQLite keeps the -wal and -shm of a linked state.db beside the file the
// link points to. They must be found there, or the live database is read
// as immutable and its uncheckpointed rows are lost.
func TestHermesReadsLinkedDatabaseLive(t *testing.T) {
	real := t.TempDir()
	db := liveHermesDB(t, real)
	defer db.Close()
	home := t.TempDir()
	if err := os.Symlink(filepath.Join(real, "state.db"), filepath.Join(home, "state.db")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	before := tree(t, home)

	res := mustRead(t, "hermes", home, since)
	sameTree(t, before, tree(t, home))
	if got := byID(t, res, "live"); got.Tokens != (Tokens{Input: 6, Output: 2}) {
		t.Fatalf("live = %+v", got)
	}
}

// A database nobody had open is read as immutable. If Hermes opens it during
// that read, the pages read can mix two states, so the read is done again.
func TestHermesRereadsADatabaseOpenedDuringTheRead(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	exec(t, db, `PRAGMA journal_mode=WAL`)
	hermesRow{id: "old", in: 1, started: now}.insert(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reads := 0
	var writer *sql.DB
	t.Cleanup(func() {
		afterHermesRead = func() {}
		if writer != nil {
			writer.Close()
		}
	})
	afterHermesRead = func() {
		reads++
		if writer != nil {
			return
		}
		var err error
		if writer, err = sql.Open("sqlite", filepath.Join(home, "state.db")); err != nil {
			t.Fatal(err)
		}
		writer.SetMaxOpenConns(1)
		exec(t, writer, `PRAGMA wal_autocheckpoint=0`)
		hermesRow{id: "new", in: 5, started: now}.insert(t, writer)
	}

	res := mustRead(t, "hermes", home, since)
	if reads != 2 {
		t.Fatalf("read %d times, want 2", reads)
	}
	if got := byID(t, res, "new"); got.Tokens != (Tokens{Input: 5}) {
		t.Fatalf("new = %+v", got)
	}

	// Read in place while Hermes has it open, one read is one snapshot.
	reads = 0
	mustRead(t, "hermes", home, since)
	if reads != 1 {
		t.Fatalf("live read %d times, want 1", reads)
	}
}

// A commit between the usage and sessions queries must not be half seen: a
// session row grown past its usage rows would credit the difference to the
// row's own billing provider, counting it twice once both are read.
func TestHermesReadsOneCommit(t *testing.T) {
	home := t.TempDir()
	db := liveHermesDB(t, home)
	defer db.Close()
	exec(t, db, hermesUsageTable)
	hermesRow{id: "s", billing: "openai-codex", in: 10, started: now}.insert(t, db)
	exec(t, db, `INSERT INTO session_model_usage (session_id, model, billing_provider, input_tokens) VALUES ('s', 'grok', 'xai-oauth', 10)`)
	t.Cleanup(func() { betweenHermesQueries = func() {} })
	betweenHermesQueries = func() {
		exec(t, db, `BEGIN`)
		exec(t, db, `UPDATE sessions SET input_tokens = 30 WHERE id = 's'`)
		exec(t, db, `UPDATE session_model_usage SET input_tokens = 30 WHERE session_id = 's'`)
		exec(t, db, `COMMIT`)
		betweenHermesQueries = func() {}
	}

	s := byID(t, mustRead(t, "hermes", home, since), "s")
	if want := map[string]Tokens{"xai-oauth": {Input: 10}}; !reflect.DeepEqual(s.Parts, want) {
		t.Fatalf("parts = %+v, want %+v", s.Parts, want)
	}
}

// A database that changes under every read is read a bounded number of
// times, and the last read stands.
func TestHermesStopsRereadingAChangingDatabase(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	hermesRow{id: "a", in: 1, started: now}.insert(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reads := 0
	t.Cleanup(func() { afterHermesRead = func() {} })
	afterHermesRead = func() {
		reads++
		at := now.Add(time.Duration(reads) * time.Minute)
		if err := os.Chtimes(filepath.Join(home, "state.db"), at, at); err != nil {
			t.Fatal(err)
		}
	}

	res := mustRead(t, "hermes", home, since)
	if reads != hermesAttempts {
		t.Fatalf("read %d times, want %d", reads, hermesAttempts)
	}
	if got := byID(t, res, "a"); got.Tokens != (Tokens{Input: 1}) {
		t.Fatalf("a = %+v", got)
	}
}

// A clean Hermes exit checkpoints and removes the -wal and -shm files, but
// the database stays in WAL mode. Reading it must not recreate them.
func TestHermesWALModeWithoutWALFile(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	exec(t, db, `PRAGMA journal_mode=WAL`)
	hermesRow{id: "done", in: 3, out: 1, started: now}.insert(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(filepath.Join(home, "state.db"+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("state.db%s left behind by the writer: %v", suffix, err)
		}
	}
	before := tree(t, home)

	res := mustRead(t, "hermes", home, since)
	sameTree(t, before, tree(t, home))
	if got := byID(t, res, "done"); got.Tokens != (Tokens{Input: 3, Output: 1}) {
		t.Fatalf("done = %+v", got)
	}
}

// The snapshot copy is private to one read and removed after it.
func TestHermesRemovesItsCopy(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	hermesRow{id: "a", in: 1}.insert(t, db)
	db.Close()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	mustRead(t, "hermes", home, since)
	left, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("left behind in temp: %v", left)
	}
}

func TestSQLiteURI(t *testing.T) {
	got := sqliteURI(filepath.Join(t.TempDir(), "a b#c?.db"))
	if !strings.HasPrefix(got, "file:///") || !strings.HasSuffix(got, "/a%20b%23c%3F.db?mode=ro") {
		t.Fatalf("uri = %q", got)
	}
}

// URI characters in the home path must not cut the database path short.
func TestHermesHomeWithURICharacters(t *testing.T) {
	home := filepath.Join(t.TempDir(), "my home #1 %41")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	db := hermesDB(t, home, hermesColumns)
	hermesRow{id: "a", in: 1}.insert(t, db)
	db.Close()
	if got := total(mustRead(t, "hermes", home, since)); got != (Tokens{Input: 1}) {
		t.Fatalf("total = %+v", got)
	}
}

// hermesUsageTable is Hermes' session_model_usage: the tokens of each
// session's model calls by model, billing route, and task. Rows with a task
// are auxiliary calls no sessions row counts.
const hermesUsageTable = `CREATE TABLE session_model_usage (
	session_id TEXT NOT NULL,
	model TEXT NOT NULL,
	billing_provider TEXT NOT NULL DEFAULT '',
	billing_base_url TEXT NOT NULL DEFAULT '',
	billing_mode TEXT NOT NULL DEFAULT '',
	task TEXT NOT NULL DEFAULT '',
	api_call_count INTEGER NOT NULL DEFAULT 0,
	input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens INTEGER NOT NULL DEFAULT 0,
	cache_write_tokens INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	estimated_cost_usd REAL NOT NULL DEFAULT 0,
	actual_cost_usd REAL NOT NULL DEFAULT 0,
	cost_status TEXT,
	cost_source TEXT,
	first_seen REAL,
	last_seen REAL,
	PRIMARY KEY (session_id, model, billing_provider, billing_base_url, billing_mode, task)
)`

// hermesUse is one session_model_usage row.
type hermesUse struct {
	session, model, provider, mode, task string
	in, out, cacheRead, cacheWrite       int64
	last                                 time.Time
}

func (u hermesUse) insert(t *testing.T, db *sql.DB) {
	t.Helper()
	exec(t, db, `INSERT INTO session_model_usage (session_id, model, billing_provider, billing_mode, task,
		api_call_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
		first_seen, last_seen) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, 9, ?, ?)`,
		u.session, u.model, u.provider, u.mode, u.task, u.in, u.out, u.cacheRead, u.cacheWrite,
		unix(now.Add(-2*time.Hour)), unix(u.last))
}

// hermesWorkDB is a Hermes profile's day: a Codex-subscription session that
// switched to SuperGrok mid-way, with a sub-agent, a compression continuation
// from before usage rows, and auxiliary calls on three routes; an Anthropic
// session; and a background review that created its own empty session row.
func hermesWorkDB(t *testing.T, home string) {
	t.Helper()
	db := hermesDB(t, home, hermesColumns)
	exec(t, db, hermesUsageTable)
	start := now.Add(-3 * time.Hour)
	for _, r := range []hermesRow{
		{id: "main", cwd: "/work/agent", billing: "openai-codex", in: 1000, out: 100, cacheRead: 5000, started: start, ended: now.Add(-time.Hour)},
		{id: "sub", parent: "main", cwd: "/work/agent", billing: "xai-oauth", in: 200, out: 20, started: start.Add(time.Minute), ended: start.Add(30 * time.Minute)},
		{id: "cont", parent: "main", cwd: "/work/agent", billing: "openai-codex", in: 100, out: 10, started: now.Add(-time.Hour)},
		{id: "claude", cwd: "/work/other", billing: "anthropic", in: 70, out: 7, cacheRead: 700, cacheWrite: 70, started: start},
		{id: "review", started: now.Add(-10 * time.Minute)},
	} {
		r.insert(t, db)
	}
	for _, u := range []hermesUse{
		{session: "main", model: "gpt-5.5", provider: "openai-codex", mode: "subscription_included", in: 600, out: 60, cacheRead: 3000, last: now.Add(-2 * time.Hour)},
		{session: "main", model: "grok-4", provider: "xai-oauth", mode: "subscription_included", in: 400, out: 40, cacheRead: 2000, last: now.Add(-time.Hour)},
		{session: "main", model: "gemini-flash", provider: "openrouter", task: "title_generation", in: 50, out: 5, last: start},
		{session: "main", model: "gemini-flash", provider: "openrouter", task: "vision", in: 20, out: 2, last: start},
		// The last compression ran after the session ended: it moves the
		// session's activity.
		{session: "main", model: "gpt-5.5-mini", provider: "openai-codex", task: "compression", in: 300, out: 30, last: now.Add(-20 * time.Minute)},
		{session: "sub", model: "grok-4", provider: "xai-oauth", mode: "subscription_included", in: 200, out: 20, last: start.Add(30 * time.Minute)},
		{session: "claude", model: "claude-sonnet", provider: "anthropic", mode: "official_docs_snapshot", in: 70, out: 7, cacheRead: 700, cacheWrite: 70, last: start},
		{session: "review", model: "gpt-5.5", provider: "openai-codex", task: "background_review", in: 40, out: 4, last: now.Add(-5 * time.Minute)},
	} {
		u.insert(t, db)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHermesSplitsUsageByBillingProvider(t *testing.T) {
	home := t.TempDir()
	hermesWorkDB(t, home)
	before := tree(t, home)
	res := mustRead(t, "hermes", home, since)
	sameTree(t, before, tree(t, home))
	if got := strings.Join(ids(res), ","); got != "claude,main,review" {
		t.Fatalf("sessions = %s", got)
	}
	main := byID(t, res, "main")
	wantMain := map[string]Tokens{
		// Main loop, compression, and the continuation, which has no usage
		// rows and so is its sessions row's.
		"openai-codex": {Input: 600 + 300 + 100, Output: 60 + 30 + 10, CacheRead: 3000},
		// The route switch and the sub-agent.
		"xai-oauth":  {Input: 400 + 200, Output: 40 + 20, CacheRead: 2000},
		"openrouter": {Input: 70, Output: 7},
	}
	if !reflect.DeepEqual(main.Parts, wantMain) {
		t.Fatalf("main parts = %+v, want %+v", main.Parts, wantMain)
	}
	if want := (Tokens{Input: 1670, Output: 167, CacheRead: 5000}); main.Tokens != want {
		t.Fatalf("main tokens = %+v, want %+v", main.Tokens, want)
	}
	if main.Account != "openai-codex" || main.Project != "/work/agent" || !main.Updated.Equal(now.Add(-20*time.Minute)) {
		t.Fatalf("main = %+v", main)
	}
	if c := byID(t, res, "claude"); !reflect.DeepEqual(c.Parts, map[string]Tokens{"anthropic": {Input: 70, Output: 7, CacheRead: 700, CacheWrite: 70}}) {
		t.Fatalf("claude = %+v", c)
	}
	// A session with only auxiliary usage still counts, under the route it
	// billed, and its last call dates it.
	r := byID(t, res, "review")
	if !reflect.DeepEqual(r.Parts, map[string]Tokens{"openai-codex": {Input: 40, Output: 4}}) || !r.Updated.Equal(now.Add(-5*time.Minute)) {
		t.Fatalf("review = %+v", r)
	}
	noLeak(t, res)
}

// Profiles are homes of their own, each with its own state.db.
func TestHermesProfilesReadTogether(t *testing.T) {
	root := t.TempDir()
	def := filepath.Join(root, ".hermes")
	work := filepath.Join(def, "profiles", "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	hermesWorkDB(t, work)
	db := hermesDB(t, def, hermesColumns)
	exec(t, db, hermesUsageTable)
	hermesRow{id: "home", cwd: "/work/home", billing: "openrouter", in: 5, out: 1, started: now}.insert(t, db)
	hermesUse{session: "home", model: "kimi", provider: "openrouter", in: 5, out: 1, last: now}.insert(t, db)
	db.Close()

	res := ReadHomes("hermes", []string{def, work}, since)
	if got := strings.Join(ids(res), ","); got != "claude,home,main,review" {
		t.Fatalf("sessions = %s", got)
	}
	if h := byID(t, res, "home"); h.Home != def || !reflect.DeepEqual(h.Parts, map[string]Tokens{"openrouter": {Input: 5, Output: 1}}) {
		t.Fatalf("home = %+v", h)
	}
	if m := byID(t, res, "main"); m.Home != work {
		t.Fatalf("main home = %s", m.Home)
	}
}

// Before the task column every usage row was a main-loop row, and before
// the table the sessions row was all there was.
func TestHermesUsageOlderSchemas(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	exec(t, db, `CREATE TABLE session_model_usage (session_id TEXT NOT NULL, model TEXT NOT NULL,
		billing_provider TEXT NOT NULL DEFAULT '', input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0)`)
	hermesRow{id: "a", billing: "openai-codex", in: 30, out: 3, cacheRead: 9, started: now}.insert(t, db)
	exec(t, db, `INSERT INTO session_model_usage VALUES ('a', 'gpt', 'openai-codex', 10, 1), ('a', 'grok', 'xai-oauth', 20, 2)`)
	db.Close()
	a := byID(t, mustRead(t, "hermes", home, since), "a")
	want := map[string]Tokens{"openai-codex": {Input: 10, Output: 1, CacheRead: 9}, "xai-oauth": {Input: 20, Output: 2}}
	if !reflect.DeepEqual(a.Parts, want) {
		t.Fatalf("parts = %+v, want %+v", a.Parts, want)
	}

	home = t.TempDir()
	db = hermesDB(t, home, hermesColumns)
	hermesRow{id: "b", billing: "anthropic", in: 4, started: now}.insert(t, db)
	db.Close()
	if b := byID(t, mustRead(t, "hermes", home, since), "b"); !reflect.DeepEqual(b.Parts, map[string]Tokens{"anthropic": {Input: 4}}) {
		t.Fatalf("b = %+v", b)
	}
}

// An auxiliary call on a fallback route records no billing provider, and
// Hermes keeps such a call off the main loop's route: it is not billed to
// the session's provider.
func TestHermesAuxWithoutRouteIsUnknown(t *testing.T) {
	home := t.TempDir()
	db := hermesDB(t, home, hermesColumns)
	exec(t, db, hermesUsageTable)
	hermesRow{id: "s", cwd: "/work/a", billing: "openai-codex", in: 100, out: 10, started: now}.insert(t, db)
	hermesUse{session: "s", model: "gpt-5.5", provider: "openai-codex", in: 100, out: 10, last: now}.insert(t, db)
	hermesUse{session: "s", model: "local-vision", task: "vision", in: 30, out: 3, last: now}.insert(t, db)
	db.Close()
	s := byID(t, mustRead(t, "hermes", home, since), "s")
	want := map[string]Tokens{"openai-codex": {Input: 100, Output: 10}, UnknownAccount: {Input: 30, Output: 3}}
	if !reflect.DeepEqual(s.Parts, want) || s.Tokens != (Tokens{Input: 130, Output: 13}) {
		t.Fatalf("parts = %+v tokens = %+v, want %+v", s.Parts, s.Tokens, want)
	}
}
