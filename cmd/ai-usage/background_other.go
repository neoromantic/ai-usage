//go:build !darwin && !linux && !windows

package main

import "io"

// askBackground does not ask: only macOS, Linux, and Windows terminals are
// asked.
func askBackground(io.Writer) (dark, ok bool) { return false, false }
