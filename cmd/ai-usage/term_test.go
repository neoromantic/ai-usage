package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/colorprofile"

	"github.com/neoromantic/ai-usage/internal/view"
)

func firstLineWidth(s string) int {
	first, _, _ := strings.Cut(s, "\n")
	return utf8.RuneCountInString(first)
}

// widest is how wide the widest line of s is.
func widest(s string) int {
	w := 0
	for l := range strings.SplitSeq(s, "\n") {
		w = max(w, utf8.RuneCountInString(l))
	}
	return w
}

func TestDisplayFlags(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")

	// A pipe gets no escapes unless asked, and NO_COLOR does not beat asking.
	if out := d.ok("report"); strings.Contains(out, "\x1b") || widest(out) > 79 || strings.Contains(out, "  PLAN  ") {
		t.Fatalf("default report:\n%s", out)
	}
	if out := d.ok("report", "--color=always"); !strings.HasPrefix(out, "\x1b[1mai-usage\x1b[m") || !regexp.MustCompile(`\x1b\[91m\d+ over\x1b\[m`).MatchString(out) {
		t.Fatalf("--color=always:\n%q", out)
	}
	t.Setenv("NO_COLOR", "1")
	if out := d.ok("report", "--color", "always"); !strings.Contains(out, "\x1b[91m") {
		t.Fatal("NO_COLOR turned off --color=always")
	}
	// On a terminal NO_COLOR keeps bold and reverse video, which are not
	// colors.
	t.Setenv("TTY_FORCE", "1")
	t.Setenv("TERM", "xterm-256color")
	if out := d.ok("report"); !strings.HasPrefix(out, "\x1b[1mai-usage\x1b[m") || regexp.MustCompile(`\x1b\[[0-9;]*[349][0-9]`).MatchString(out) {
		t.Fatalf("NO_COLOR on a terminal:\n%q", out)
	}
	t.Setenv("NO_COLOR", "")
	if out := d.ok("report", "--color=never"); strings.Contains(out, "\x1b") {
		t.Fatal("--color=never printed escapes")
	}
	t.Setenv("TTY_FORCE", "")
	t.Setenv("TERM", "")

	// The width is the flag, else COLUMNS, else 80, and it is clamped. The
	// report is as wide as its widest table; status spans the width.
	if out := d.ok("report", "--width", "120"); widest(out) > 119 || !strings.Contains(out, "  PLAN  ") {
		t.Fatalf("--width 120:\n%s", out)
	}
	if w := firstLineWidth(d.ok("status", "--width", "120")); w != 119 {
		t.Fatalf("--width 120: header is %d wide", w)
	}
	t.Setenv("COLUMNS", "100")
	if w := firstLineWidth(d.ok("status")); w != 99 {
		t.Fatalf("COLUMNS=100: header is %d wide", w)
	}
	if w := firstLineWidth(d.ok("status", "--width", "90")); w != 89 {
		t.Fatalf("--width beats COLUMNS: header is %d wide", w)
	}
	if w := firstLineWidth(d.ok("status", "--width", "500")); w != 159 {
		t.Fatalf("--width 500: header is %d wide", w)
	}
	t.Setenv("COLUMNS", "")

	// ASCII by flag, or by a locale without UTF-8.
	isASCII := func(s string) bool {
		for i := 0; i < len(s); i++ {
			if s[i] >= 0x80 {
				return false
			}
		}
		return true
	}
	if out := d.ok("report", "--ascii"); !isASCII(out) || !strings.Contains(out, "\n* dev@example.com  ===") {
		t.Fatalf("--ascii:\n%s", out)
	}
	t.Setenv("LC_ALL", "C")
	if out := d.ok("report"); !isASCII(out) {
		t.Fatalf("LC_ALL=C:\n%s", out)
	}
	t.Setenv("LC_ALL", "en_US.UTF-8")

	// --json ignores the display flags.
	var r view.Report
	if err := json.Unmarshal([]byte(d.ok("report", "--json", "--color=always", "--projects", "--devices", "--width", "120")), &r); err != nil || r.SchemaVersion != view.SchemaVersion {
		t.Fatalf("--json with display flags: %v", err)
	}
	// A team of one device has USAGE, not the two views of DEVICES, so
	// --devices changes nothing; it goes with --projects, and a quiet
	// collection prints nothing with it.
	if a, b := d.ok("report"), d.ok("report", "--devices", "--projects"); a != b || !strings.Contains(b, "\nUSAGE  ") {
		t.Fatalf("--devices on one device:\n%s\nwithout:\n%s", b, a)
	}
	if out := d.ok("collect", "--quiet", "--offline", "--devices"); out != "" {
		t.Fatalf("collect --quiet --devices printed %q", out)
	}

	st := d.ok("status", "--color=always", "--ascii", "--width", "100")
	if !strings.HasPrefix(st, "\x1b[1mai-usage status\x1b[m ") || !isASCII(st) {
		t.Fatalf("status with display flags:\n%q", st)
	}
}

