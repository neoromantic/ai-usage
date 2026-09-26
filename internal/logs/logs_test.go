package logs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// fullHomes writes one small home per provider under root and returns them by
// provider. Each home also holds the credential files the readers must skip.
func fullHomes(t *testing.T, root string) map[string]string {
	t.Helper()
	homes := map[string]string{}
	for _, p := range snapshot.Providers {
		homes[p] = filepath.Join(root, p)
	}

	claude := homes["claude"]
	mustWrite(t, filepath.Join(claude, "projects", "-work-claude", "sess.jsonl"),
		cl{id: "m1", req: "r1", session: "sess", cwd: "/work/claude", usage: use(4, 1, 0, 0)}.String())
	mustWrite(t, filepath.Join(claude, "projects", "-work-claude", "sess", "subagents", "agent-1.jsonl"),
		cl{id: "m2", req: "r2", session: "sess", side: true, usage: use(1, 1, 0, 0)}.String())
	mustWrite(t, filepath.Join(claude, ".credentials.json"), "{}")

	codex := homes["codex"]
	mustWrite(t, rollout(codex, "sessions", "20", rootID), rootLines...)
	mustWrite(t, rollout(codex, "archived_sessions", "19", kidA),
		cxMeta{at: "2026-09-19T10:00:00Z", id: kidA, cwd: "/work/archived"}.String(),
		cxCount("2026-09-19T10:00:05Z", u{10, 0, 1}, u{10, 0, 1}))
	mustWrite(t, filepath.Join(codex, "auth.json"), "{}")

	grok := homes["grok"]
	mustWrite(t, filepath.Join(grok, "sessions", "%2Fwork%2Fgrok", "sid", "summary.json"), grokSummary("sid", "/work/grok"))
	mustWrite(t, filepath.Join(grok, "sessions", "%2Fwork%2Fgrok", "sid", "updates.jsonl"), turn("p", [4]int64{3, 1, 1, 0}))
	mustWrite(t, filepath.Join(grok, "auth.json"), "{}")

	hermes := homes["hermes"]
	if err := os.MkdirAll(hermes, 0o755); err != nil {
		t.Fatal(err)
	}
	db := hermesDB(t, hermes, hermesColumns)
	hermesRow{id: "h", cwd: "/work/hermes", in: 2, out: 2, started: now}.insert(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(hermes, ".env"), "KEY=1")
	for _, secretFile := range []string{
		filepath.Join(claude, ".credentials.json"),
		filepath.Join(codex, "auth.json"),
		filepath.Join(grok, "auth.json"),
		filepath.Join(hermes, ".env"),
	} {
		deny(t, secretFile)
	}
	return homes
}

// Readers only read: a harness home looks the same, byte for byte and time
// for time, after every provider has been read.
func TestReadersNeverWriteToTheHome(t *testing.T) {
	root := t.TempDir()
	homes := fullHomes(t, root)
	before := tree(t, root)
	for _, p := range snapshot.Providers {
		res := mustRead(t, p, homes[p], since)
		if len(res.Sessions) == 0 || res.Unreadable != 0 || res.Malformed != 0 {
			t.Errorf("%s: %+v", p, res)
		}
		noLeak(t, res)
	}
	sameTree(t, before, tree(t, root))
}

func TestReadMissingHome(t *testing.T) {
	for _, p := range snapshot.Providers {
		res, err := Read(p, filepath.Join(t.TempDir(), "absent"), since)
		if err != nil || len(res.Sessions) != 0 || res.Limits != nil || res.Unreadable != 0 {
			t.Errorf("%s: res %+v err %v", p, res, err)
		}
	}
}

func TestReadUnknownProvider(t *testing.T) {
	res, err := Read("gemini", t.TempDir(), since)
	if err == nil {
		t.Fatal("no error")
	}
	if len(res.Sessions) != 0 {
		t.Fatalf("sessions = %+v", res.Sessions)
	}
}

