//go:build !unix

package logs

import "io/fs"

// fileID is zero where the file info carries no inode, as on Windows: the
// parse cache then tells files apart by size and time alone.
func fileID(fs.FileInfo) (dev, ino uint64) { return 0, 0 }
