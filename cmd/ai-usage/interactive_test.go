package main

import (
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/tui"
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

// TestViewConfig: the interactive view starts on the background COLORFGBG
// says, else dark, and does not ask the terminal before it opens; Bubble
// Tea asks once it reads the keys too, and the answer then decides.
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
	t.Setenv("COLORFGBG", "0;15")
	if c := viewConfig(state.Dir(d.dir), res, "", &display{color: "always"}, true, io.Discard); c.Options.Dark {
		t.Fatalf("with a light COLORFGBG, the view starts with %+v", c.Options)
	}
	// --devices opens it on the status view of DEVICES; without, the matrix.
	fs := flags("report")
	disp := displayFlags(fs, true)
	if err := parse(fs, []string{"--devices", "--projects"}); err != nil {
		t.Fatal(err)
	}
	if c := viewConfig(state.Dir(d.dir), res, "", disp, true, io.Discard); !c.Options.DeviceStatus || !c.Options.AllProjects {
		t.Fatalf("--devices --projects: the view starts with %+v", c.Options)
	}
	if c := viewConfig(state.Dir(d.dir), res, "", &display{}, true, io.Discard); c.Options.DeviceStatus {
		t.Fatalf("without --devices, the view starts with %+v", c.Options)
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

// TestQuitDuringRefresh: closing the view while the collection `r` started
// still runs stops that collection, and it saves nothing: not the probes
// the stop made fail, not a sample, and not a release check.
func TestQuitDuringRefresh(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	// New usage gives the stopped collection a sample to write.
	d.claude("33333333-cccc", "/work/web", 2)
	before := tree(t, d.dir)
	res, err := loadResult(state.Dir(d.dir))
	if err != nil {
		t.Fatal(err)
	}

	// A release build checks for a release after it collects, which
	// hermetic fails the test for.
	version = "v9.9.9"
	started := make(chan struct{})
	var once sync.Once
	probeEnv = func() probe.Env {
		e := fakeProbeEnv()
		// Each harness answers only once the collection is stopped.
		e.Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return exec.CommandContext(ctx, name, args...)
		}
		return e
	}

	in, keys := io.Pipe()
	t.Cleanup(func() { keys.Close() })
	done := make(chan error, 1)
	go func() {
		done <- tui.Run(context.Background(), viewConfig(state.Dir(d.dir), res, "", &display{}, true, io.Discard), in, io.Discard)
	}()
	if _, err := io.WriteString(keys, "r"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("r did not collect")
	}
	if _, err := io.WriteString(keys, "q"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the view did not close")
	}
	after := tree(t, d.dir)
	for name, body := range after {
		if before[name] != body {
			t.Errorf("the stopped collection wrote %s:\n%s", name, body)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			t.Errorf("the stopped collection removed %s", name)
		}
	}
}

// tree is every file under dir, by its path in dir.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
