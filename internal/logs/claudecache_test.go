package logs

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sizes(t *testing.T, paths ...string) int64 {
	t.Helper()
	var n int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		n += info.Size()
	}
	return n
}

// readCached reads homes with the cache in dir. It returns the read and how
// many bytes of logs it parsed.
func readCached(dir string, homes ...string) (Result, int64) {
	before := claudeParsed.Load()
	res := Reader{CacheDir: dir}.ReadHomes("claude", homes, since)
	return res, claudeParsed.Load() - before
}

// sameRead fails unless a read with the cache is the read without it.
func sameRead(t *testing.T, got Result, homes ...string) {
	t.Helper()
	want := ReadHomes("claude", homes, since)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cached read =\n%+v\nwant\n%+v", got, want)
	}
}

func cachedFiles(t *testing.T, dir string) map[string]claudeEntry {
	t.Helper()
	c := loadClaudeCache(dir)
	if c.prev == nil {
		t.Fatal("no cache")
	}
	return c.prev
}

func TestClaudeCacheUnchanged(t *testing.T) {
	c := newClaudeCorpus(t)
	dir := filepath.Join(t.TempDir(), "cache")
	// An older version's cache goes once this version's is written.
	older := filepath.Join(dir, fmt.Sprintf("claude-v%d.gob", claudeCacheVersion-1))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, older, "stale")
	res, parsed := readCached(dir, c.home)
	if _, err := os.Stat(older); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the older cache is still there: %v", err)
	}
	sameRead(t, res, c.home)
	if want := sizes(t, c.main, c.sub, c.copy); parsed != want {
		t.Fatalf("first read parsed %d bytes, want %d", parsed, want)
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{dir: os.ModeDir | 0o700, filepath.Join(dir, claudeCacheName): 0o600} {
			if info, err := os.Stat(path); err != nil || info.Mode() != mode {
				t.Fatalf("%s: %v, %v", path, info.Mode(), err)
			}
		}
	}
	for range 2 {
		res, parsed = readCached(dir, c.home)
		sameRead(t, res, c.home)
		if parsed != 0 {
			t.Fatalf("an unchanged read parsed %d bytes", parsed)
		}
	}
	// The corpus has what the cache must carry for each session.
	s := byID(t, res, "a4c2e917")
	if s.Version != "2.1.210" || s.Nulls != 1 || s.Side.Output == 0 || s.Hours.Sum().CacheWrite1h != 3000 || len(s.Rejected) != 1 || res.Homes[c.home].Malformed != 1 {
		t.Fatalf("session = %+v, home %+v", s, res.Homes[c.home])
	}
	noLeak(t, res)
	if b, err := os.ReadFile(filepath.Join(dir, claudeCacheName)); err != nil || bytes.Contains(b, []byte("SECRET")) {
		t.Fatalf("the cache carries log text, or %v", err)
	}
}

