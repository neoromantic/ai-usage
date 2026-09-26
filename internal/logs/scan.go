package logs

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxLineBytes = 32 << 20

// deniedFile is a credential or secret name. The collector never opens these.
func deniedFile(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	switch base {
	case "auth.json", "credentials.json", ".credentials.json", ".env", "cookies", "cookies.json":
		return true
	}
	if strings.HasPrefix(base, ".env") || strings.Contains(base, "credential") || strings.Contains(base, "cookie") {
		return true
	}
	return false
}

// walkLogs visits each file under root that keep accepts, by its
// slash-separated path relative to root. Credential files are never visited.
// A missing root holds nothing; any other error counts as unreadable.
func walkLogs(root string, unreadable *int, keep func(rel string) bool, visit func(file, rel string, info fs.FileInfo)) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// d is nil only when root itself cannot be read.
			if d != nil || !errors.Is(err, fs.ErrNotExist) {
				*unreadable++
			}
			return nil
		}
		if d.IsDir() || deniedFile(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if err != nil || !keep(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			*unreadable++
			return nil
		}
		visit(path, rel, info)
		return nil
	})
}

func isJSONL(rel string) bool { return strings.HasSuffix(rel, ".jsonl") }

// forEachLine calls visit for each non-blank line of path. It returns how
// many lines it skipped for being longer than maxLineBytes.
func forEachLine(path string, visit func(line []byte)) (long int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return forEachReader(f, visit)
}

// forEachReader skips a line longer than maxLineBytes and reads on, so one
// huge tool output does not hide the usage lines after it. Those are small.
func forEachReader(r io.Reader, visit func(line []byte)) (long int, err error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var buf []byte
	over := false
	for {
		chunk, err := br.ReadSlice('\n')
		line := chunk
		switch {
		case over:
		case len(buf)+len(chunk) > maxLineBytes:
			over, buf = true, buf[:0]
		case len(buf) > 0 || errors.Is(err, bufio.ErrBufferFull):
			// A line longer than the reader's buffer comes in pieces.
			buf = append(buf, chunk...)
			line = buf
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if over {
			long++
		} else if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			visit(trimmed)
		}
		buf, over = buf[:0], false
		if errors.Is(err, io.EOF) {
			return long, nil
		}
		if err != nil {
			return long, err
		}
	}
}

func freshEnough(mod, since time.Time) bool {
	if since.IsZero() {
		return true
	}
	return !mod.Before(since)
}

func resolveDir(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", os.ErrNotExist
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return resolved, nil
}
