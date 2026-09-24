package main

import (
	"bytes"
	"os"
	"testing"
	"unsafe"

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
	var name [128]byte
	err = control(m, func(fd int) error {
		if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
			return err
		}
		if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
			return err
		}
		if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); e != 0 {
			return e
		}
		return nil
	})
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	return m, ptySlave(t, m, string(name[:bytes.IndexByte(name[:], 0)]))
}