func TestDisplayFlagErrors(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	for _, args := range [][]string{
		{"report", "--projects", "--bogus"},
		{"--offline", "--color=sometimes"},
		{"report", "--color=sometimes"},
		{"report", "--width", "-3"},
		{"status", "--projects"},
		// status draws no DEVICES.
		{"status", "--devices"},
		{"status", "--json", "--devices"},
		{"report", "--devices=sometimes"},
	} {
		r := d.run("", args...)
		if r.code != 2 || !helpShown(r.stderr) {
			t.Fatalf("%v: exit %d, stderr %q", args, r.code, r.stderr)
		}
	}
	// Bad flags are refused before anything is collected.
	if _, err := os.Stat(d.dir); !os.IsNotExist(err) {
		t.Fatalf("a refused run wrote to %s: %v", d.dir, err)
	}
}

// The first locale variable set decides, however it spells UTF-8.
func TestTerminalDetection(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("COLUMNS", "")
	if w := termWidth(0, &buf); w != 80 {
		t.Fatalf("default width %d", w)
	}
	t.Setenv("COLUMNS", "132")
	if w := termWidth(0, &buf); w != 132 {
		t.Fatalf("COLUMNS width %d", w)
	}
	t.Setenv("COLUMNS", "wide")
	if w := termWidth(0, &buf); w != 80 {
		t.Fatalf("bad COLUMNS width %d", w)
	}
	if w := termWidth(100, &buf); w != 100 {
		t.Fatalf("flag width %d", w)
	}

	for _, k := range []string{"NO_COLOR", "COLORTERM", "CLICOLOR", "CLICOLOR_FORCE", "TTY_FORCE", "COLORFGBG"} {
		t.Setenv(k, "")
	}
	t.Setenv("TERM", "xterm-256color")
	profiles := func(auto, always, never colorprofile.Profile) {
		t.Helper()
		for mode, want := range map[string]colorprofile.Profile{"auto": auto, "always": always, "never": never} {
			if got := (&display{color: mode}).profile(&buf); got != want {
				t.Errorf("NO_COLOR=%q TTY_FORCE=%q --color=%s: %v, want %v", os.Getenv("NO_COLOR"), os.Getenv("TTY_FORCE"), mode, got, want)
			}
		}
	}
	// A pipe takes no escapes unless asked.
	profiles(colorprofile.NoTTY, colorprofile.ANSI256, colorprofile.NoTTY)
	// A terminal takes color; with NO_COLOR, bold and reverse video only.
	t.Setenv("TTY_FORCE", "1")
	profiles(colorprofile.ANSI256, colorprofile.ANSI256, colorprofile.NoTTY)
	t.Setenv("NO_COLOR", "1")
	profiles(colorprofile.ASCII, colorprofile.ANSI256, colorprofile.NoTTY)
	// Any value counts, as no-color.org has it.
	t.Setenv("NO_COLOR", "yes")
	profiles(colorprofile.ASCII, colorprofile.ANSI256, colorprofile.NoTTY)
	// A dumb terminal takes nothing unless asked.
	t.Setenv("TERM", "dumb")
	profiles(colorprofile.NoTTY, colorprofile.ANSI, colorprofile.NoTTY)
	t.Setenv("NO_COLOR", "")
	profiles(colorprofile.NoTTY, colorprofile.ANSI, colorprofile.NoTTY)
	t.Setenv("TTY_FORCE", "")
	if o := (&display{color: "always"}).options(&buf, true); !o.Color || !o.Dark {
		t.Errorf("--color=always draws %+v", o)
	}

	for _, c := range []struct {
		all, ctype, lang string
		utf8             bool
	}{
		{"", "", "en_US.utf8", true},
		{"C", "", "en_US.UTF-8", false},
		{"", "UTF-8", "", true},
		{"", "", "", false},
	} {
		t.Setenv("LC_ALL", c.all)
		t.Setenv("LC_CTYPE", c.ctype)
		t.Setenv("LANG", c.lang)
		t.Setenv("WT_SESSION", "")
		t.Setenv("TERM_PROGRAM", "")
		if got := utf8Locale(); got != c.utf8 {
			t.Errorf("LC_ALL=%q LC_CTYPE=%q LANG=%q: utf8 = %v", c.all, c.ctype, c.lang, got)
		}
	}
}

// TestBackground: the background is what COLORFGBG says, else what the
// terminal answers where it may be asked, else dark.
func TestBackground(t *testing.T) {
	saved := background
	t.Cleanup(func() { background = saved })
	var buf bytes.Buffer
	for _, c := range []struct {
		fgbg string
		// answer is the terminal's: dark, light, or nothing.
		answer           string
		ask, asked, dark bool
	}{
		{"", "light", true, true, false},
		{"", "", true, true, true},
		// The interactive view does not ask before it opens.
		{"", "light", false, false, true},
		{"0;15", "dark", true, false, false},
		{"15;0", "light", true, false, true},
		{"0;default;15", "dark", true, false, false},
		{"7;8", "light", true, false, true},
		{"15;default", "light", true, true, false},
	} {
		t.Setenv("COLORFGBG", c.fgbg)
		asked := false
		background = func(io.Writer) (bool, bool) {
			asked = true
			return c.answer == "dark", c.answer != ""
		}
		if o := (&display{color: "always"}).options(&buf, c.ask); o.Dark != c.dark || asked != c.asked {
			t.Errorf("COLORFGBG=%q, answer %q, ask %v: dark %v, asked %v", c.fgbg, c.answer, c.ask, o.Dark, asked)
		}
	}
}