// One provider's broken home neither stops nor changes the others.
func TestProviderFailureIsolation(t *testing.T) {
	needDeny(t)
	root := t.TempDir()
	homes := fullHomes(t, root)
	want := map[string][]Session{}
	for _, p := range []string{"claude", "grok"} {
		want[p] = mustRead(t, p, homes[p], since).Sessions
	}

	deny(t, filepath.Join(homes["codex"], "sessions"))
	codex := mustRead(t, "codex", homes["codex"], since)
	if codex.Unreadable == 0 {
		t.Fatal("unreadable sessions directory was not reported")
	}
	// Archived threads are still read.
	if got := strings.Join(ids(codex), ","); got != kidA {
		t.Fatalf("codex sessions = %s", got)
	}

	mustWrite(t, filepath.Join(homes["hermes"], "state.db"), "garbage")
	if _, err := Read("hermes", homes["hermes"], since); err == nil {
		t.Fatal("corrupt hermes database read without error")
	}

	for p, sessions := range want {
		if got := mustRead(t, p, homes[p], since).Sessions; !reflect.DeepEqual(got, sessions) {
			t.Errorf("%s changed: %+v, want %+v", p, got, sessions)
		}
	}
}

// withoutHome drops the homes each session was read from, to compare reads
// of different homes.
func withoutHome(in []Session) []Session {
	out := make([]Session, len(in))
	for i, s := range in {
		s.Home, s.Homes = "", nil
		out[i] = s
	}
	return out
}

