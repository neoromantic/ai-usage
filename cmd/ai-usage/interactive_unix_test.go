//go:build darwin || linux

package main

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/tui"
)

// sgr is a Select Graphic Rendition sequence, and its parameters.
var sgr = regexp.MustCompile(`\x1b\[([0-9;:]*)m`)

// colored says whether out sets a foreground or background color.
func colored(out string) bool {
	for _, m := range sgr.FindAllStringSubmatch(out, -1) {
		for _, p := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ';' || r == ':' }) {
			n, _ := strconv.Atoi(p)
			if n >= 30 && n <= 38 || n >= 40 && n <= 48 || n >= 90 && n <= 97 || n >= 100 && n <= 107 {
				return true
			}
		}
	}
	return false
}

// TestViewColor opens the interactive view on a pseudo-terminal: --color
// and NO_COLOR color it as they color the static report.
func TestViewColor(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	res, err := loadResult(state.Dir(d.dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERM", "xterm-256color")
	for _, c := range []struct {
		noColor, flag string
		want          bool
	}{
		{"", "auto", true},
		{"1", "always", true},
		{"1", "auto", false},
		{"", "never", false},
	} {
		t.Setenv("NO_COLOR", c.noColor)
		master, slave := openPTY(t)
		sent := answering(master, "")
		cfg := viewConfig(state.Dir(d.dir), res, "", &display{color: c.flag}, true, slave)
		done := make(chan error, 1)
		go func() { done <- tui.Run(context.Background(), cfg, slave, slave) }()
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(sent(), "ai-usage") && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := master.WriteString("q"); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("NO_COLOR=%q --color=%s: the view did not quit", c.noColor, c.flag)
		}
		out := sent()
		if !strings.Contains(out, "ai-usage") {
			t.Fatalf("NO_COLOR=%q --color=%s: the view drew nothing:\n%q", c.noColor, c.flag, out)
		}
		if colored(out) != c.want {
			t.Errorf("NO_COLOR=%q --color=%s: colored %v", c.noColor, c.flag, !c.want)
		}
	}
}
