package logs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
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

func ageFile(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
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

func mustRead(t *testing.T, provider, home string, since time.Time) Result {
	t.Helper()
	res, err := Read(provider, home, since)
	if err != nil {
		t.Fatalf("Read(%s): %v", provider, err)
	}
	return res
}

func byID(t *testing.T, res Result, id string) Session {
	t.Helper()
	for _, s := range res.Sessions {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no session %q in %+v", id, res.Sessions)
	return Session{}
}

func ids(res Result) []string {
	out := make([]string, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}

func total(res Result) Tokens {
	var sum Tokens
	for _, s := range res.Sessions {
		sum = sum.Add(s.Tokens)
	}
	return sum
}

// noLeak fails when any session field carries log text.
func noLeak(t *testing.T, res Result) {
	t.Helper()
	for _, s := range res.Sessions {
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