// Two homes can share their logs through a symlink. Each reads the same
// session ids with the same tokens, and read together they count once.
func TestHomesSharingLogsReadAlike(t *testing.T) {
	root := t.TempDir()
	homes := fullHomes(t, root)
	for _, tc := range []struct{ provider, dir string }{
		{"claude", "projects"},
		{"codex", "sessions"},
		{"codex", "archived_sessions"},
		{"grok", "sessions"},
	} {
		alias := filepath.Join(root, "alias-"+tc.provider)
		if err := os.MkdirAll(alias, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(homes[tc.provider], tc.dir), filepath.Join(alias, tc.dir)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	for _, p := range []string{"claude", "codex", "grok"} {
		alias := filepath.Join(root, "alias-"+p)
		a := mustRead(t, p, homes[p], since)
		b := mustRead(t, p, alias, since)
		if len(a.Sessions) == 0 || !reflect.DeepEqual(withoutHome(a.Sessions), withoutHome(b.Sessions)) {
			t.Errorf("%s: %+v vs %+v", p, a.Sessions, b.Sessions)
		}
		both := ReadHomes(p, []string{homes[p], alias}, since)
		if !reflect.DeepEqual(withoutHome(both.Sessions), withoutHome(a.Sessions)) {
			t.Errorf("%s read together: %+v, want %+v", p, both.Sessions, a.Sessions)
		}
		for _, h := range []string{homes[p], alias} {
			if hr, ok := both.Homes[h]; !ok || hr.Err != nil {
				t.Errorf("%s: home %s read = %+v, %v", p, h, hr, ok)
			}
		}
	}
}

// A session in one home whose sub-agent, fork, or resumed copy is in another
// home is counted once when the homes are read together, and each session
// names the home it grows in.
func TestReadHomesCountsAcrossHomesOnce(t *testing.T) {
	t.Run("claude sub-agent and fork in another home", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		dir := func(home string) string { return filepath.Join(home, "projects", "-work-claude") }
		orig := []string{
			cl{id: "m1", req: "r1", session: "orig", cwd: "/work/claude", at: "2026-09-20T10:00:00Z", usage: use(10, 1, 0, 0)}.String(),
			cl{id: "m2", req: "r2", session: "orig", cwd: "/work/claude", at: "2026-09-20T10:01:00Z", usage: use(20, 2, 0, 0)}.String(),
		}
		mustWrite(t, filepath.Join(dir(a), "orig.jsonl"), orig...)
		// b holds a sidechain transcript of orig without orig itself, and a
		// fork of orig that copies its messages.
		mustWrite(t, filepath.Join(dir(b), "agent-1.jsonl"),
			cl{id: "s1", req: "rs1", session: "orig", cwd: "/work/claude", side: true, at: "2026-09-20T10:02:00Z", usage: use(3, 3, 0, 0)}.String())
		fork := append(append([]string{}, orig...),
			cl{id: "m3", req: "r3", session: "fork", cwd: "/work/claude", at: "2026-09-21T09:00:00Z", usage: use(40, 4, 0, 0)}.String())
		fork[0] = strings.Replace(fork[0], `"timestamp":"2026-09-20T10:00:00Z"`, `"timestamp":"2026-09-21T08:59:00Z"`, 1)
		mustWrite(t, filepath.Join(dir(b), "fork.jsonl"), fork...)

		res := ReadHomes("claude", []string{a, b}, since)
		if got := strings.Join(ids(res), ","); got != "fork,orig" {
			t.Fatalf("sessions = %s", got)
		}
		if got := byID(t, res, "orig"); got.Tokens != (Tokens{Input: 33, Output: 6}) || got.Home != a {
			t.Fatalf("orig = %+v", got)
		}
		if got := byID(t, res, "fork"); got.Tokens != (Tokens{Input: 40, Output: 4}) || got.Home != b {
			t.Fatalf("fork = %+v", got)
		}
		if got, want := total(res), (Tokens{Input: 73, Output: 10}); got != want {
			t.Fatalf("total = %+v, want %+v", got, want)
		}
	})
	t.Run("claude session resumed in a copy of its home", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		first := cl{id: "m1", req: "r1", session: "s", cwd: "/work/claude", at: "2026-09-20T10:00:00Z", usage: use(10, 1, 0, 0)}.String()
		pa := filepath.Join(a, "projects", "-work-claude", "s.jsonl")
		pb := filepath.Join(b, "projects", "-work-claude", "s.jsonl")
		mustWrite(t, pa, first)
		mustWrite(t, pb, first, cl{id: "m2", req: "r2", session: "s", cwd: "/work/claude", at: "2026-09-21T10:00:00Z", usage: use(5, 1, 0, 0)}.String())
		ageFile(t, pa, now.Add(-2*time.Hour))
		ageFile(t, pb, now.Add(-time.Hour))
		res := ReadHomes("claude", []string{a, b}, since)
		if len(res.Sessions) != 1 || res.Sessions[0].Tokens != (Tokens{Input: 15, Output: 2}) || res.Sessions[0].Home != b {
			t.Fatalf("sessions = %+v", res.Sessions)
		}
	})
	t.Run("codex fork in another home", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		mustWrite(t, rollout(a, "sessions", "20", rootID), rootLines...)
		lines := []string{
			cxMeta{at: "2026-09-21T09:00:00.000Z", id: forkID, cwd: "/work/app", extra: `,"forked_from_id":"` + rootID + `"`}.String(),
			cxMeta{at: "2026-09-20T10:00:00.000Z", id: rootID, cwd: "/work/app"}.String(),
			cxCount("2026-09-21T09:00:00.000Z", e1[0], e1[1]),
			cxCount("2026-09-21T09:00:00.000Z", e2[0], e2[1]),
			cxCount("2026-09-21T09:00:20.000Z", u{4000, 3300, 100}, u{1500, 1400, 20}),
		}
		mustWrite(t, rollout(b, "sessions", "21", forkID), lines...)

		res := ReadHomes("codex", []string{a, b}, since)
		if got := byID(t, res, rootID); got.Tokens != rootOwn || got.Home != a {
			t.Fatalf("parent = %+v", got)
		}
		if got := byID(t, res, forkID); got.Tokens != (Tokens{Input: 100, CacheRead: 1400, Output: 20}) || got.Home != b {
			t.Fatalf("fork = %+v", got)
		}
		// Limits stay with the home whose logs saw them.
		if res.Homes[a].Limits == nil || res.Homes[b].Limits != nil {
			t.Fatalf("limits: a %+v, b %+v", res.Homes[a].Limits, res.Homes[b].Limits)
		}
	})
	t.Run("one broken home does not hide the others", func(t *testing.T) {
		needDeny(t)
		a, b := t.TempDir(), t.TempDir()
		mustWrite(t, rollout(a, "sessions", "20", rootID), rootLines...)
		if err := os.MkdirAll(filepath.Join(b, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(b, "archived_sessions"), "not a directory")
		res := ReadHomes("codex", []string{a, b}, since)
		if res.Homes[a].Err != nil || len(res.Sessions) != 1 || res.Sessions[0].Home != a {
			t.Fatalf("result = %+v", res)
		}
	})
}

