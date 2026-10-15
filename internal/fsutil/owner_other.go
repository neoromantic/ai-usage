//go:build !unix

package fsutil

// Mine reports no file as the user's: only Unix systems have owners that
// Homebrew installs are told apart by.
func Mine(string) bool { return false }
