package schedule

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
)

// AgentLabel names the macOS launch agent and its plist.
const AgentLabel = "io.github.neoromantic.ai-usage"

// agentPath ends the launch agent's PATH, so the collector finds the system
// tools even when the installing shell's PATH left them out.
var agentPath = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// AgentPlist is the macOS launch agent: the collector every 15 minutes in the
// person's login session, where it can reach the keychain, as a background
// process whose output goes nowhere. A calendar interval, unlike
// StartInterval, runs once on wake for the runs missed during sleep. launchd
// never starts a second copy while one runs. PATH is captured from the
// installing shell, as in the crontab line.
func AgentPlist(exe, home, path string) string {
	dirs := pathDirs(path)
	for _, d := range agentPath {
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + AgentLabel + `</string>
` + programArguments(exe, home) + `  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>` + xmlText(strings.Join(dirs, ":")) + `</string>
  </dict>
  <key>StartCalendarInterval</key>
  <array>
` + quarterHours() + `  </array>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>/dev/null</string>
  <key>StandardErrorPath</key>
  <string>/dev/null</string>
</dict>
</plist>
`
}

// quarterHours are the minutes of each hour the agent runs at.
func quarterHours() string {
	var b strings.Builder
	for m := 0; m < 60; m += int(Interval / time.Minute) {
		b.WriteString("    <dict>\n      <key>Minute</key>\n      <integer>" + strconv.Itoa(m) + "</integer>\n    </dict>\n")
	}
	return b.String()
}

// programArguments is the plist's command. Lookup matches it to tell this
// binary and state folder from another.
func programArguments(exe, home string) string {
	argv := []string{exe, "collect", "--quiet", "--home", home}
	var b strings.Builder
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range argv {
		b.WriteString("    <string>" + xmlText(a) + "</string>\n")
	}
	b.WriteString("  </array>\n")
	return b.String()
}

func (s Scheduler) plist() (string, error) {
	if s.AgentDir == "" {
		return "", errors.New("cannot find the LaunchAgents folder: the home folder is unknown")
	}
	return filepath.Join(s.AgentDir, AgentLabel+".plist"), nil
}

// domain is the person's login session. launchd has no session to run the
// agent in until someone logs in at the screen.
func (s Scheduler) domain() string { return "gui/" + strconv.Itoa(s.UID) }

func (s Scheduler) service() string { return s.domain() + "/" + AgentLabel }

func (s Scheduler) launchctl(ctx context.Context, args ...string) ([]byte, error) {
	return s.Run(ctx, "launchctl", args, nil)
}

// installAgent writes the plist and loads it in place of an earlier one.
func (s Scheduler) installAgent(ctx context.Context, exe, home, path string) error {
	file, err := s.plist()
	if err != nil {
		return err
	}
	if _, err := s.launchctl(ctx, "print", s.domain()); err != nil {
		return fmt.Errorf("no login session to run the launch agent in; log in at the screen, then run `ai-usage schedule install`: %w", err)
	}
	if err := os.MkdirAll(s.AgentDir, 0o755); err != nil {
		return err
	}
	// Replaced in one step, so launchd never loads half of it. launchd
	// refuses a plist that others can write.
	if err := fsutil.WriteFile(file, []byte(AgentPlist(exe, home, path)), 0o644); err != nil {
		return err
	}
	if s.InAgent {
		// Booting the agent out would stop this very run before it loaded
		// the agent again. launchd already runs this binary, and reads the
		// new plist the next time it loads the agent.
		return nil
	}
	// A loaded agent keeps its old definition until it is booted out.
	if err := s.bootout(ctx); err != nil {
		return err
	}
	if _, err := s.launchctl(ctx, "enable", s.service()); err != nil {
		return err
	}
	_, err = s.launchctl(ctx, "bootstrap", s.domain(), file)
	return err
}

// removeAgent boots the agent out and removes the plist.
func (s Scheduler) removeAgent(ctx context.Context) error {
	file, err := s.plist()
	if err != nil {
		return err
	}
	if err := s.bootout(ctx); err != nil {
		return err
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// settle is how long bootout waits between checks that the agent is gone.
var settle = 100 * time.Millisecond

// bootout unloads the agent if it is loaded, and waits until launchd has
// stopped it, since bootstrapping an agent still being torn down fails.
func (s Scheduler) bootout(ctx context.Context) error {
	if !s.loaded(ctx) {
		return nil
	}
	if _, err := s.launchctl(ctx, "bootout", s.service()); err != nil {
		return err
	}
	for range 50 {
		if !s.loaded(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(settle):
		}
	}
	return errors.New("launchctl: the launch agent did not stop within 5 seconds")
}

// lookupAgent reads the plist and asks launchd whether it is loaded. A plist
// launchd has not loaded counts as absent, so the next run loads it.
func (s Scheduler) lookupAgent(ctx context.Context, exe, home string) (State, error) {
	file, err := s.plist()
	if err != nil {
		return Absent, err
	}
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return Absent, nil
	}
	if err != nil {
		return Absent, err
	}
	switch {
	case s.disabled(ctx):
		return Disabled, nil
	case !strings.Contains(string(b), programArguments(exe, home)):
		return Other, nil
	case !s.loaded(ctx):
		return Absent, nil
	}
	return Active, nil
}

func (s Scheduler) loaded(ctx context.Context) bool {
	_, err := s.launchctl(ctx, "print", s.service())
	return err == nil
}

// disabled reports whether the person ran launchctl disable on the agent.
// Newer macOS prints "disabled", older "true".
func (s Scheduler) disabled(ctx context.Context) bool {
	out, err := s.launchctl(ctx, "print-disabled", s.domain())
	if err != nil {
		return false
	}
	for l := range strings.SplitSeq(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), " => ")
		if ok && k == `"`+AgentLabel+`"` {
			return v == "disabled" || v == "true"
		}
	}
	return false
}
