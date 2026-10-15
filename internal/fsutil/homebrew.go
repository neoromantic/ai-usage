package fsutil

import (
	"os"
	"path/filepath"
	"regexp"
)

// HomebrewTap is the formula's full name, which every brew command takes:
// Homebrew trusts a third-party tap's formula only by that name, and
// refreshes the tap before an upgrade only when it is named.
const HomebrewTap = "neoromantic/tap/ai-usage"

// HomebrewUpgrade is the command that brings a Homebrew copy to the latest
// release.
const HomebrewUpgrade = "brew upgrade " + HomebrewTap

// keg matches the binary in a keg of the ai-usage formula. The version folder
// can carry a revision, such as 0.8.0_1.
var keg = regexp.MustCompile(`^(.+)/Cellar/ai-usage/[^/]+/bin/ai-usage$`)

// Homebrew reports whether real, an executable's resolved path, is the
// binary of an ai-usage keg, and returns the Homebrew prefix that holds it.
// The path is never looked up: once brew has removed an old keg, a process
// still running from it has a path that no longer exists.
func Homebrew(real string) (prefix string, ok bool) {
	m := keg.FindStringSubmatch(filepath.ToSlash(real))
	if m == nil {
		return "", false
	}
	return filepath.FromSlash(m[1]), true
}

// HomebrewStable is the path a scheduler should run for real: the formula's
// opt link, prefix/opt/ai-usage/bin/ai-usage, which brew moves to each new
// keg, when real is in a keg and that link exists; otherwise real. The
// result is never resolved, so the scheduler entry outlives upgrades.
func HomebrewStable(real string) string {
	prefix, ok := Homebrew(real)
	if !ok {
		return real
	}
	opt := filepath.Join(prefix, "opt", "ai-usage")
	if fi, err := os.Lstat(opt); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return real
	}
	return filepath.Join(opt, "bin", "ai-usage")
}