// A line too long to read is malformed. The lines on either side still count.
func TestLineTooLongIsSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 32 MiB line")
	}
	home := t.TempDir()
	long := `{"type":"assistant","usage":"` + strings.Repeat("x", maxLineBytes) + `"}`
	mustWrite(t, filepath.Join(home, "projects", "p", "sess.jsonl"),
		cl{id: "m1", req: "r1", session: "sess", cwd: "/work", usage: use(4, 1, 0, 0)}.String(),
		long,
		cl{id: "m2", req: "r2", session: "sess", cwd: "/work", usage: use(6, 2, 0, 0)}.String())
	res := mustRead(t, "claude", home, since)
	if res.Malformed != 1 || res.Unreadable != 0 {
		t.Fatalf("malformed %d unreadable %d", res.Malformed, res.Unreadable)
	}
	if got := total(res); got != (Tokens{Input: 10, Output: 3}) {
		t.Fatalf("total = %+v", got)
	}
}

func TestRollup(t *testing.T) {
	t1 := now.Add(-3 * time.Hour)
	t2 := now.Add(-2 * time.Hour)
	t3 := now.Add(-time.Hour)
	tok := func(n int64) Tokens { return Tokens{Input: n} }
	cases := []struct {
		name string
		in   []Session
		want []Session
	}{
		{
			name: "chain rolls into its root",
			in: []Session{
				{ID: "a", Project: "/a", Tokens: tok(1), Updated: t1},
				{ID: "b", ParentID: "a", Project: "/b", Tokens: tok(2), Updated: t3, Account: "acct"},
				{ID: "c", ParentID: "b", Project: "/c", Tokens: tok(4), Updated: t2},
			},
			want: []Session{{ID: "a", Project: "/a", Tokens: tok(7), Updated: t3, Account: "acct"}},
		},
		{
			name: "child listed before its parent",
			in: []Session{
				{ID: "c", ParentID: "a", Tokens: tok(4)},
				{ID: "a", Project: "/a", Tokens: tok(1)},
			},
			want: []Session{{ID: "a", Project: "/a", Tokens: tok(5)}},
		},
		{
			name: "root without project takes its child's",
			in: []Session{
				{ID: "a", Tokens: tok(1)},
				{ID: "b", ParentID: "a", Project: "/b", Tokens: tok(1)},
			},
			want: []Session{{ID: "a", Project: "/b", Tokens: tok(2)}},
		},
		{
			name: "empty root keeps its children's tokens",
			in: []Session{
				{ID: "a", Project: "/a"},
				{ID: "b", ParentID: "a", Tokens: tok(3)},
				{ID: "z", Project: "/z"},
			},
			want: []Session{{ID: "a", Project: "/a", Tokens: tok(3)}},
		},
		{
			name: "self parent",
			in:   []Session{{ID: "a", ParentID: "a", Project: "/a", Tokens: tok(1)}},
			want: []Session{{ID: "a", Project: "/a", Tokens: tok(1)}},
		},
		{
			name: "sessions without id stay apart",
			in: []Session{
				{Project: "/x", Tokens: tok(1)},
				{Project: "/y", Tokens: tok(2)},
				{ID: "b", ParentID: "", Tokens: tok(4), Project: "/b"},
			},
			want: []Session{
				{Project: "/x", Tokens: tok(1)},
				{Project: "/y", Tokens: tok(2)},
				{ID: "b", Project: "/b", Tokens: tok(4)},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rollup(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("rollup = %+v\nwant     %+v", got, c.want)
			}
		})
	}
}

