package logs

import (
	"bufio"
	"bytes"
	"errors"
	"io"
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
