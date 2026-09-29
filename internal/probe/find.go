package probe

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
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
		for _, editor := range editors {
			// The extension keeps one binary per platform it supports.
			matches, _ := filepath.Glob(filepath.Join(e.HomeDir, editor, "extensions", "openai.chatgpt-*", "bin", "*", file))
			out = append(out, matches...)
		}
	}
	return out
}

// editors are the folders where VS Code and the editors built on it keep
// their extensions, some of which bundle a harness.
var editors = []string{".vscode", ".vscode-insiders", ".vscode-server", ".cursor", ".cursor-server", ".windsurf"}

// Find reports whether a harness binary is installed.
func (e Env) Find(name string) bool {
	if name == "claude" {
		return len(e.claudeInstalls()) > 0
	}
	return len(e.bins(name)) > 0
}

// candidate is one install of Claude Code: its binary, its version when
// known, and how it was installed, one of the kind constants or "".
type candidate struct {
	path, version, kind string
}

// How Claude Code was installed, as errors name it.
const (
	kindHomebrew  = "Homebrew"
	kindNPM       = "npm"
	kindNative    = "native"
	kindApp       = "desktop app"
	kindExtension = "editor extension"
)

// claudeInstalls are the Claude Code binaries installed, each once however
// many links lead to it: the one find locates, then the others in the
// per-user and shared install places, the native installer's versions, the
// one the desktop app keeps, and those editor extensions bundle. People
// update the one they use, which the system scheduler's PATH can miss, and
// an old one first on it would leave them without a quota reading. The
// version is the one the install's folders tell, where they do.
func (e Env) claudeInstalls() []candidate {
	var out []candidate
	seen := map[string]bool{}
	add := func(path, version, kind string) {
		if r := fsutil.RealPath(path); !seen[r] {
			seen[r] = true
			out = append(out, candidate{path, version, kind})
		}
	}
	if p, ok := e.find("claude"); ok {
		add(p, "", "")
	}
	for _, dir := range append(extraBinDirs(e.HomeDir, "claude"), e.SystemBinDirs...) {
		for _, file := range binNames("claude") {
			if p := filepath.Join(dir, file); isExecutable(p) {
				add(p, "", "")
			}
		}
	}
	file := binNames("claude")[0]
	var apps []string
	for _, dir := range e.AppDirs {
		apps = append(apps, filepath.Join(dir, "Claude.app", "Contents", "Resources", "app", "claude-code"))
	}
	if e.HomeDir != "" {
		apps = append(apps, filepath.Join(e.HomeDir, "Applications", "Claude.app", "Contents", "Resources", "app", "claude-code"))
		if p := filepath.Join(e.HomeDir, ".claude", "local", file); isExecutable(p) {
			add(p, "", kindNPM)
		}
		versions := filepath.Join(e.HomeDir, ".local", "share", "claude", "versions")
		for _, v := range versionNames(versions) {
			if p := filepath.Join(versions, v.file); isExecutable(p) {
				add(p, v.version, kindNative)
			}
		}
	}
	for _, dir := range apps {
		for _, v := range versionNames(dir) {
			if p := filepath.Join(dir, v.file, file); isExecutable(p) {
				add(p, v.version, kindApp)
			}
		}
	}
	if e.HomeDir != "" {
		// The desktop app on macOS downloads its own Claude Code here.
		dir := filepath.Join(e.HomeDir, "Library", "Application Support", "Claude", "claude-code")
		for _, v := range versionNames(dir) {
			if p := filepath.Join(dir, v.file, "claude.app", "Contents", "MacOS", "claude"); isExecutable(p) {
				add(p, v.version, kindApp)
			}
		}
		for _, editor := range editors {
			matches, _ := filepath.Glob(filepath.Join(e.HomeDir, editor, "extensions", "anthropic.claude-code-*", "resources", "native-binary", file))
			for _, p := range matches {
				if isExecutable(p) {
					add(p, extensionVersion.FindString(filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(p))))), kindExtension)
				}
			}
		}
	}
	return out
}

