// Package fsutil holds the file helpers that several packages share.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFile replaces path in one step, through a temporary file and a rename,
// so a reader never sees half of it. Missing folders are made 0700.
func WriteFile(path string, b []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// CreateTemp always makes a new 0600 file. A fixed temp name would reuse a
	// leftover file and write into whatever looser mode it had.
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if werr == nil {
		// Without this a crash can leave the renamed file empty.
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(f.Name(), perm)
	}
	if werr == nil {
		werr = os.Rename(f.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(f.Name())
	}
	return werr
}
