package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileReplacesAtomicallyAndPrivately(t *testing.T) {
	// Windows has no Unix permission bits.
	unix := runtime.GOOS != "windows"
	path := filepath.Join(t.TempDir(), "nested", "file.json")
	for _, w := range []struct {
		body string
		perm os.FileMode
	}{{"one", 0o600}, {"two", 0o644}} {
		if err := WriteFile(path, []byte(w.body), w.perm); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil || string(b) != w.body {
			t.Fatalf("content = %q, %v; want %q", b, err, w.body)
		}
		if mode := perm(t, path); unix && mode != w.perm {
			t.Fatalf("file mode = %o, want %o", mode, w.perm)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temporary files left behind: %v", names)
	}
	if mode := perm(t, filepath.Dir(path)); unix && mode&0o077 != 0 {
		t.Fatalf("new folder mode = %o, want no group or other access", mode)
	}
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
