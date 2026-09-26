package logs

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
			st, err := statDB(db)
			if err != nil {
				t.Fatal(err)
			}
			uri, live, cleanup, err := hermesURI(db, st)
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
	// A -shm without its -wal is a leftover, so the read is from a copy.
	mustWrite(t, filepath.Join(home, "state.db-shm"))
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

// The URI names the whole path, read-only, whatever characters it holds.
func TestSQLiteURI(t *testing.T) {
	got := sqliteURI(filepath.Join(t.TempDir(), "a b#c?.db"))
	u, err := url.Parse(got)
	if err != nil || u.Scheme != "file" || !strings.HasSuffix(u.Path, "/a b#c?.db") || u.Query().Get("mode") != "ro" {
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
