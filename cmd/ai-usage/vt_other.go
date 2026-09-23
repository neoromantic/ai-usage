//go:build !windows

package main

import "io"

// enableVT says whether color can be written; every terminal here takes it.
func enableVT(io.Writer) bool { return true }
