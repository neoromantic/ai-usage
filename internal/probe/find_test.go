package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"
)

func writeExe(t *testing.T, path string) {
	t.Helper()
	writeFile(t, path, "#!/bin/sh\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func notOnPath(string) (string, error) { return "", exec.ErrNotFound }

func TestFindUsesPathFirst(t *testing.T) {
	home := t.TempDir()
	writeExe(t, filepath.Join(home, ".local", "bin", binNames("claude")[0]))
	env := Env{HomeDir: home, LookPath: func(n string) (string, error) { return "/on/path/" + n, nil }}
	if p, ok := env.find("claude"); !ok || p != "/on/path/claude" {
		t.Errorf("find = %q, %v", p, ok)
	}
}

// A harness in its own ~/.<name>/bin is found when PATH lacks it. TestBins
// covers ~/.local/bin.
func TestFindFallbackDirs(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(home, ".codex", "bin", binNames("codex")[0])
	writeExe(t, want)
	if p, ok := (Env{HomeDir: home, LookPath: notOnPath}).find("codex"); !ok || p != want {
		t.Errorf("find = %q, %v; want %q", p, ok, want)
	}
}

func TestFindWindowsShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("npm .cmd shims are a Windows install")
	}
	home := t.TempDir()
	want := filepath.Join(home, "AppData", "Roaming", "npm", "codex.cmd")
	writeExe(t, want)
	if p, ok := (Env{HomeDir: home, LookPath: notOnPath}).find("codex"); !ok || p != want {
		t.Errorf("find = %q, %v; want %q", p, ok, want)
	}
}

func TestFindSkipsDirsAndNonExecutables(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin", binNames("claude")[0]), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		p := filepath.Join(home, "bin", "claude")
		writeExe(t, p)
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if p, ok := (Env{HomeDir: home, LookPath: notOnPath}).find("claude"); ok {
		t.Errorf("found %q", p)
	}
}

func TestFindSystemDirsWithoutHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no shared bin directories are searched on Windows")
	}
	dir := t.TempDir()
	want := filepath.Join(dir, "grok")
	writeExe(t, want)
	env := Env{LookPath: notOnPath, SystemBinDirs: []string{filepath.Join(dir, "missing"), dir}}
	if p, ok := env.find("grok"); !ok || p != want {
		t.Errorf("find = %q, %v; want %q", p, ok, want)
	}
}

