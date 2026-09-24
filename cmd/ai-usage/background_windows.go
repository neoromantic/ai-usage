//go:build windows

package main

import (
	"io"
	"os"

	"charm.land/lipgloss/v2"
)

// askBackground asks the console stdout is drawn on. Lip Gloss opens the
// console itself when standard input is not it, as in the installer's first
// run, and takes a console that does not answer to be dark.
func askBackground(stdout io.Writer) (dark, ok bool) {
	out, ok := stdout.(*os.File)
	if !ok {
		return false, false
	}
	if _, tty := terminal(out); !tty {
		return false, false
	}
	return lipgloss.HasDarkBackground(os.Stdin, out), true
}
