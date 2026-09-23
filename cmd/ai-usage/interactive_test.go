package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenView(t *testing.T) {
	for _, c := range []struct {
		name                     string
		in, out, json, plain, gd bool
		term                     string
		want                     bool
	}{
		{"both ends a terminal", true, true, false, false, false, "xterm-256color", true},
		{"no TERM, as on Windows", true, true, false, false, false, "", true},
		{"piped input, as the installer runs it", false, true, false, false, false, "xterm-256color", false},
		{"piped output", true, false, false, false, false, "xterm-256color", false},
		{"--json", true, true, true, false, false, "xterm-256color", false},
		{"--plain", true, true, false, true, false, "xterm-256color", false},
		{"the first run, with the guide", true, true, false, false, true, "xterm-256color", false},
		{"a dumb terminal", true, true, false, false, false, "dumb", false},
	} {
		if got := openView(c.in, c.out, c.json, c.plain, c.gd, c.term); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestReportStaysStatic: a terminal on standard input alone prints the
// report, and --plain is a display flag of every command that draws.
func TestReportStaysStatic(t *testing.T) {
	hermetic(t)
	saved := stdinTTY
	t.Cleanup(func() { stdinTTY = saved })
	stdinTTY = func() bool { return true }

	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/api", 1)
	for _, args := range [][]string{
		{"collect", "--offline"},
		{"collect", "--offline", "--plain"},
		{"--offline", "--plain", "--width", "120"},
		{"report"},
		{"report", "--plain", "--color=never"},
	} {
		if out := d.ok(args...); !strings.HasPrefix(out, "ai-usage") {
			t.Fatalf("%v printed %q", args, out)
		}
	}
	if out := d.ok("status", "--plain"); !strings.Contains(out, "ai-usage") {
		t.Fatalf("status --plain printed %q", out)
	}
	if out := d.ok("help"); !strings.Contains(out, "--plain") {
		t.Fatal("usage does not list --plain")
	}

	// A file is not a terminal either.
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if (&display{}).interactive(f, false, false) {
		t.Fatal("a file opened the interactive view")
	}
}
