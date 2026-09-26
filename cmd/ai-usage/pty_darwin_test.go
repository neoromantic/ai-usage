package main

import (
	"bytes"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ptsName grants and unlocks the program's end of the pseudo-terminal whose
// master is fd, and returns its name.
func ptsName(fd int) (string, error) {
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return "", err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return "", err
	}
	var name [128]byte
	//lint:ignore SA1019 no libSystem wrapper for TIOCPTYGNAME
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		return "", e
	}
	return string(name[:bytes.IndexByte(name[:], 0)]), nil
}
