package probe

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// lastLine keeps the last line written to it, such as the error a program
// printed before it exited, without its terminal colors and cut short.
type lastLine struct {
	mu   sync.Mutex
	tail []byte
}

func (l *lastLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tail = append(l.tail, p...)
	if len(l.tail) > 4096 {
		l.tail = append([]byte(nil), l.tail[len(l.tail)-4096:]...)
	}
	return len(p), nil
}

var terminalCodes = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// String is the line of the lines kept that errorOf picks.
func (l *lastLine) String() string { return errorOf(l.lines()) }

// lines are the lines kept, without terminal colors, control or hidden
// characters, or blank lines.
func (l *lastLine) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return printedLines(string(l.tail))
}

// printedLines are the lines of what a program printed, without terminal
// colors, control or hidden characters, or blank lines.
func printedLines(s string) []string {
	var lines []string
	for line := range strings.SplitSeq(terminalCodes.ReplaceAllString(s, ""), "\n") {
		line = strings.TrimSpace(snapshot.Printable(line))
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// errorOf is the last line that says it is an error, or else the last line
// that is not a hint, a stack frame, or a runtime's version, as a command-line
// parser, Node, or a Rust panic prints after the error. A line cut at a comma
// is joined with the one after it.
func errorOf(lines []string) string {
	for i, line := range slices.Backward(lines) {
		if !errorLine.MatchString(line) {
			continue
		}
		// A panic since Rust 1.73 says where on one line and why on the next.
		if strings.Contains(line, "panicked at") && strings.HasSuffix(line, ":") && i+1 < len(lines) && !hint(lines[i+1]) {
			return truncate(lines[i+1], maxErrLine)
		}
		return truncate(line, maxErrLine)
	}
	for i, line := range slices.Backward(lines) {
		if hint(line) {
			continue
		}
		if i > 0 && strings.HasSuffix(lines[i-1], ",") {
			return truncate(lines[i-1]+" "+line, maxErrLine)
		}
		return truncate(line, maxErrLine)
	}
	return ""
}

// errorLine matches a line that says it is the error: "error: ...", Node's
// "TypeError: ...", or a Rust panic.
var errorLine = regexp.MustCompile(`^(?i:error)|^[A-Za-z]*Error\b|panicked at`)

// hint reports whether a line is one printed after an error rather than the
// error itself.
func hint(line string) bool {
	for _, p := range []string{"For more information", "Usage:", "Node.js v", "at ", "note: "} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// waitOrKill waits for cmd to exit and kills it once grace has passed. Wait
// closes stdout, which ends the reader if it is still reading. How cmd exited
// is not the probe's answer, so the only error is a bug while waiting.
func waitOrKill(cmd *exec.Cmd, grace time.Duration) (err error) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer rescue(&err)
		_ = cmd.Wait()
	}()
	select {
	case <-done:
		return err
	case <-time.After(grace):
	}
	_ = killGroup(cmd)
	<-done
	return err
}

// rescue, deferred in a goroutine a probe starts, turns a panic there into
// *err, so a bug fails that probe rather than ending the process.
func rescue(err *error) {
	if v := recover(); v != nil {
		*err = fmt.Errorf("stopped by a bug: %v", v)
	}
}

// maxErrLine is how many bytes of one error line from a harness the probe
// keeps. The line is cut on a rune boundary and without an ellipsis, so the
// error text the probe stores stays byte-identical.
const maxErrLine = 160

func shortErr(err error) string {
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit status %d", exit.ExitCode())
	}
	return truncate(err.Error(), maxErrLine)
}

// truncate cuts s to at most n bytes without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
