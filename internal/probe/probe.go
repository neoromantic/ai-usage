// Package probe asks an installed harness who is logged in and how full its
// quota windows are.
//
// It runs only one-shot commands that print identity or quota, and reads only
// files where the harness already cached that answer. It never opens a
// credential file, never starts a conversation, and never runs a login,
// logout, or token-refresh command.
package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Reading is what a harness reported. An empty Account means it did not say.
// A nil Quota means there is no reading; nothing is invented.
type Reading struct {
	Account string
	Plan    string
	Quota   *Quota
}

// Quota is a set of windows observed at one moment.
type Quota struct {
	At      time.Time
	Source  string // "harness", "cache", or "log"
	Windows []snapshot.Window
}

// notLoggedIn is the error of a harness that answered that nobody is logged
// in. It is not used for a harness that did not answer, whether it timed out,
// is missing, or printed something unreadable. collect tells the two apart
// through LoggedOut: the first ends the current login, the second keeps it.
type notLoggedIn string

func (e notLoggedIn) Error() string { return string(e) + ": not logged in" }
func (notLoggedIn) LoggedOut() bool { return true }

// joinErrors joins a probe's problems into one error whose message is the
// messages in order, separated by "; ", and whose chain keeps each of them,
// so errors.As still finds a notLoggedIn among them.
func joinErrors(errs []error) error {
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	}
	return joinedErrors(errs)
}

type joinedErrors []error

func (j joinedErrors) Error() string {
	msgs := make([]string, len(j))
	for i, err := range j {
		msgs[i] = err.Error()
	}
	return strings.Join(msgs, "; ")
}

func (j joinedErrors) Unwrap() []error { return j }

// Env is how probes reach the outside world. Tests replace it.
type Env struct {
	// Command builds a command. It defaults to exec.CommandContext.
	Command func(ctx context.Context, name string, args ...string) *exec.Cmd
	// LookPath finds a harness binary.
	LookPath func(string) (string, error)
	// Environ is the environment passed to harness commands.
	Environ []string
	// HomeDir is the OS user's home, used to tell a default harness home.
	HomeDir string
	// SystemBinDirs are tried after PATH and the per-user install places.
	// DefaultEnv fills them; a test's Env leaves them empty so a harness
	// installed on the machine running it stays out of reach.
	SystemBinDirs []string
	Now           func() time.Time
	Timeout       time.Duration
}

// DefaultEnv is the real environment.
func DefaultEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{
		Command:       exec.CommandContext,
		LookPath:      exec.LookPath,
		Environ:       os.Environ(),
		HomeDir:       home,
		SystemBinDirs: systemBinDirs(),
		Now:           time.Now,
		Timeout:       20 * time.Second,
	}
}

func (e Env) now() time.Time {
	if e.Now == nil {
		return time.Now().UTC()
	}
	return e.Now().UTC()
}

// command builds a harness command. It starts in the user's home, as it does
// under the system scheduler, because Claude Code applies the settings of the
// project it starts in, and those can point it at another provider and so
// another account. It runs in a process group of its own, so that a timeout
// also stops whatever a wrapper script started.
func (e Env) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if e.Command == nil {
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		cmd = e.Command(ctx, name, args...)
	}
	cmd.Dir = e.HomeDir
	ownGroup(cmd)
	return cmd
}

func (e Env) timeout() time.Duration {
	if e.Timeout <= 0 {
		return 20 * time.Second
	}
	return e.Timeout
}

// getenv is the last value Environ gives key, or this process's value when
// Environ is nil.
func (e Env) getenv(key string) string {
	base := e.Environ
	if base == nil {
		base = os.Environ()
	}
	value := ""
	for _, kv := range base {
		if name, v, ok := strings.Cut(kv, "="); ok && sameEnvName(name, key) {
			value = v
		}
	}
	return value
}

// Getenv is the value harness commands get for key: the last one Environ
// gives it, or this process's value when Environ is nil.
func (e Env) Getenv(key string) string { return e.getenv(key) }

// WithEnv returns a copy of e whose harness commands get key set to value,
// or not set at all when value is empty.
func (e Env) WithEnv(key, value string) Env {
	e.Environ = e.harnessEnv(key, value)
	return e
}

// harnessEnv returns Environ with key removed, then set to value when value
// is not empty. A nil Environ means this process's environment, as it does for
// exec.Cmd.
func (e Env) harnessEnv(key, value string) []string {
	base := e.Environ
	if base == nil {
		base = os.Environ()
	}
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if name, _, ok := strings.Cut(kv, "="); ok && sameEnvName(name, key) {
			continue
		}
		out = append(out, kv)
	}
	if value != "" {
		out = append(out, key+"="+value)
	}
	return out
}

// sameEnvName compares variable names the way the OS does. Windows ignores
// case, so a stale Claude_Config_Dir would otherwise survive.
func sameEnvName(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// find locates a harness binary. PATH is tried first, then the usual install
// places, because the system scheduler runs with a short PATH.
func (e Env) find(name string) (string, bool) {
	look := e.LookPath
	if look == nil {
		look = exec.LookPath
	}
	if p, err := look(name); err == nil {
		return p, true
	}
	for _, dir := range append(extraBinDirs(e.HomeDir, name), e.SystemBinDirs...) {
		for _, file := range binNames(name) {
			p := filepath.Join(dir, file)
			if isExecutable(p) {
				return p, true
			}
		}
	}
	return "", false
}

// systemBinDirs are the shared install places the system scheduler's short
// PATH can miss.
func systemBinDirs() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"}
}

// extraBinDirs are the per-user install places.
func extraBinDirs(home, name string) []string {
	var dirs []string
	if runtime.GOOS == "windows" {
		if home != "" {
			dirs = append(dirs,
				filepath.Join(home, ".local", "bin"),
				filepath.Join(home, "."+name, "bin"),
				filepath.Join(home, "AppData", "Roaming", "npm"),
			)
		}
		return dirs
	}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, "."+name, "bin"),
		)
	}
	return dirs
}

// binNames are the file names a harness can have in a directory. On Windows
// it is an .exe, or a .cmd shim when npm installed it.
func binNames(name string) []string {
	if runtime.GOOS == "windows" {
		return []string{name + ".exe", name + ".cmd", name + ".bat"}
	}
	return []string{name}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode()&0o111 != 0
}

// Find reports whether a harness binary is installed.
func (e Env) Find(name string) bool {
	_, ok := e.find(name)
	return ok
}
