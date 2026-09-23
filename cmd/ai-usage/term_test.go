package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neoromantic/ai-usage/internal/view"
)

func firstLineWidth(s string) int {
	first, _, _ := strings.Cut(s, "\n")
	return utf8.RuneCountInString(first)
}

func TestDisplayFlags(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")

	// A pipe gets no color unless asked, and NO_COLOR does not beat asking.
	if out := d.ok("report"); strings.Contains(out, "\x1b") || firstLineWidth(out) != 79 {
		t.Fatalf("default report:\n%s", out)
	}
	if out := d.ok("report", "--color=always"); !strings.HasPrefix(out, "\x1b[1mai-usage\x1b[0m ") {
		t.Fatalf("--color=always:\n%q", out)
	}
	t.Setenv("NO_COLOR", "1")
	if out := d.ok("report", "--color", "always"); !strings.Contains(out, "\x1b[") {
		t.Fatal("NO_COLOR turned off --color=always")
	}
	t.Setenv("NO_COLOR", "")
	if out := d.ok("report", "--color=never"); strings.Contains(out, "\x1b") {
		t.Fatal("--color=never printed color")
	}

	// The width is the flag, else COLUMNS, else 80, and it is clamped.
	// Status shows it: a one-device report stops at its tables when wide.
	if w := firstLineWidth(d.ok("status", "--width", "120")); w != 119 {
		t.Fatalf("--width 120: header is %d wide", w)
	}
	if w := firstLineWidth(d.ok("report", "--width", "120")); w != 107 {
		t.Fatalf("--width 120: one-device report header is %d wide", w)
	}
	t.Setenv("COLUMNS", "100")
	if w := firstLineWidth(d.ok("report")); w != 99 {
		t.Fatalf("COLUMNS=100: header is %d wide", w)
	}
	if w := firstLineWidth(d.ok("report", "--width", "90")); w != 89 {
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
	if out := d.ok("report", "--ascii"); !isASCII(out) || !strings.Contains(out, "\n* dev@example.com") {
		t.Fatalf("--ascii:\n%s", out)
	}
	t.Setenv("LC_ALL", "C")
	if out := d.ok("report"); !isASCII(out) {
		t.Fatalf("LC_ALL=C:\n%s", out)
	}
	t.Setenv("LC_ALL", "en_US.UTF-8")

	for view, want := range map[string]string{"--projects": "\nPROJECTS  ", "--tokens": "\nTEAM TOKENS  ", "--devices": "\nDEVICES  1 "} {
		if out := d.ok("report", view); !strings.Contains(out, want) {
			t.Fatalf("%s lacks %q:\n%s", view, want, out)
		}
	}
	// --json ignores the display flags.
	var r view.Report
	if err := json.Unmarshal([]byte(d.ok("report", "--json", "--color=always", "--tokens", "--width", "120")), &r); err != nil || r.SchemaVersion != 2 {
		t.Fatalf("--json with display flags: %v", err)
	}

	st := d.ok("status", "--color=always", "--ascii", "--width", "100")
	if !strings.HasPrefix(st, "\x1b[1mai-usage status\x1b[0m ") || !isASCII(st) {
		t.Fatalf("status with display flags:\n%q", st)
	}
	if w := firstLineWidth(d.ok("status", "--width", "100")); w != 99 {
		t.Fatalf("status --width 100: header is %d wide", w)
	}
}

func TestDisplayFlagErrors(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	for _, args := range [][]string{
		{"report", "--projects", "--tokens"},
		{"--offline", "--devices", "--projects"},
		{"collect", "--tokens", "--devices"},
		{"report", "--color=sometimes"},
		{"report", "--width", "-3"},
		{"status", "--projects"},
	} {
		r := d.run("", args...)
		if r.code != 2 || !strings.Contains(r.stderr, "Usage:") {
			t.Fatalf("%v: exit %d, stderr %q", args, r.code, r.stderr)
		}
	}
	// Two views are refused before anything is collected.
	if _, err := os.Stat(d.dir); !os.IsNotExist(err) {
		t.Fatalf("a refused run wrote to %s: %v", d.dir, err)
	}
}

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

	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	if useColor("auto", &buf) || !useColor("always", &buf) || useColor("never", &buf) {
		t.Fatal("color on a buffer")
	}

	for _, c := range []struct {
		all, ctype, lang string
		utf8             bool
	}{
		{"", "", "en_US.UTF-8", true},
		{"", "", "en_US.utf8", true},
		{"C", "", "en_US.UTF-8", false},
		{"", "UTF-8", "", true},
		{"", "", "", false},
		{"", "", "POSIX", false},
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
