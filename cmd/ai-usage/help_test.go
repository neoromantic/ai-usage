package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/view"
)

// TestHelpLayout: the help fits the width it is given, and color changes
// none of its text.
func TestHelpLayout(t *testing.T) {
	for _, width := range []int{80, 100, 160} {
		plain := ansi.Strip(helpText(view.Options{Width: width}))
		colored := helpText(view.Options{Width: width, Color: true})
		if ansi.Strip(colored) != plain || colored == plain {
			t.Errorf("width %d: color changes the text, or adds none", width)
		}
		for l := range strings.SplitSeq(plain, "\n") {
			if ansi.StringWidth(l) > min(width, helpMax) {
				t.Errorf("width %d: %q is wider", width, l)
			}
		}
	}
}

func TestVersionHelpAndUsageErrors(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	for _, args := range [][]string{{"version"}, {"-version"}, {"--version"}} {
		if out := d.ok(args...); out != "dev\n" {
			t.Fatalf("%v printed %q", args, out)
		}
	}
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {"report", "-h"}, {"collect", "--help"}} {
		if out := d.ok(args...); out != plainHelp() {
			t.Fatalf("%v printed %q", args, out)
		}
	}
	for _, args := range [][]string{
		{"bogus"},
		{"collect", "--bogus"},
		{"collect", "extra"},
		{"report", "extra"},
		{"team", "bogus"},
		{"team", "key", "extra"},
		{"team", "join", "a", "b"},
		{"team", "forget-device"},
		{"team", "forget-device", "../other-team"},
		{"relay", "bogus"},
		{"relay", "set"},
		{"schedule"},
		{"schedule", "bogus"},
	} {
		r := d.run("", args...)
		if r.code != 2 || !helpShown(r.stderr) {
			t.Fatalf("%v: exit %d, stderr %q", args, r.code, r.stderr)
		}
	}
	if r := d.run("", "bogus"); !strings.Contains(r.stderr, "bogus") {
		t.Fatalf("stderr = %q", r.stderr)
	}
}

// plainHelp is the help as it is printed out of a terminal: with no
// escapes, and 80 columns wide.
func plainHelp() string { return ansi.Strip(helpText(view.Options{Width: 80})) }

// helpShown is whether out ends with the help, after an error.
func helpShown(out string) bool { return strings.HasSuffix(out, "\n\n"+plainHelp()) }

// TestUsageSections: each [SECTION] in a command is a section of the help,
// and the command takes every flag listed in that section.
func TestUsageSections(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	sections := map[string][]string{}
	flagLine := regexp.MustCompile(`^(--[a-z-]+)(=[a-z]+| [A-Z]+)?`)
	for _, s := range help {
		for _, l := range s.lines {
			if m := flagLine.FindStringSubmatch(l.what); m != nil {
				// A flag with a value gets one it takes: the first choice, or 1.
				f := m[1] + m[2]
				if strings.HasPrefix(m[2], " ") {
					f = m[1] + "=1"
				}
				sections[s.title] = append(sections[s.title], f)
			}
		}
	}
	word := regexp.MustCompile(`^[a-z-]+$`)
	checked := 0
	for _, s := range help {
		for _, l := range s.lines {
			rest, ok := strings.CutPrefix(l.what, "ai-usage")
			if !ok {
				continue
			}
			var cmd []string
			for f := range strings.FieldsSeq(rest) {
				if !word.MatchString(f) {
					break
				}
				cmd = append(cmd, f)
			}
			for _, m := range regexp.MustCompile(`\[([A-Z]+)\]`).FindAllStringSubmatch(rest, -1) {
				flags, ok := sections[m[1]]
				// KEY is the argument its line explains.
				if !ok && m[1] != "KEY" {
					t.Errorf("%q: no section says what %s is", l.what, m[0])
				}
				for _, f := range flags {
					// -h stops the command once its flags are read.
					if r := d.run("", append(slices.Clone(cmd), f, "-h")...); r.code != 0 {
						first, _, _ := strings.Cut(r.stderr, "\n")
						t.Errorf("ai-usage %s: %s", strings.Join(append(cmd, f), " "), first)
					}
					checked++
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no synopsis names a section of flags")
	}
}
