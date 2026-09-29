package logs

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// Fictional OpenAI account ids, as Codex's thread table records them.
const (
	annID = "acct-7f3e0c21"
	samID = "acct-19b4d6aa"
)

// codexThread is a row of Codex's threads table. A zero updated or created
// time is NULL.
type codexThread struct {
	id, creator      string
	updated, created int64
}

// codexStateDB writes <home>/<name> with a threads table of the given
// columns and rows, as Codex keeps it.
func writeCodexStateDB(t *testing.T, home, name string, withCreator bool, rows ...codexThread) {
	t.Helper()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, name))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cols := `id TEXT PRIMARY KEY, rollout_path TEXT, created_at INTEGER, updated_at INTEGER, title TEXT, first_user_message TEXT`
	if withCreator {
		cols += `, creator_account_id TEXT`
	}
	exec(t, db, `CREATE TABLE threads (`+cols+`)`)
	null := func(n int64) any {
		if n == 0 {
			return nil
		}
		return n
	}
	for _, r := range rows {
		if withCreator {
			var creator any
			if r.creator != "" {
				creator = r.creator
			}
			exec(t, db, `INSERT INTO threads (id, rollout_path, created_at, updated_at, title, first_user_message, creator_account_id) VALUES (?, '/secret/path', ?, ?, ?, ?, ?)`,
				r.id, null(r.created), null(r.updated), secret, secret, creator)
		} else {
			exec(t, db, `INSERT INTO threads (id, rollout_path, created_at, updated_at, title, first_user_message) VALUES (?, '/secret/path', ?, ?, ?, ?)`,
				r.id, null(r.created), null(r.updated), secret, secret)
		}
	}
}

// Codex's thread table names the account that created a thread. The home's
// account is the one that created its newest thread that names one, by its
// last update, else its creation, else the order rows were written. A session
// carries its root thread's account, else the first of its members'. The
// newest state_N.sqlite is the one read, and it is left as it was.
func TestCodexThreadTableNamesAccounts(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	mustWrite(t, rollout(a, "sessions", "20", rootID), rootLines...)
	mustWrite(t, rollout(a, "sessions", "20", kidA),
		cxMeta{at: "2026-09-20T10:01:00.000Z", id: kidA, session: rootID, cwd: "/work/app", source: spawnedBy(rootID)}.String(),
		cxCount("2026-09-20T10:01:10.000Z", u{300, 100, 20}, u{300, 100, 20}),
	)
	mustWrite(t, rollout(a, "sessions", "21", kidB),
		cxMeta{at: "2026-09-21T10:00:00.000Z", id: kidB, cwd: "/work/other"}.String(),
		cxCount("2026-09-21T10:00:10.000Z", u{50, 0, 5}, u{50, 0, 5}),
	)
	writeCodexStateDB(t, a, "state_4.sqlite", true, codexThread{id: kidB, creator: "acct-00000000", updated: 9e9})
	writeCodexStateDB(t, a, "state_5.sqlite", true,
		codexThread{id: kidA, creator: annID, updated: 1790000100},
		codexThread{id: kidB, creator: "", updated: 1790000900},
		codexThread{id: guardID, creator: samID, created: 1790000500},
		codexThread{id: lostID, creator: "  ", updated: 1790000800},
	)
	if err := os.WriteFile(filepath.Join(a, "state_9.sqlite.bak.before-fix"), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Another home's table names the other session's creator.
	writeCodexStateDB(t, b, "state_5.sqlite", true, codexThread{id: kidB, creator: samID, created: 1790000000})
	before := tree(t, root)

	res := ReadHomes("codex", []string{a, b}, since)
	sameTree(t, before, tree(t, root))
	// guardID was never updated, and was created after kidA's last update.
	if got := res.Homes[a].AccountID; got != samID {
		t.Fatalf("home a account = %q, want %q", got, samID)
	}
	if got := res.Homes[b].AccountID; got != samID {
		t.Fatalf("home b account = %q", got)
	}
	if got := byID(t, res, rootID); got.CreatorAccount != annID || got.Home != a {
		t.Fatalf("root = %+v, want its sub-agent's creator", got)
	}
	if got := byID(t, res, kidB); got.CreatorAccount != samID {
		t.Fatalf("other session creator = %q", got.CreatorAccount)
	}
	noLeak(t, res)
}

// Without the creator column, or the table, or a database at all, nothing
// names an account, and the read goes on as before.
func TestCodexThreadTableWithoutCreatorColumn(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, home string){
		"no column": func(t *testing.T, home string) {
			writeCodexStateDB(t, home, "state_5.sqlite", false, codexThread{id: rootID, updated: 1790000000})
		},
		"no table": func(t *testing.T, home string) {
			db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			exec(t, db, `CREATE TABLE other (id TEXT)`)
		},
		"not a database": func(t *testing.T, home string) {
			mustWrite(t, filepath.Join(home, "state_5.sqlite"), "garbage")
		},
		"none": func(*testing.T, string) {},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			mustWrite(t, rollout(home, "sessions", "20", rootID), rootLines...)
			setup(t, home)
			res := mustRead(t, "codex", home, since)
			if res.AccountID != "" || byID(t, res, rootID).CreatorAccount != "" {
				t.Fatalf("ids = %q, %q", res.AccountID, byID(t, res, rootID).CreatorAccount)
			}
			if res.Unreadable != 0 || res.Malformed != 0 || byID(t, res, rootID).Tokens != rootOwn {
				t.Fatalf("read = %+v", res)
			}
		})
	}
}
