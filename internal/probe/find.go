package probe

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
)

// find locates a harness binary. PATH is tried first, then the usual install
// places, because the system scheduler runs with a short PATH.
func (e Env) find(name string) (string, bool) {
	look := e.LookPath
	if look == nil {
		look = exec.LookPath
	}
	if p, err := look(name); err == nil {
		return p, true
	}
	for _, dir := range append(extraBinDirs(e.HomeDir, name), e.SystemBinDirs...) {
		for _, file := range binNames(name) {
			p := filepath.Join(dir, file)
			if isExecutable(p) {
				return p, true
			}
		}
	}
	return "", false
}

// systemBinDirs are the shared install places the system scheduler's short
// PATH can miss.
func systemBinDirs() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"}
}

// extraBinDirs are the per-user install places.
func extraBinDirs(home, name string) []string {
	var dirs []string
	if runtime.GOOS == "windows" {
		if home != "" {
			dirs = append(dirs,
				filepath.Join(home, ".local", "bin"),
				filepath.Join(home, "."+name, "bin"),
				filepath.Join(home, "AppData", "Roaming", "npm"),
			)
		}
		return dirs
	}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, "."+name, "bin"),
		)
	}
	return dirs
}

// binNames are the file names a harness can have in a directory. On Windows
// it is an .exe, or a .cmd shim when npm installed it.
func binNames(name string) []string {
	if runtime.GOOS == "windows" {
		return []string{name + ".exe", name + ".cmd", name + ".bat"}
	}
	return []string{name}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode()&0o111 != 0
}

// appDirs are the shared places desktop apps are installed.
func appDirs() []string {
	if runtime.GOOS == "darwin" {
		return []string{"/Applications"}
	}
	return nil
}

// bins are the binaries of a harness to try in turn: the one find locates,
// then the copies desktop apps and editor extensions bundle, newest first.
// A bundled copy is often all a user of the app has, and newer than a
// command line installed long ago.
func (e Env) bins(name string) []string {
	var out []string
	if p, ok := e.find(name); ok {
		out = append(out, p)
	}
	type bundled struct {
		path string
		mod  time.Time
	}
	var found []bundled
	for _, p := range e.bundled(name) {
		if info, err := os.Stat(p); err == nil && isExecutable(p) {
			found = append(found, bundled{p, info.ModTime()})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
	seen := map[string]bool{}
	for _, p := range out {
		seen[fsutil.RealPath(p)] = true
	}
	for _, b := range found {
		if r := fsutil.RealPath(b.path); !seen[r] {
			seen[r] = true
			out = append(out, b.path)
		}
	}
	return out
}

// bundled are the places an app or an editor extension keeps its own copy
// of a harness. Only Codex is bundled so: the ChatGPT app and the Codex app
// on macOS, and OpenAI's extension for VS Code and the editors built on it.
func (e Env) bundled(name string) []string {
	if name != "codex" {
		return nil
	}
	file := binNames(name)[0]
	var out []string
	apps := slices.Clone(e.AppDirs)
	if e.HomeDir != "" {
		apps = append(apps, filepath.Join(e.HomeDir, "Applications"))
	}
	for _, dir := range apps {
		for _, app := range []string{"ChatGPT.app", "Codex.app"} {
			out = append(out, filepath.Join(dir, app, "Contents", "Resources", file))
		}
	}
	if e.HomeDir != "" {
		for _, editor := range []string{".vscode", ".vscode-insiders", ".vscode-server", ".cursor", ".cursor-server", ".windsurf"} {
			// The extension keeps one binary per platform it supports.
			matches, _ := filepath.Glob(filepath.Join(e.HomeDir, editor, "extensions", "openai.chatgpt-*", "bin", "*", file))
			out = append(out, matches...)
		}
	}
	return out
}

// Find reports whether a harness binary is installed.
func (e Env) Find(name string) bool {
	return len(e.bins(name)) > 0
}