// Lines added to a log are parsed alone, and the read is the one a full
// parse gives: a new snapshot of a message replaces the one the cache kept,
// and a new cost-state line replaces the old.
func TestClaudeCacheAppend(t *testing.T) {
	c := newClaudeCorpus(t)
	dir := t.TempDir()
	readCached(dir, c.home)
	added := appendLines(t, c.main,
		corpusUser("u9", "2026-09-21T08:00:00Z", ""),
		corpusCwd("/Users/ann/src/app/api", "2026-09-21T08:00:30Z"),
		corpusMsg("m2", "2026-09-21T08:01:00Z", use(3, 400, 0, 50), `"end_turn"`),
		corpusMsg("m3", "2026-09-21T08:02:00Z", use(20, 30, 100, 900), `"end_turn"`),
		cl{session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-21T08:03:00Z", usage: use(1, 1, 0, 0)}.String(),
		costStateAt("a4c2e917", "2026-09-21T08:04:00Z", [4]int64{90, 12000, 4100, 2800}),
	)
	added += appendLines(t, c.sub,
		cl{id: "s2", req: "rs2", session: "a4c2e917", side: true, cwd: "/Users/ann/src/app", at: "2026-09-21T08:05:00Z", usage: use(2, 2, 0, 0), stop: "null", version: "2.1.211"}.String())
	res, parsed := readCached(dir, c.home)
	sameRead(t, res, c.home)
	if parsed != added {
		t.Fatalf("parsed %d bytes, want the %d added", parsed, added)
	}
	if s := byID(t, res, "a4c2e917"); s.Version != "2.1.211" || s.Nulls != 1 {
		t.Fatalf("session = %+v", s)
	}
	if _, parsed := readCached(dir, c.home); parsed != 0 {
		t.Fatalf("a read after the append parsed %d bytes", parsed)
	}
}

// A log that did not only grow is parsed in full.
func TestClaudeCacheRewritten(t *testing.T) {
	msg := corpusMsg("m9", "2026-09-21T08:00:00Z", use(5, 5, 0, 0), `"end_turn"`)
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, c claudeCorpus)
		// inode is a change only the inode shows, which Windows cannot see.
		inode bool
	}{
		{name: "truncated", write: func(t *testing.T, c claudeCorpus) {
			mustWrite(t, c.main, corpusUser("u0", "2026-09-20T10:00:00Z", ""), msg)
		}},
		// The parsed lines keep their length, so the file still has a line
		// end where the cache's parse stopped; only the hash tells.
		{name: "rewritten larger in place", write: func(t *testing.T, c claudeCorpus) {
			body, err := os.ReadFile(c.main)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.Replace(body, []byte("/Users/ann/src/app"), []byte("/Users/ann/src/abc"), 1)
			if err := os.WriteFile(c.main, append(body, msg+"\n"...), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replaced by a new file", inode: true, write: func(t *testing.T, c claudeCorpus) {
			body, err := os.ReadFile(c.main)
			if err != nil {
				t.Fatal(err)
			}
			tmp := c.main + ".tmp"
			if err := os.WriteFile(tmp, append(body, msg+"\n"...), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(tmp, c.main); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "same size, new time", write: func(t *testing.T, c claudeCorpus) {
			ageFile(t, c.main, now.Add(-30*time.Minute))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClaudeCorpus(t)
			dir := t.TempDir()
			readCached(dir, c.home)
			tc.write(t, c)
			res, parsed := readCached(dir, c.home)
			sameRead(t, res, c.home)
			if want := sizes(t, c.main); parsed != want && !(tc.inode && runtime.GOOS == "windows") {
				t.Fatalf("parsed %d bytes, want all %d", parsed, want)
			}
		})
	}

	// A log read while a line was half written is parsed again in full
	// once the line is done, so the half is not counted as malformed twice.
	c := newClaudeCorpus(t)
	dir := t.TempDir()
	half := msg[:len(msg)/2]
	f, err := os.OpenFile(c.main, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(half); err != nil {
		t.Fatal(err)
	}
	res, _ := readCached(dir, c.home)
	sameRead(t, res, c.home)
	if _, err := f.WriteString(msg[len(half):] + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	res, parsed := readCached(dir, c.home)
	sameRead(t, res, c.home)
	if want := sizes(t, c.main); parsed != want || res.Homes[c.home].Malformed != 1 {
		t.Fatalf("parsed %d bytes, want %d; malformed %d", parsed, want, res.Homes[c.home].Malformed)
	}
}

// A message in a cached log and in a new one counts once, in the session
// that starts first, whichever log was parsed this time.
func TestClaudeCacheSharedMessage(t *testing.T) {
	c := newClaudeCorpus(t)
	dir := t.TempDir()
	readCached(dir, c.home)
	m3 := corpusMsg("m3", "2026-09-20T11:40:00Z", use(30, 300, 0, 0), `"end_turn"`)
	appendLines(t, c.main, m3)
	projects := filepath.Join(c.home, "projects", "-Users-ann-src-app")
	// A fork that starts later repeats m1 and m3, and a log that starts
	// earlier holds m2.
	mustWrite(t, filepath.Join(projects, "c9e2f4b1.jsonl"),
		cl{uuid: "f0", id: "m1", req: "r1", session: "c9e2f4b1", cwd: "/Users/ann/src/app", at: "2026-09-20T10:00:09Z", usage: use(10, 90, 4000, 900), stop: `"tool_use"`}.String(),
		strings.ReplaceAll(m3, "a4c2e917", "c9e2f4b1"),
		cl{id: "f1", req: "rf1", session: "c9e2f4b1", cwd: "/Users/ann/src/app", at: "2026-09-21T09:00:00Z", usage: use(5, 5, 0, 0)}.String(),
	)
	mustWrite(t, filepath.Join(projects, "d3a8b6c2.jsonl"),
		cl{id: "e0", req: "re0", session: "d3a8b6c2", cwd: "/Users/ann/src/app", at: "2026-09-20T09:00:00Z", usage: use(1, 1, 0, 0)}.String(),
		strings.ReplaceAll(corpusMsg("m2", "2026-09-20T11:05:00Z", use(3, 4, 0, 50), "null"), "a4c2e917", "d3a8b6c2"),
	)
	res, _ := readCached(dir, c.home)
	sameRead(t, res, c.home)
	if got := byID(t, res, "c9e2f4b1").Tokens; got != (Tokens{Input: 5, Output: 5}) {
		t.Fatalf("fork tokens = %+v, want its own message's", got)
	}
	if got := byID(t, res, "d3a8b6c2").Tokens; got != (Tokens{Input: 4, Output: 5, CacheRead: 50}) {
		t.Fatalf("earlier log's tokens = %+v", got)
	}
}

func TestClaudeCacheDamaged(t *testing.T) {
	c := newClaudeCorpus(t)
	all := sizes(t, c.main, c.sub, c.copy)
	for name, damage := range map[string]func(t *testing.T, path string){
		"corrupt": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("not a cache"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		// An older cache's states mean something else, even for files
		// that did not change.
		"older": func(t *testing.T, path string) {
			files := cachedFiles(t, filepath.Dir(path))
			for p, e := range files {
				e.State.Msgs = nil
				files[p] = e
			}
			var buf bytes.Buffer
			if err := gob.NewEncoder(&buf).Encode(claudeCacheFile{Version: claudeCacheVersion - 1, Files: files}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			readCached(dir, c.home)
			damage(t, filepath.Join(dir, claudeCacheName))
			res, parsed := readCached(dir, c.home)
			sameRead(t, res, c.home)
			if parsed != all {
				t.Fatalf("parsed %d bytes, want all %d", parsed, all)
			}
			if _, parsed := readCached(dir, c.home); parsed != 0 {
				t.Fatalf("the rebuilt cache parsed %d bytes", parsed)
			}
		})
	}
}

// The cache keeps only the logs the last read saw, and a home read under two
// names parses each log once.
func TestClaudeCacheKeepsSeenLogs(t *testing.T) {
	c := newClaudeCorpus(t)
	dir := t.TempDir()
	readCached(dir, c.home)
	if err := os.Remove(c.copy); err != nil {
		t.Fatal(err)
	}
	res, parsed := readCached(dir, c.home)
	sameRead(t, res, c.home)
	files := cachedFiles(t, dir)
	if parsed != 0 || len(files) != 2 {
		t.Fatalf("parsed %d bytes, cache holds %d logs", parsed, len(files))
	}
	if runtime.GOOS == "windows" {
		return
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(c.home, alias); err != nil {
		t.Fatal(err)
	}
	appendLines(t, c.main, corpusMsg("m3", "2026-09-21T08:02:00Z", use(20, 30, 0, 0), `"end_turn"`))
	res, _ = readCached(dir, c.home, alias)
	sameRead(t, res, c.home, alias)
	if _, parsed := readCached(dir, c.home, alias); parsed != 0 {
		t.Fatalf("parsed %d bytes", parsed)
	}
}

// BenchmarkReadClaude reads about 20 MB of logs made mostly of tool
// results, as a working session's are: in full, and again from the cache.
func BenchmarkReadClaude(b *testing.B) {
	home := b.TempDir()
	result := strings.Repeat("output of a tool ", 200)
	var size int64
	for s := range 40 {
		id := fmt.Sprintf("a4c2%04x", s)
		var body strings.Builder
		for i := range 125 {
			at := time.Date(2026, 9, 20, 10, 0, i, 0, time.UTC).Format(time.RFC3339)
			fmt.Fprintf(&body, `{"type":"user","cwd":"/Users/ann/src/app","sessionId":%q,"message":{"role":"user","content":[{"type":"tool_result","content":%q}]},"uuid":"u%d","timestamp":%q}`+"\n", id, result, i, at)
			body.WriteString(cl{id: fmt.Sprintf("m%d", i), req: fmt.Sprintf("r%d", i), session: id, cwd: "/Users/ann/src/app", at: at, usage: use(10, 200, 30, 4000), stop: `"end_turn"`}.String() + "\n")
		}
		path := filepath.Join(home, "projects", "-Users-ann-src-app", id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
			b.Fatal(err)
		}
		size += int64(body.Len())
	}
	b.Run("full", func(b *testing.B) {
		b.SetBytes(size)
		for b.Loop() {
			ReadHomes("claude", []string{home}, time.Time{})
		}
	})
	b.Run("cached", func(b *testing.B) {
		r := Reader{CacheDir: b.TempDir()}
		r.ReadHomes("claude", []string{home}, time.Time{})
		b.SetBytes(size)
		for b.Loop() {
			r.ReadHomes("claude", []string{home}, time.Time{})
		}
	})
}
