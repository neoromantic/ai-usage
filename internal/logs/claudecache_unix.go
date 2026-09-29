//go:build unix

package logs

import (
	"io/fs"
	"syscall"
)

// fileID is the device and inode of the file info describes.
func fileID(info fs.FileInfo) (dev, ino uint64) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return 0, 0
}
