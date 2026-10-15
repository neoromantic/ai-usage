//go:build unix

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMine(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if !Mine(dir) || !Mine(link) {
		t.Fatal("a folder this test made is not its own")
	}
	if Mine(filepath.Join(dir, "missing")) {
		t.Fatal("a missing file is the test's")
	}
	if os.Geteuid() != 0 && Mine("/") {
		t.Fatal("/ is the test's")
	}
}
