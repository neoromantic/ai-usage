package probe

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func writeExe(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func notOnPath(string) (string, error) { return "", exec.ErrNotFound }

func TestFindUsesPathFirst(t *testing.T) {
	home := t.TempDir()
	writeExe(t, filepath.Join(home, ".local", "bin", exeName("claude")))
	env := Env{HomeDir: home, LookPath: func(n string) (string, error) { return "/on/path/" + n, nil }}
	if p, ok := env.find("claude"); !ok || p != "/on/path/claude" {
		t.Errorf("find = %q, %v", p, ok)
	}
}

// A harness in its own ~/.<name>/bin is found when PATH lacks it. TestBins
// covers ~/.local/bin.
func TestFindFallbackDirs(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(home, ".codex", "bin", exeName("codex"))
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
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin", exeName("claude")), 0o755); err != nil {
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
	onPath := filepath.Join(home, ".local", "bin", exeName("codex"))
	writeExe(t, onPath)
	env := Env{HomeDir: home, LookPath: notOnPath}
	old := bundle(t, home, filepath.Join(".cursor", "extensions", "openai.chatgpt-0.4.1", "bin", "linux-x86_64", exeName("codex")), testNow.Add(-72*time.Hour))
	newer := bundle(t, home, filepath.Join(".cursor", "extensions", "openai.chatgpt-0.5.0", "bin", "linux-x86_64", exeName("codex")), testNow)
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
	writeExe(t, filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-26.5.1", "bin", "x", exeName("claude")))
	if got := env.bins("claude"); got != nil {
		t.Errorf("claude bins = %q", got)
	}
}
