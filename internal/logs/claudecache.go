package logs

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"

	"github.com/neoromantic/ai-usage/internal/fsutil"
)

// claudeCacheVersion names what a cached state means. Raise it, and the
// file name with it, whenever the parse keeps something new or reads a line
// differently: an older cache is then read as none.
const (
	claudeCacheVersion = 1
	claudeCacheName    = "claude-v1.gob"
)

// edgeBytes is how much of a file's start and end the cache hashes, to tell
// a file that grew from one that was rewritten.
const edgeBytes = 256

// claudeParsed counts the bytes of Claude logs parsed, for tests.
var claudeParsed atomic.Int64

// claudeCache keeps what a read parsed of each Claude log, so the next read
// skips a log that has not changed and parses only the end of one that grew.
// Claude Code only appends to a transcript, and a session's transcripts
// hold most of its history. A nil cache parses every log in full.
type claudeCache struct {
	path string
	// prev is the cache as the last read left it, and next what this read
	// keeps: the logs it read, and so none that vanished.
	prev, next map[string]claudeEntry
	// dirty is set once this read parsed a log.
	dirty bool
}

type claudeCacheFile struct {
	Version int
	Files   map[string]claudeEntry
}

// claudeEntry is one log as the cache keeps it: the file it was, and the
// state its lines gave.
type claudeEntry struct {
	Key claudeKey
	// Edges hashes the file's first bytes and its last ones up to Size.
	Edges uint64
	State claudeState
}

// claudeKey tells one version of a file from another. Windows has no inode
// here, so there the size and time tell it alone.
type claudeKey struct {
	Dev, Ino      uint64
	Size, ModTime int64
}

func keyOf(info fs.FileInfo) claudeKey {
	dev, ino := fileID(info)
	return claudeKey{Dev: dev, Ino: ino, Size: info.Size(), ModTime: info.ModTime().UnixNano()}
}

// loadClaudeCache reads the cache in dir. An empty dir keeps none, and a
// missing, damaged, or older cache starts empty.
func loadClaudeCache(dir string) *claudeCache {
	if dir == "" {
		return nil
	}
	c := &claudeCache{path: filepath.Join(dir, claudeCacheName), next: map[string]claudeEntry{}}
	f, err := os.Open(c.path)
	if err != nil {
		return c
	}
	defer f.Close()
	var cf claudeCacheFile
	if gob.NewDecoder(bufio.NewReader(f)).Decode(&cf) == nil && cf.Version == claudeCacheVersion {
		c.prev = cf.Files
	}
	return c
}

// save replaces the cache with what this read kept, when that changed.
func (c *claudeCache) save() error {
	if c == nil || (!c.dirty && len(c.next) == len(c.prev)) {
		return nil
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(claudeCacheFile{Version: claudeCacheVersion, Files: c.next}); err != nil {
		return err
	}
	return fsutil.WriteFile(c.path, buf.Bytes(), 0o600)
}

// lookup is what the cache kept of path: from this read when it has read
// path already, as it does for a home linked under two names.
func (c *claudeCache) lookup(path string) (claudeEntry, bool) {
	if c == nil {
		return claudeEntry{}, false
	}
	if e, ok := c.next[path]; ok {
		return e, true
	}
	e, ok := c.prev[path]
	return e, ok
}

// parse reads one log: from the cache when the file has not changed since,
// from where the cache's parse stopped when lines were only added, and in
// full otherwise. It returns the file, how many of its lines were
// malformed or too long, and why it could not be read.
func (c *claudeCache) parse(path, id, parent string) (*claudeFile, int, error) {
	f := &claudeFile{path: path, sess: Session{ID: id, ParentID: parent}}
	fh, err := os.Open(path)
	if err != nil {
		return f, 0, err
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil {
		return f, 0, err
	}
	key := keyOf(info)
	from := int64(0)
	if old, ok := c.lookup(path); ok {
		if old.Key == key {
			c.next[path] = old
			f.claudeState = old.State
			return f.done(), f.Bad, nil
		}
		if old.grew(key) && edgeHash(fh, old.Key.Size) == old.Edges {
			f.claudeState = old.State
			// The cache's messages are shared; a later snapshot replaces one.
			f.Msgs = slices.Clone(f.Msgs)
			from = old.Key.Size
		}
	}
	p := newClaudeParse(f)
	long, err := forEachReader(io.NewSectionReader(fh, from, key.Size-from), maxLineBytes, p.line)
	claudeParsed.Add(key.Size - from)
	f.Bad += long
	if err == nil && c != nil {
		c.next[path] = claudeEntry{Key: key, Edges: edgeHash(fh, key.Size), State: f.claudeState}
		c.dirty = true
	}
	return f.done(), f.Bad, err
}

// grew is set when to can be the same file as e with lines added after the
// ones e parsed: a larger size, and a last parsed line that was complete,
// which a zero Edges says it was not. The hash of the edges then checks
// that the lines e parsed are still there: Claude Code rewrites a file it
// resumes or compacts.
func (e claudeEntry) grew(to claudeKey) bool {
	return to.Dev == e.Key.Dev && to.Ino == e.Key.Ino && to.Size > e.Key.Size && e.Edges != 0
}

// edgeHash hashes the first edgeBytes of r and the last edgeBytes before
// offset size. It is 0 when those do not end a line, so a parse that
// stopped inside a line is never taken up again.
func edgeHash(r io.ReaderAt, size int64) uint64 {
	n := min(size, edgeBytes)
	head, tail := make([]byte, n), make([]byte, n)
	if n == 0 {
		return 0
	}
	if _, err := r.ReadAt(tail, size-n); err != nil || tail[n-1] != '\n' {
		return 0
	}
	if _, err := r.ReadAt(head, 0); err != nil {
		return 0
	}
	h := fnv.New64a()
	h.Write(head)
	h.Write(tail)
	return h.Sum64()
}
