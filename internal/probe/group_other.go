//go:build !unix

package probe

import "os/exec"

// ownGroup leaves cmd as it is. Without process groups only the harness
// itself is killed, not what a wrapper script started.
func ownGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// ownedByUs is true: without Unix user ids, who owns a file is not checked.
func ownedByUs(string) bool { return true }
