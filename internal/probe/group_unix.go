//go:build unix

package probe

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup starts cmd as the leader of a new process group and makes its
// context kill the whole group. A harness started through a wrapper script
// would otherwise keep running after the wrapper is killed.
func ownGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	// Only a command made with a context has a Cancel to replace.
	if cmd.Cancel != nil {
		cmd.Cancel = func() error { return killGroup(cmd) }
	}
}

// killGroup kills the process group ownGroup started cmd in.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// ownedByUs reports whether this process's effective user owns path. A path
// it cannot stat is not.
func ownedByUs(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