// extensionVersion is the version in the folder name of Anthropic's
// extension, as anthropic.claude-code-2.1.235-darwin-arm64.
var extensionVersion = regexp.MustCompile(`\d+\.\d+\.\d+`)

type versionName struct{ file, version string }

// versionNames are the entries of dir named as a Claude Code version, with
// an .exe on Windows or without, the newest first.
func versionNames(dir string) []versionName {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []versionName
	for _, e := range entries {
		v := strings.TrimSuffix(e.Name(), ".exe")
		if claudeVersionName.MatchString(v) {
			out = append(out, versionName{e.Name(), v})
		}
	}
	slices.SortStableFunc(out, func(a, b versionName) int { return compareVersions(b.version, a.version) })
	return out
}

// compareVersions orders versions as X.Y.Z numbers, and one not known before
// any known.
func compareVersions(a, b string) int {
	switch {
	case a == b:
		return 0
	case selfupdate.Newer(a, b) || b == "":
		return 1
	case selfupdate.Newer(b, a) || a == "":
		return -1
	}
	return 0
}

// claudeCandidates are claudeInstalls, each with its version, from its
// folders or else as `claude --version` tells it, and how it was installed.
func (e Env) claudeCandidates(ctx context.Context) []candidate {
	cs := e.claudeInstalls()
	for i := range cs {
		c := &cs[i]
		if c.version == "" {
			c.version = claudeVersion(c.path)
		}
		if c.version == "" {
			c.version = e.claudeVersionOutput(ctx, c.path)
		}
		if c.kind == "" {
			c.kind = claudeKind(c.path)
		}
	}
	return cs
}

// claudeVersionTimeout bounds `claude --version`, which answers at once.
var claudeVersionTimeout = 5 * time.Second

// claudeVersionSaid matches what `claude --version` prints, as
// "2.1.281 (Claude Code)".
var claudeVersionSaid = regexp.MustCompile(`(\d+\.\d+\.\d+) \(Claude Code\)`)

// claudeVersions are the versions `claude --version` told in this process,
// by the binary's real path, size, and time.
var claudeVersions sync.Map

// claudeVersionOutput is the version `claude --version` tells for bin, or ""
// when it does not in time. It runs only for an install whose folders do not
// tell, such as one behind a version manager's shim.
func (e Env) claudeVersionOutput(ctx context.Context, bin string) string {
	key := ""
	if info, err := os.Stat(bin); err == nil {
		key = fmt.Sprintf("%s\x00%d\x00%d", fsutil.RealPath(bin), info.Size(), info.ModTime().UnixNano())
		if v, ok := claudeVersions.Load(key); ok {
			return v.(string)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, claudeVersionTimeout)
	defer cancel()
	out, _ := e.command(ctx, bin, []string{"DISABLE_AUTOUPDATER", "1"}, "--version").Output()
	v := ""
	if m := claudeVersionSaid.FindSubmatch(out); m != nil {
		v = string(m[1])
	}
	if key != "" && ctx.Err() == nil {
		claudeVersions.Store(key, v)
	}
	return v
}

// claudeKind is how the Claude Code at path was installed, as its path and
// the one its links lead to tell, or "".
func claudeKind(path string) string {
	for _, p := range []string{fsutil.RealPath(path), path} {
		p = filepath.ToSlash(p)
		switch {
		case strings.Contains(p, "/extensions/anthropic.claude-code-"):
			return kindExtension
		case strings.Contains(p, "/Claude.app/") || strings.Contains(p, "/Claude/claude-code/"):
			return kindApp
		// npm under Homebrew's node is an npm install.
		case strings.Contains(p, "/node_modules/") || strings.Contains(p, "/.npm-global/") ||
			strings.Contains(p, "/.claude/local/") || strings.Contains(p, "/Roaming/npm/"):
			return kindNPM
		case strings.Contains(p, "/homebrew/") || strings.Contains(p, "/Cellar/") || strings.Contains(p, "/Caskroom/"):
			return kindHomebrew
		case strings.Contains(p, "/claude/versions/"):
			return kindNative
		}
	}
	return ""
}
