//go:build unix

package fsutil

import (
	"os"
	"syscall"
)

// Mine reports whether path, followed through links, belongs to the user
// this process runs as.
func Mine(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
