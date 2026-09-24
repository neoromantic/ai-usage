package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/neoromantic/ai-usage/internal/state"
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

// TestViewConfig: the interactive view does not ask the terminal for its
// background before it opens; Bubble Tea asks once it reads the keys too.
func TestViewConfig(t *testing.T) {
	hermetic(t)
	saved := background
	t.Cleanup(func() { background = saved })
	background = func(io.Writer) (bool, bool) {
		t.Error("the view asked the terminal before it opened")
		return true, true
	}
	d := newDevice(t)
	d.ok("collect", "--quiet", "--offline")
	res, err := loadResult(state.Dir(d.dir))
	if err != nil {
		t.Fatal(err)
	}
	if c := viewConfig(state.Dir(d.dir), res, "", &display{color: "always"}, true, io.Discard); !c.Options.Color || !c.Options.Dark {
		t.Fatalf("the view starts with %+v", c.Options)
	}

	// The view is written with the escapes the static report would be.
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TTY_FORCE", "1")
	t.Setenv("TERM", "xterm-256color")
	for flag, want := range map[string]colorprofile.Profile{
		"always": colorprofile.ANSI256,
		"auto":   colorprofile.ASCII,
		"never":  colorprofile.NoTTY,
	} {
		c := viewConfig(state.Dir(d.dir), res, "", &display{color: flag}, true, io.Discard)
		if c.Profile != want || c.Options.Color != (want >= colorprofile.ANSI) {
			t.Errorf("--color=%s with NO_COLOR: profile %v, color %v", flag, c.Profile, c.Options.Color)
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
