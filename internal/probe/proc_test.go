package probe

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A bug in the goroutine that waits for app-server is the probe's error. A
// nil command panics there as such a bug would.
func TestWaitOrKillPanicIsItsError(t *testing.T) {
	if msg := errText(waitOrKill(nil, time.Minute)); msg == "" || strings.Contains(msg, "\n") {
		t.Errorf("error = %q", msg)
	}
}

func TestLastLineSaysTheError(t *testing.T) {
	for in, want := range map[string]string{
		"node:internal/modules/cjs/loader:1228\n  throw err;\n  ^\n\nError: Cannot find module '/x'\n    at Module._resolveFilename (node:internal)\n\nNode.js v22.1.0\n": "Error: Cannot find module '/x'",
		"it broke\n\nFor more information, try '--help'.\n": "it broke",
		"\x1b[2mlast words\x1b[0m\r\n\n":                    "last words",
		"":                                                  "",
		"thread 'main' panicked at src/main.rs:5:5:\nfailed to load config\nnote: run with `RUST_BACKTRACE=1` environment variable to display a backtrace\n": "failed to load config",
		"thread 'main' panicked at 'old style', src/main.rs:5:5\nnote: run with `RUST_BACKTRACE=1` environment variable to display a backtrace\n":            "thread 'main' panicked at 'old style', src/main.rs:5:5",
		"TypeError: foo is not a function\n    at ModuleJob.run (node:internal/modules/esm/module_job:195:25)\n\nNode.js v20.11.0\n":                         "TypeError: foo is not a function",
		"'node' is not recognized as an internal or external command,\r\noperable program or batch file.\r\n":                                                "'node' is not recognized as an internal or external command, operable program or batch file.",
	} {
		var l lastLine
		_, _ = l.Write([]byte(in))
		if got := l.String(); got != want {
			t.Errorf("lastLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// A command that failed says why in the last line it printed on its errors,
// cut short, after its exit status.
func TestShortErrSaysWhy(t *testing.T) {
	err := fmt.Errorf("auth: %w", &exec.ExitError{Stderr: []byte("line1\nfatal: token expired\n")})
	if got := shortErr(err); !strings.HasPrefix(got, "exit status ") || !strings.HasSuffix(got, ": fatal: token expired") {
		t.Errorf("got %q", got)
	}
	long := &exec.ExitError{Stderr: []byte("error: " + strings.Repeat("é", 80) + "\n")}
	if got := shortErr(long); len(got) > len("exit status -1: ")+maxSaid || strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("got %q, %d bytes", got, len(got))
	}
	if got := shortErr(&exec.ExitError{}); got != "exit status -1" {
		t.Errorf("got %q", got)
	}
	if got := shortErr(errors.New("plain")); got != "plain" {
		t.Errorf("got %q", got)
	}
}
