// Package schedule registers the collector with the system scheduler: the
// user's crontab on Linux, a launch agent on macOS, Task Scheduler on Windows.
package schedule

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Marker ends the crontab line and names the Windows task.
const Marker = "ai-usage"

// Interval is how often the scheduler runs the collector.
const Interval = 15 * time.Minute

// State is what the scheduler holds for the collector.
type State int

const (
	// Absent means there is no entry.
	Absent State = iota
	// Other means an entry runs another binary or another state folder.
	Other
	// Disabled means the person commented the entry out or disabled the
	// task or the launch agent. Runs leave it alone; Install turns it back
	// on.
	Disabled
	// Active means the entry runs this binary with this state folder.
	Active
)

// Runner runs a scheduler command. Tests replace it.
type Runner func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error)

// Scheduler talks to the OS scheduler.
type Scheduler struct {
	GOOS string
	Run  Runner
	// AgentDir holds the macOS launch agent, and UID names the login
	// session launchd runs it in. InAgent says this process is a run the
	// launch agent started.
	AgentDir string
	UID      int
	InAgent  bool
}

// Default uses the real OS and commands.
func Default() Scheduler {
	s := Scheduler{GOOS: runtime.GOOS, Run: execRunner, UID: os.Getuid(), InAgent: os.Getenv("XPC_SERVICE_NAME") == AgentLabel}
	if h, err := os.UserHomeDir(); err == nil {
		s.AgentDir = filepath.Join(h, "Library", "LaunchAgents")
	}
	return s
}

// Name is the system scheduler Install registers with, as a person knows it.
func (s Scheduler) Name() string {
	switch s.GOOS {
	case "darwin":
		return "launchd"
	case "windows":
		return "Task Scheduler"
	}
	return "cron"
}

func execRunner(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			line, _, _ := strings.Cut(msg, "\n")
			return out, fmt.Errorf("%s: %s", name, line)
		}
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// Install registers exe to collect into the state folder home. It replaces
// an earlier registration, including one the person disabled.
func (s Scheduler) Install(ctx context.Context, exe, home, path string) error {
	if strings.ContainsAny(exe+home+path, "\r\n") {
		// A line break would end the entry and start another one.
		return errors.New("cannot schedule a binary path, state folder, or PATH that contains a line break")
	}
	switch s.GOOS {
	case "darwin":
		return s.installAgent(ctx, exe, home, path)
	case "windows":
		return s.installTask(ctx, exe, home)
	}
	return s.installCron(ctx, exe, home, path)
}

// Remove deletes the registration. Removing an absent one is not an error.
func (s Scheduler) Remove(ctx context.Context) error {
	switch s.GOOS {
	case "darwin":
		return s.removeAgent(ctx)
	case "windows":
		return s.removeTask(ctx)
	}
	return s.removeCron(ctx)
}

// Lookup reports whether the scheduler runs exe with the state folder home.
func (s Scheduler) Lookup(ctx context.Context, exe, home string) (State, error) {
	switch s.GOOS {
	case "darwin":
		return s.lookupAgent(ctx, exe, home)
	case "windows":
		return s.lookupTask(ctx, exe, home)
	}
	return s.lookupCron(ctx, exe, home)
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
