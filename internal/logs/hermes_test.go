package logs

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
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
	// A row with no time is kept: dropping it would hide usage.
	if u := byID(t, res, "u"); u.Tokens != (Tokens{Input: 7}) || u.Project != UnknownProject || !u.Updated.IsZero() {
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
	sameTree(t, before, tree(t, home))
	if got := byID(t, res, "live"); got.Tokens != (Tokens{Input: 6, Output: 2}) {
		t.Fatalf("live = %+v", got)
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