func TestBins(t *testing.T) {
	home := t.TempDir()
	onPath := filepath.Join(home, ".local", "bin", binNames("codex")[0])
	writeExe(t, onPath)
	env := Env{HomeDir: home, LookPath: notOnPath}
	old := bundle(t, home, filepath.Join(".cursor", "extensions", "openai.chatgpt-0.4.1", "bin", "linux-x86_64", binNames("codex")[0]), testNow.Add(-72*time.Hour))
	newer := bundle(t, home, filepath.Join(".cursor", "extensions", "openai.chatgpt-0.5.0", "bin", "linux-x86_64", binNames("codex")[0]), testNow)
	app := bundle(t, home, chatGPTApp, testNow.Add(-24*time.Hour))
	want := []string{onPath, newer, app, old}
	if runtime.GOOS != "windows" {
		// A link to a binary already listed, and a file that cannot run,
		// are left out.
		link := filepath.Join(home, "Applications", "Codex.app", "Contents", "Resources", "codex")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(onPath, link); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(bundle(t, home, vscodeExt, testNow), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := env.bins("codex"); !slices.Equal(got, want) {
		t.Errorf("bins = %q, want %q", got, want)
	}
	writeExe(t, filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-26.5.1", "bin", "x", binNames("claude")[0]))
	if got := env.bins("claude"); got != nil {
		t.Errorf("claude bins = %q", got)
	}
}

// Every Claude Code installed is found once, however many links lead to it,
// with the version its folders tell and how it was installed.
func TestClaudeInstalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the layouts are macOS and Linux installs")
	}
	home, shared := t.TempDir(), t.TempDir()
	brew := claudeLayout(t, shared, kindHomebrew, "2.1.42")
	native := claudeLayout(t, home, kindNative, "2.1.210")
	older := claudeLayout(t, home, kindNative, "2.1.200")
	writeFile(t, filepath.Join(home, ".local", "share", "claude", "versions", "notes.txt"), "")
	link(t, native, filepath.Join(home, ".local", "bin", "claude"))
	local := filepath.Join(home, ".claude", "local", "claude")
	writeExe(t, local)
	writeFile(t, filepath.Join(home, ".claude", "local", "node_modules", "@anthropic-ai", "claude-code", "package.json"), `{"name":"@anthropic-ai/claude-code","version":"2.1.150"}`)
	apps := filepath.Join(shared, "Applications")
	bundled := filepath.Join(apps, "Claude.app", "Contents", "Resources", "app", "claude-code", "2.1.220", "claude")
	writeExe(t, bundled)
	app := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code", "2.1.230", "claude.app", "Contents", "MacOS", "claude")
	writeExe(t, app)
	ext := filepath.Join(home, ".cursor", "extensions", "anthropic.claude-code-2.1.235-darwin-arm64", "resources", "native-binary", "claude")
	writeExe(t, ext)
	// OpenAI's extension bundles Codex, not Claude Code.
	writeExe(t, filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-26.5.1", "resources", "native-binary", "claude"))

	env := Env{HomeDir: home, LookPath: func(string) (string, error) { return brew, nil }, SystemBinDirs: []string{filepath.Dir(brew)}, AppDirs: []string{apps}}
	want := []candidate{
		{brew, "2.1.42", kindHomebrew},
		{filepath.Join(home, ".local", "bin", "claude"), "2.1.210", kindNative},
		{local, "2.1.150", kindNPM},
		{older, "2.1.200", kindNative},
		{bundled, "2.1.220", kindApp},
		{app, "2.1.230", kindApp},
		{ext, "2.1.235", kindExtension},
	}
	if got := env.claudeCandidates(context.Background()); !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if c, ok := claudeForUsage(want); !ok || c != want[6] {
		t.Errorf("read with %v", c)
	}
	// Someone who uses only the desktop app has Claude Code all the same.
	env = Env{HomeDir: t.TempDir(), LookPath: notOnPath}
	writeExe(t, filepath.Join(env.HomeDir, "Library", "Application Support", "Claude", "claude-code", "2.1.230", "claude.app", "Contents", "MacOS", "claude"))
	if !env.Find("claude") {
		t.Error("the desktop app's Claude Code is not found")
	}
}

// Of the installs, the usage is read with the newest that caches it, else
// the newest that prints it, else one whose version is not known.
func TestClaudeForUsage(t *testing.T) {
	for _, tc := range []struct {
		versions []string
		want     int // the index chosen, or -1 for none
	}{
		{[]string{"2.1.42", "2.1.210", "2.1.300", "2.1.208"}, 2},
		{[]string{"2.1.42", "2.1.200", "", "2.1.195"}, 1},
		{[]string{"2.1.42", "", "2.1.100"}, 1},
		{[]string{"2.1.42", "1.0.128"}, -1},
		{nil, -1},
	} {
		var cs []candidate
		for _, v := range tc.versions {
			cs = append(cs, candidate{path: "claude-" + v, version: v})
		}
		c, ok := claudeForUsage(cs)
		if ok != (tc.want >= 0) || ok && c != cs[tc.want] {
			t.Errorf("%q: got %v, %v", tc.versions, c, ok)
		}
	}
}

// An install whose folders do not tell its version, as behind a version
// manager's shim, tells it when asked.
func TestClaudeVersionAsked(t *testing.T) {
	env, _ := fakeEnv(t, "claude-ok", "PROBE_VERSION=2.1.230")
	shim := filepath.Join(env.HomeDir, ".volta", "bin", "claude")
	writeExe(t, shim)
	env.LookPath = func(string) (string, error) { return shim, nil }
	if got := env.claudeCandidates(context.Background()); len(got) != 1 || got[0].version != "2.1.230" {
		t.Errorf("got %q", got)
	}
}

// How Claude Code was installed is told by its path, or the one its links
// lead to.
func TestClaudeKind(t *testing.T) {
	for path, want := range map[string]string{
		"/opt/homebrew/bin/claude":                                                                           kindHomebrew,
		"/usr/local/Caskroom/claude-code/2.1.42/claude":                                                      kindHomebrew,
		"/opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js":                                    kindNPM,
		"/home/ann/.npm-global/bin/claude":                                                                   kindNPM,
		"/home/ann/.claude/local/claude":                                                                     kindNPM,
		"/home/ann/.local/share/claude/versions/2.1.210":                                                     kindNative,
		"/Applications/Claude.app/Contents/Resources/app/claude-code/2.1.220/claude":                         kindApp,
		"/Users/ann/Library/Application Support/Claude/claude-code/2.1.230/claude.app/Contents/MacOS/claude": kindApp,
		"/home/ann/.vscode/extensions/anthropic.claude-code-2.1.235/resources/native-binary/claude":          kindExtension,
		"/usr/bin/claude": "",
	} {
		if got := claudeKind(filepath.FromSlash(path)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}
