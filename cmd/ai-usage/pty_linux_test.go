package main

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// openPTY is a new pseudo-terminal: master is the terminal's end, and slave
// the program's.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var n uint32
	err = control(m, func(fd int) (err error) {
		if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
			return err
		}
		n, err = unix.IoctlGetUint32(fd, unix.TIOCGPTN)
		return err
	})
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	return m, ptySlave(t, m, fmt.Sprintf("/dev/pts/%d", n))
}
