package logs

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
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

// ForEachLine calls visit for each non-blank line of path. It returns how
// many lines it skipped for being longer than max bytes.
func ForEachLine(path string, max int, visit func(line []byte)) (long int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return forEachReader(f, max, visit)
}

// forEachReader skips a line longer than max and reads on, so one huge tool
// output does not hide the lines after it. The lines callers decode are small.
func forEachReader(r io.Reader, max int, visit func(line []byte)) (long int, err error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var buf []byte
	over := false
	for {
		chunk, err := br.ReadSlice('\n')
		line := chunk
		switch {
		case over:
		case len(buf)+len(chunk) > max:
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

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// looseTime is a time a log or database writes as Unix seconds or
// milliseconds, or as an ISO 8601 string. A value it cannot read, or one at
// or before the Unix epoch, is the zero time rather than an error, so one odd
// time does not reject the line or row.
func looseTime(v any) time.Time {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case int64:
		f = float64(x)
	case []byte:
		return looseTime(string(x))
	case string:
		s := strings.TrimSpace(x)
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"} {
				if t, err := time.Parse(layout, s); err == nil && t.After(time.Unix(0, 0)) {
					return t.UTC()
				}
			}
			return time.Time{}
		}
		f = n
	default:
		return time.Time{}
	}
	if !(f > 0 && f < 1e15) {
		return time.Time{}
	}
	// Seconds stay below this until the year 5138; milliseconds pass it in 1973.
	if f >= 1e11 {
		f /= 1000
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
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
