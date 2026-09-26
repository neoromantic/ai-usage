//go:build darwin || linux

package main

import (
	"bytes"
	"os"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"
)

// control runs do on the descriptor of f without making it blocking, as Fd
// would, so that closing f still ends a read of it.
func control(f *os.File, do func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var derr error
	if err := rc.Control(func(fd uintptr) { derr = do(int(fd)) }); err != nil {
		return err
	}
	return derr
}

// openPTY is a new pseudo-terminal, 120 by 40: master is the terminal's end,
// and slave the program's. Both ends close when the test ends.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var name string
	err = control(m, func(fd int) (err error) { name, err = ptsName(fd); return })
	var s *os.File
	if err == nil {
		s, err = os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	}
	if err == nil {
		err = control(s, func(fd int) error {
			return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120})
		})
	}
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		m.Close()
	})
	return m, s
}

// answering is the terminal of master: it answers DA1, the last question
// the program asks, with answer, and sent is what it has been sent so far.
func answering(master *os.File, answer string) (sent func() string) {
	var mu sync.Mutex
	var got []byte
	go func() {
		buf := make([]byte, 4096)
		asked := false
		for {
			n, err := master.Read(buf)
			if err != nil {
				return
			}
			mu.Lock()
			got = append(got, buf[:n]...)
			ask := !asked && answer != "" && bytes.Contains(got, []byte(ansi.RequestPrimaryDeviceAttributes))
			mu.Unlock()
			if ask {
				asked = true
				_, _ = master.WriteString(answer)
			}
		}
	}()
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return string(got)
	}
}
