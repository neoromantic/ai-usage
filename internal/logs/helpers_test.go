package logs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/meter"
)

// now is the fixed clock of every test. File times are set relative to it.
var (
	now   = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	since = now.AddDate(0, 0, -90)
)

// secret is text that must never leave a log.
const secret = "SECRET-PROMPT-DO-NOT-LEAK"

func mustWrite(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n")
	if len(lines) > 0 {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repeat reads n copies of c.
type repeat struct {
	c byte
	n int
}

func (r *repeat) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	p = p[:min(len(p), r.n)]
	for i := range p {
		p[i] = r.c
	}
	r.n -= len(p)
	return len(p), nil
}

// hugeLine reads head, n copies of c, and tail as one line, so a test can
// write or read a line over the limit without holding it.
func hugeLine(head string, c byte, n int, tail string) io.Reader {
	return io.MultiReader(strings.NewReader(head), &repeat{c: c, n: n}, strings.NewReader(tail+"\n"))
}

// lines reads each line in turn.
func lines(ls ...string) io.Reader {
	return strings.NewReader(strings.Join(ls, "\n") + "\n")
}

// mustWriteFrom writes path with what parts read, one after another.
func mustWriteFrom(t *testing.T, path string, parts ...io.Reader) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(f, io.MultiReader(parts...)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func ageFile(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// mustTime reads s, a time in RFC 3339 with or without fractional seconds.
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// deny makes path unreadable, so a reader that opened it would count it as
// unreadable. Root and Windows ignore the mode; tests that depend on the
// error call needDeny first.
func deny(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

func needDeny(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not deny reads on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads files whatever their mode")
	}
}

// homeResult is a read of one home: its sessions, and how the read went.
type homeResult struct {
	Result
	HomeRead
}

// readOne reads one provider home. Files older than since are skipped.
// Sub-agent sessions are rolled into their parents.
func readOne(provider, home string, since time.Time) (homeResult, error) {
	res := ReadHomes(provider, []string{home}, since)
	hr := res.Homes[home]
	return homeResult{res, hr}, hr.Err
}

func mustRead(t *testing.T, provider, home string, since time.Time) homeResult {
	t.Helper()
	res, err := readOne(provider, home, since)
	if err != nil {
		t.Fatalf("readOne(%s): %v", provider, err)
	}
	return res
}

// sessionSet is a read of one home or of several.
type sessionSet interface{ sessions() []Session }

func (r Result) sessions() []Session { return r.Sessions }

func byID(t *testing.T, res sessionSet, id string) Session {
	t.Helper()
	for _, s := range res.sessions() {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no session %q in %+v", id, res.sessions())
	return Session{}
}

func ids(res sessionSet) []string {
	out := make([]string, 0, len(res.sessions()))
	for _, s := range res.sessions() {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}

func total(res sessionSet) Tokens {
	var sum Tokens
	for _, s := range res.sessions() {
		sum = sum.Add(s.Tokens)
	}
	return sum
}

// checkHours fails unless s spent its input plus output in the hours of
// want, which maps a time in each hour, in RFC 3339, to that hour's input
// plus output, and unless each kind of its hours adds up to its tokens.
func checkHours(t *testing.T, s Session, want map[string]int64) {
	t.Helper()
	w := map[int64]int64{}
	for at, n := range want {
		w[HourOf(mustTime(t, at))] += n
	}
	sum := s.Hours.Sum()
	if got := s.Hours.Series(meter.Use.InOut); !reflect.DeepEqual(got, w) || sum.Tokens() != s.Tokens {
		t.Fatalf("%s hours = %s, adding up to %+v, want %v, adding up to %+v", s.ID, showHours(s.Hours), sum, w, s.Tokens)
	}
}

func showHours(hours meter.Hours) string {
	keys := make([]int64, 0, len(hours))
	for h := range hours {
		keys = append(keys, h)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, h := range keys {
		out = append(out, fmt.Sprintf("%s=%d", HourStart(h).Format("2006-01-02T15h"), hours[h].InOut()))
	}
	return "[" + strings.Join(out, " ") + "]"
}

// noLeak fails when any session field carries log text.
func noLeak(t *testing.T, res sessionSet) {
	t.Helper()
	for _, s := range res.sessions() {
		for _, v := range []string{s.ID, s.ParentID, s.Project, s.Account} {
			if strings.Contains(v, "SECRET") {
				t.Fatalf("session %+v carries log text", s)
			}
		}
	}
}

// tree records every entry under root with its size, mode, time, and content
// hash, so a test can prove a reader left a harness home untouched.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		entry := info.Mode().String() + " " + info.ModTime().UTC().Format(time.RFC3339Nano)
		if info.Mode().IsRegular() {
			// A denied file keeps its mode and time; the test cannot read it either.
			body, err := os.ReadFile(path)
			if err != nil && !errors.Is(err, fs.ErrPermission) {
				return err
			}
			sum := sha256.Sum256(body)
			entry += " " + hex.EncodeToString(sum[:])
		}
		out[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed: %q -> %q", k, v, after[k])
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("%s was created", k)
		}
	}
}
