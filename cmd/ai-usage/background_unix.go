//go:build darwin || linux

package main

import (
	"image/color"
	"io"
	"regexp"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// askWait is how long a terminal has to answer. One that does not know the
// question still answers the one after it at once.
const askWait = time.Second

var (
	// oscBackground is a terminal's answer to OSC 11, ended by BEL or ST.
	oscBackground = regexp.MustCompile(`\x1b\]11;([^\x07\x1b]*)(?:\x07|\x1b\\)`)
	// da1 is its answer to DA1, which every terminal gives.
	da1 = regexp.MustCompile(`\x1b\[\?[0-9;]*c`)
)

// askBackground asks through the controlling terminal, so that it is asked
// when standard input is not a terminal too, as in the installer's first
// run. Only a process in the terminal's foreground asks: one in the
// background would be stopped for setting it raw.
func askBackground(stdout io.Writer) (dark, ok bool) {
	if _, tty := terminal(stdout); !tty {
		return false, false
	}
	fd, err := unix.Open("/dev/tty", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, false
	}
	defer unix.Close(fd)
	if pg, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err != nil || pg != unix.Getpgrp() {
		return false, false
	}
	return queryBackground(fd, askWait)
}

// queryBackground sets the terminal raw, asks it for its background with
// OSC 11 and then DA1, and reads up to the answer to DA1, for at most wait.
// Keys typed ahead are left for whoever reads next: with any waiting, the
// terminal is not asked. A whole line shows before the terminal is set raw,
// which on macOS would hold it back until more is typed, and part of a line
// only after.
func queryBackground(fd int, wait time.Duration) (dark, ok bool) {
	if readable(fd, 0) {
		return false, false
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return false, false
	}
	defer term.Restore(fd, old)
	if readable(fd, 0) {
		return false, false
	}
	if _, err := unix.Write(fd, []byte(ansi.RequestBackgroundColor+ansi.RequestPrimaryDeviceAttributes)); err != nil {
		return false, false
	}
	deadline := time.Now().Add(wait)
	var got []byte
	buf := make([]byte, 256)
	for readable(fd, time.Until(deadline)) {
		n, err := unix.Read(fd, buf)
		if err != nil || n <= 0 {
			break
		}
		got = append(got, buf[:n]...)
		if end := da1.FindIndex(got); end != nil {
			return backgroundIn(got[:end[0]])
		}
	}
	return false, false
}

// backgroundIn is whether the background a terminal's answer names is dark,
// and whether it names one.
func backgroundIn(answer []byte) (dark, ok bool) {
	m := oscBackground.FindSubmatch(answer)
	if m == nil {
		return false, false
	}
	c := ansi.XParseColor(string(m[1]))
	if c == nil {
		return false, false
	}
	return isDark(c), true
}

// readable waits up to d for input on fd, and says whether there is some.
func readable(fd int, d time.Duration) bool {
	for {
		var in unix.FdSet
		in.Set(fd)
		tv := unix.NsecToTimeval(max(d, 0).Nanoseconds())
		n, err := unix.Select(fd+1, &in, nil, nil, &tv)
		if err != unix.EINTR {
			return err == nil && n > 0
		}
	}
}

// isDark says a color's lightness is under half, as Lip Gloss and Bubble
// Tea judge it.
func isDark(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return max(r, g, b)+min(r, g, b) < 0xffff
}
