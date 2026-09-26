package logs

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// readCodexHomes sums <home>/sessions and <home>/archived_sessions of every
// home together, and records each home's read in reads.
//
// A rollout file is one thread. Its token_count lines carry the thread's
// cumulative total and the usage of the last request, and a request counts
// once, by that last usage. The same (total, last) pair seen again is a
// repeated notification, a fork replaying its parent's history, or the same
// file kept twice. A fork's cumulative total starts at its parent's, so
// neither the replay nor the seeded total is new usage.
//
// Replays are matched within a family of threads linked by the ids in the
// first session_meta, and the thread that started first keeps the request,
// whichever home holds it. A stale file that a fresh thread links to is read
// only for that matching.
//
// Newer Codex also writes a token_usage_record line for each response, before
// the token_count that repeats it. A compaction's record is the one the next
// token_count leaves out, so a record no token_count repeats counts once per
// response id in the family.
//
// A request's tokens go to the hour of the line that counts it, in the thread
// that counts it.
//
// OpenAI counts cached input inside input_tokens, so cached tokens are moved
// out of Input. token_count lines also carry the rate limits the harness last
// saw; each home's newest reading is kept as a fallback quota reading for
// whoever is logged in at that home.
//
// One file linked into several homes, as Orca links each rollout into every
// account's home, is read once, from the first of them. Its readings are no
// home's fallback, since the file does not say which account it ran under.
func readCodexHomes(homes []string, since time.Time, reads map[string]HomeRead) []Session {
	var files []*codexFile
	stale := map[string]string{}
	linked := sameFiles{}
	for _, h := range homes {
		fs, hr := codexFiles(h, since, stale, linked)
		files = append(files, fs...)
		reads[h] = hr
	}
	files = append(files, linkedCodexFiles(files, stale)...)
	sessions := countCodex(files)
	for _, h := range homes {
		var own []*codexFile
		for _, f := range files {
			if f.home == h && len(f.mirrors) == 0 {
				own = append(own, f)
			}
		}
		hr := reads[h]
		hr.Limits = codexLimits(own)
		reads[h] = hr
	}
	return sessions
}

// codexFiles parses the fresh rollout files of one home and indexes its stale
// ones in stale. A file an earlier home already holds is noted on that one.
func codexFiles(home string, since time.Time, stale map[string]string, linked sameFiles) ([]*codexFile, HomeRead) {
	var out HomeRead
	var files []*codexFile
	for _, leaf := range []string{"sessions", "archived_sessions"} {
		root, err := resolveDir(filepath.Join(home, leaf))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, HomeRead{Err: err}
		}
		files = append(files, walkCodex(root, home, since, stale, linked, &out)...)
	}
	return files, out
}

// walkCodex parses the fresh rollout files under root and indexes the stale
// ones by the thread id in their name.
func walkCodex(root, home string, since time.Time, stale map[string]string, linked sameFiles, out *HomeRead) []*codexFile {
	var files []*codexFile
	walkLogs(root, &out.Unreadable, isJSONL, func(file, _ string, info fs.FileInfo) {
		name := info.Name()
		if first, ok := linked.find(name, info); ok {
			if first != nil && first.home != home && !slices.Contains(first.mirrors, home) {
				first.mirrors = append(first.mirrors, home)
			}
			return
		}
		if !freshEnough(info.ModTime(), since) {
			linked.add(name, info, nil)
			if id := codexNameID(name); id != "" {
				stale[id] = file
			}
			return
		}
		f, bad, err := parseCodex(file)
		out.add(bad, err)
		f.id = cmp.Or(f.id, strings.TrimSuffix(name, ".jsonl"))
		f.home = home
		f.updated = info.ModTime().UTC()
		f.fresh = true
		linked.add(name, info, f)
		files = append(files, f)
	})
	return files
}

// sameFiles finds a file already read under another path, by name and then
// by identity: a hard link, not a copy, is the same file.
type sameFiles map[string][]sameFile

type sameFile struct {
	info fs.FileInfo
	// file is nil for a file that was too old to read.
	file *codexFile
}

