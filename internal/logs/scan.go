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

// ForEachLine calls visit for each non-blank line of path, a line longer
// than max bytes squeezed as forEachReader does. It returns how many lines it
// skipped for being longer than max even squeezed.
func ForEachLine(path string, max int, visit func(line []byte)) (long int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return forEachReader(f, max, visit)
}

// forEachReader squeezes a line longer than max as it reads it, so one huge
// tool output neither hides the lines after it nor takes its own size in
// memory. A squeezed line still longer than max is skipped and counted.
func forEachReader(r io.Reader, max int, visit func(line []byte)) (long int, err error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var buf []byte
	var sq *squeezer
	over := false
	for {
		chunk, err := br.ReadSlice('\n')
		line := chunk
		switch {
		case over:
		case sq != nil || len(buf)+len(chunk) > max:
			if sq == nil {
				// What the line kept so far is squeezed where it is.
				sq = &squeezer{}
				buf = sq.add(buf[:0], buf)
			}
			buf = sq.add(buf, chunk)
			line = buf
			if len(buf) > max {
				over, buf = true, buf[:0]
			}
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
		buf, sq, over = buf[:0], nil, false
		if errors.Is(err, io.EOF) {
			return long, nil
		}
		if err != nil {
			return long, err
		}
	}
}

// longString is the longest string a squeezed line keeps. The strings a
// reader takes from a line, such as ids, times, and working directories, are
// far shorter.
const longString = 64 << 10

// squeezer shortens a JSON line too long to keep, as it is read: a string
// longer than longString is left empty, and so is every array. No reader
// takes an array from a line, and the two are what makes a line long: a
// tool's output, a file it read, an image. The keys, the numbers, and the
// short strings stay, and with them a line's type, ids, times, and usage.
// A line that is not JSON stays as broken as it was.
type squeezer struct {
	inString, escaped bool
	// from is where the open string's text starts in the output, and cut
	// is set once that text was too long to keep.
	from int
	cut  bool
	// arrays is how many arrays the read is inside.
	arrays int
}

// add appends what it keeps of in to out. out may be in[:0]: it never
// writes past what it has read.
func (s *squeezer) add(out, in []byte) []byte {
	for _, c := range in {
		if s.inString {
			end := !s.escaped && c == '"'
			s.escaped = !s.escaped && c == '\\'
			s.inString = !end
			switch {
			case s.arrays > 0:
			case end:
				out = append(out, c)
			case s.cut:
			case len(out)-s.from >= longString:
				out, s.cut = out[:s.from], true
			default:
				out = append(out, c)
			}
			continue
		}
		keep := s.arrays == 0
		switch c {
		case '"':
			s.inString, s.cut = true, false
			s.from = len(out) + 1
		case '[':
			s.arrays++
		case ']':
			s.arrays = max(s.arrays-1, 0)
			keep = s.arrays == 0
		}
		if keep {
			out = append(out, c)
		}
	}
	return out
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
