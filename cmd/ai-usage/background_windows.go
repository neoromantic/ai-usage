//go:build windows

package main

import (
	"io"
	"os"

	"charm.land/lipgloss/v2"
	xwindows "github.com/charmbracelet/x/windows"
	"golang.org/x/sys/windows"
)

// askBackground asks the console stdout is drawn on. Lip Gloss opens the
// console itself when standard input is not it, as in the installer's first
// run, and takes a console that does not answer to be dark. It drops the
// console's waiting input as it asks, so with a key typed ahead the console
// is not asked.
func askBackground(stdout io.Writer) (dark, ok bool) {
	out, ok := stdout.(*os.File)
	if !ok {
		return false, false
	}
	if _, tty := terminal(out); !tty {
		return false, false
	}
	if consoleTypedAhead() {
		return false, false
	}
	return lipgloss.HasDarkBackground(os.Stdin, out), true
}

// consoleTypedAhead says whether a key typed ahead waits in the console
// Lip Gloss would read: standard input when it is the console, else the
// console itself. A console whose input cannot be read counts as one.
func consoleTypedAhead() bool {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		f, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
		if err != nil {
			return true
		}
		defer f.Close()
		h = windows.Handle(f.Fd())
	}
	var n uint32
	if windows.GetNumberOfConsoleInputEvents(h, &n) != nil {
		return true
	}
	if n == 0 {
		return false
	}
	// Peeking leaves the events for whoever reads next.
	recs := make([]xwindows.InputRecord, n)
	var read uint32
	if xwindows.PeekConsoleInput(h, &recs[0], n, &read) != nil {
		return true
	}
	var keys []consoleKey
	for _, r := range recs[:min(read, n)] {
		if r.EventType == xwindows.KEY_EVENT {
			k := r.KeyEvent()
			keys = append(keys, consoleKey{down: k.KeyDown, vk: k.VirtualKeyCode})
		}
	}
	return typedAhead(keys)
}
