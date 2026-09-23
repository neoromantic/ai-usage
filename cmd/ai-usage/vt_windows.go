//go:build windows

package main

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// enableVT turns on escape sequences for a console stdout, and says whether
// color can be written. Output that is not a console takes it as it is.
func enableVT(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return true
	}
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return true
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
