//go:build darwin || linux

package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// TestQueryBackground asks a pseudo-terminal as the static report asks the
// terminal it prints on.
func TestQueryBackground(t *testing.T) {
	const wait = 500 * time.Millisecond
	light := "\x1b]11;rgb:ffff/ffff/ffff\x07\x1b[?62;22c"
	for _, c := range []struct {
		name, answer string
		// typed is what the person typed before the question, and then
		// after it.
		typed    [2]string
		dark, ok bool
	}{
		{"a light terminal", light, [2]string{}, false, true},
		{"a dark one that ends with ST", "\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\\x1b[?1;2c", [2]string{}, true, true},
		{"one that does not know OSC 11", "\x1b[?1;2c", [2]string{}, false, false},
		{"one that says nothing", "", [2]string{}, false, false},
		// Keys typed while the report collected are left for the shell.
		{"a line typed ahead", light, [2]string{"ls\n"}, false, false},
		{"part of one", light, [2]string{"l", "s\n"}, false, false},
	} {
		master, slave := openPTY(t)
		sent := answering(master, c.answer)
		fd := int(slave.Fd())
		if c.typed[0] != "" {
			if _, err := master.WriteString(c.typed[0]); err != nil {
				t.Fatal(err)
			}
			// The terminal passes on what is typed a moment later.
			time.Sleep(50 * time.Millisecond)
		}
		before, err := term.GetState(fd)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		dark, ok := queryBackground(fd, wait)
		took := time.Since(start)
		if dark != c.dark || ok != c.ok {
			t.Errorf("%s: dark %v, answered %v", c.name, dark, ok)
		}
		// One question, answered at once or given up on after the wait.
		if c.answer != "" && took >= wait || c.answer == "" && (took < wait || took > 2*wait) {
			t.Errorf("%s: took %v", c.name, took)
		}
		if after, err := term.GetState(fd); err != nil || !reflect.DeepEqual(after, before) {
			t.Errorf("%s: the terminal was left changed", c.name)
		}
		if c.typed[0] == "" {
			continue
		}
		if strings.Contains(sent(), ansi.RequestBackgroundColor) {
			t.Errorf("%s: asked over the typed keys", c.name)
		}
		if _, err := master.WriteString(c.typed[1]); err != nil {
			t.Fatal(err)
		}
		var line []byte
		buf := make([]byte, 64)
		for !bytes.HasSuffix(line, []byte("\n")) && readable(fd, time.Second) {
			n, err := slave.Read(buf)
			if err != nil {
				break
			}
			line = append(line, buf[:n]...)
		}
		if want := c.typed[0] + c.typed[1]; string(line) != want {
			t.Errorf("%s: the shell reads %q, want %q", c.name, line, want)
		}
	}
}