// Broken parent links must end, and must not lose or double tokens.
func TestRollupCycles(t *testing.T) {
	for _, in := range [][]Session{
		{{ID: "a", ParentID: "b", Tokens: Tokens{Input: 1}}, {ID: "b", ParentID: "a", Tokens: Tokens{Input: 2}}},
		{{ID: "a", ParentID: "c", Tokens: Tokens{Input: 1}}, {ID: "b", ParentID: "a", Tokens: Tokens{Input: 2}}, {ID: "c", ParentID: "b", Tokens: Tokens{Input: 4}}},
		{{ID: "k", ParentID: "a", Tokens: Tokens{Input: 8}}, {ID: "a", ParentID: "b", Tokens: Tokens{Input: 1}}, {ID: "b", ParentID: "a", Tokens: Tokens{Input: 2}}},
	} {
		var want Tokens
		for _, s := range in {
			want = want.Add(s.Tokens)
		}
		done := make(chan []Session)
		go func() { done <- rollup(in) }()
		select {
		case out := <-done:
			var got Tokens
			for _, s := range out {
				got = got.Add(s.Tokens)
				if s.ParentID != "" {
					t.Errorf("%s kept parent %s", s.ID, s.ParentID)
				}
			}
			if got != want {
				t.Errorf("total = %+v, want %+v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("rollup(%+v) did not return", in)
		}
	}
}

func TestDedupeSessions(t *testing.T) {
	t1 := now.Add(-2 * time.Hour)
	t2 := now.Add(-time.Hour)
	cases := []struct {
		name string
		in   []Session
		want []Session
	}{
		{
			name: "larger copy wins and the smaller fills gaps",
			in: []Session{
				{ID: "a", ParentID: "p", Project: "/a", Tokens: Tokens{Input: 5}, Updated: t2, Account: "acct"},
				{ID: "b", Tokens: Tokens{Input: 1}},
				{ID: "a", Tokens: Tokens{Input: 9}, Updated: t1},
			},
			want: []Session{
				{ID: "a", ParentID: "p", Project: "/a", Tokens: Tokens{Input: 9}, Updated: t2, Account: "acct"},
				{ID: "b", Tokens: Tokens{Input: 1}},
			},
		},
		{
			name: "larger copy keeps its own fields",
			in: []Session{
				{ID: "a", Project: "/old", Tokens: Tokens{Input: 1}, Updated: t2},
				{ID: "a", Project: "/new", Tokens: Tokens{Output: 2}, Updated: t1},
			},
			want: []Session{{ID: "a", Project: "/new", Tokens: Tokens{Output: 2}, Updated: t2}},
		},
		{
			name: "equal copies keep the first",
			in: []Session{
				{ID: "a", Project: "/first", Tokens: Tokens{Input: 1}},
				{ID: "a", Project: "/second", Tokens: Tokens{Output: 1}},
			},
			want: []Session{{ID: "a", Project: "/first", Tokens: Tokens{Input: 1}}},
		},
		{
			name: "no id never merges",
			in:   []Session{{Tokens: Tokens{Input: 1}}, {Tokens: Tokens{Input: 1}}},
			want: []Session{{Tokens: Tokens{Input: 1}}, {Tokens: Tokens{Input: 1}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dedupeSessions(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("dedupe = %+v\nwant      %+v", got, c.want)
			}
		})
	}
}

// The readers' own tests cover credentials.jsonl and the log names they read.
func TestDeniedFile(t *testing.T) {
	for name, want := range map[string]bool{
		"AUTH.JSON":            true,
		".credentials.json":    true,
		"gcloud-credential.db": true,
		".env":                 true,
		".env.local":           true,
		"cookies":              true,
		"Cookies.sqlite":       true,
		"environment.jsonl":    false,
	} {
		if got := deniedFile(name); got != want {
			t.Errorf("deniedFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestForEachReader(t *testing.T) {
	read := func(in string) (string, int, error) {
		var got []string
		long, err := forEachReader(strings.NewReader(in), func(line []byte) {
			got = append(got, string(line))
		})
		return strings.Join(got, "|"), long, err
	}
	if got, long, err := read("a\r\n\n  \n b \nlast"); got != "a|b|last" || long != 0 || err != nil {
		t.Fatalf("lines %q long %d err %v", got, long, err)
	}
	// A line longer than the internal buffer is read whole.
	wide := strings.Repeat("w", 200<<10)
	if got, long, err := read("a\n" + wide + "\nb"); got != "a|"+wide+"|b" || long != 0 || err != nil {
		t.Fatalf("wide line: long %d err %v", long, err)
	}
	if testing.Short() {
		return
	}
	// A line too long to keep is counted even when it ends the input.
	if got, long, err := read("a\n" + strings.Repeat("x", maxLineBytes+1)); got != "a" || long != 1 || err != nil {
		t.Fatalf("lines %q long %d err %v", got, long, err)
	}
}

// A read error ends the file and is returned, so the file counts as unreadable.
func TestForEachReaderReadError(t *testing.T) {
	broken := io.MultiReader(strings.NewReader("a\nb"), iotest.ErrReader(errors.New("input/output error")))
	var got []string
	_, err := forEachReader(broken, func(line []byte) { got = append(got, string(line)) })
	if err == nil || strings.Join(got, "|") != "a|b" {
		t.Fatalf("lines %q err %v", got, err)
	}
}