func (s sameFiles) find(name string, info fs.FileInfo) (*codexFile, bool) {
	for _, c := range s[name] {
		if os.SameFile(c.info, info) {
			return c.file, true
		}
	}
	return nil, false
}

func (s sameFiles) add(name string, info fs.FileInfo, f *codexFile) {
	s[name] = append(s[name], sameFile{info: info, file: f})
}

// linkedCodexFiles reads stale files that loaded threads name as parent,
// root, or fork source, so a fork of an old thread does not count the history
// it replays. They report no session of their own.
func linkedCodexFiles(files []*codexFile, stale map[string]string) []*codexFile {
	loaded := map[string]bool{}
	for _, f := range files {
		loaded[f.id] = true
	}
	var extra []*codexFile
	queue := append([]*codexFile(nil), files...)
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		for _, id := range f.links {
			path, ok := stale[id]
			if !ok || loaded[id] {
				continue
			}
			loaded[id] = true
			// A stale file that fails to parse only loses its matching.
			p, _, _ := parseCodex(path)
			if p.id == "" {
				p.id = id
			}
			extra = append(extra, p)
			queue = append(queue, p)
		}
	}
	return extra
}

// countCodex gives each fresh file the requests no earlier thread in its
// family already logged, and merges files of one thread into one session.
func countCodex(files []*codexFile) []Session {
	for _, family := range codexFamilies(files) {
		countFamily(family)
	}
	var rows []Session
	for _, f := range files {
		if f.fresh {
			rows = append(rows, f.session())
		}
	}
	return mergeByID(rows, addCopy)
}

// codexFamilies groups the files whose threads link to each other, each
// group in the order its threads started.
func codexFamilies(files []*codexFile) [][]*codexFile {
	fam := unionFind{}
	for _, f := range files {
		fam.add(f.id)
		for _, id := range f.links {
			fam.union(f.id, id)
		}
	}
	at := map[string]int{}
	var families [][]*codexFile
	for _, f := range files {
		root := fam.find(f.id)
		i, ok := at[root]
		if !ok {
			i = len(families)
			at[root] = i
			families = append(families, nil)
		}
		families[i] = append(families[i], f)
	}
	for _, family := range families {
		slices.SortStableFunc(family, func(x, y *codexFile) int {
			return cmp.Or(x.start.Compare(y.start), cmp.Compare(x.id, y.id), cmp.Compare(x.path, y.path))
		})
	}
	return families
}

// countFamily gives each file of one family the requests no earlier file in
// it already logged.
func countFamily(family []*codexFile) {
	seen := map[codexEvent]bool{}
	responses := map[string]bool{}
	for _, f := range family {
		var prev codexUsage
		for _, c := range f.events {
			ev := c.event
			delta := ev.last
			if !ev.hasLast {
				delta = ev.total.since(prev)
			}
			prev = ev.total
			if seen[ev] {
				continue
			}
			seen[ev] = true
			t := delta.tokens()
			f.own = f.own.Add(t)
			AddHour(&f.hours, c.at, t.InOut())
		}
		for _, r := range f.unrepeated {
			if responses[r.response] {
				continue
			}
			responses[r.response] = true
			t := r.usage.tokens()
			f.own = f.own.Add(t)
			AddHour(&f.hours, r.at, t.InOut())
		}
	}
}

// session is the file's row, with the usage countFamily gave it.
func (f *codexFile) session() Session {
	s := Session{ID: f.id, ParentID: f.parent, Project: f.project, Tokens: f.own, Hours: f.hours, Updated: f.updated, Home: f.home, Limits: f.limits[CodexMainLimit]}
	if len(f.mirrors) > 0 {
		s.Homes = append([]string{f.home}, f.mirrors...)
	}
	return s
}

// unionFind groups thread ids that may share replayed history.
type unionFind map[string]string

func (u unionFind) add(x string) {
	if _, ok := u[x]; !ok {
		u[x] = x
	}
}

func (u unionFind) find(x string) string {
	u.add(x)
	for u[x] != x {
		u[x] = u[u[x]]
		x = u[x]
	}
	return x
}

func (u unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if rb < ra {
		ra, rb = rb, ra
	}
	u[rb] = ra
}
