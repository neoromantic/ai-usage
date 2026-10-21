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

// agentArgs is the command the launch agent runs.
func agentArgs(exe, home string) []string {
	return []string{exe, "collect", "--quiet", "--home", home}
}

// programArguments is the plist's command. Lookup matches it to tell this
// binary and state folder from another.
func programArguments(exe, home string) string {
	var b strings.Builder
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range agentArgs(exe, home) {
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

// ErrRefused is Repair's answer when launchd still refuses to start the
// launch agent.
var ErrRefused = errors.New("launchd refuses to start the updated binary")

// repairAgent has launchd start the agent now, from the binary on disk. On
// macOS 27, launchd pins an agent it loaded at login to its binary's code
// signature, which for an ad-hoc signed binary is its cdhash. A Developer ID
// release is pinned to its team and identifier, which updates keep; the
// update from an ad-hoc release, or of a build from source, still changes
// the pin. Once such an update replaces the binary, launchd refuses the next
// start, and at the start after it repairs the pin and records the new
// binary for the next login. So a refused agent takes one start. A pinned one whose binary replaced says was
// just replaced takes two, and the first, refused at once, costs no run;
// when launchd does not refuse it in time, there is no second. An agent
// pinned to nothing, as on an earlier macOS, and one that runs or is being
// started are left alone. The started run collects as a scheduled run does.
// A run the agent started does nothing, since launchd does not start the
// agent while it runs.
func (s Scheduler) repairAgent(ctx context.Context, replaced bool) error {
	if s.InAgent {
		return nil
	}
	out, err := s.launchctl(ctx, "print", s.service())
	if err != nil {
		return nil
	}
	p := readPrint(out)
	if !p.idle || !p.refused && !(replaced && p.pinned) {
		return nil
	}
	if !p.refused {
		if p, err = s.start(ctx, p.runs); err != nil || !p.refused {
			return err
		}
	}
	if p, err = s.start(ctx, p.runs); err == nil && p.refused {
		return ErrRefused
	}
	return err
}

// start has launchd start the agent, and waits up to 5 seconds for the try:
// for the agent to run, or to have stopped with the outcome printed. It
// returns what launchctl print says then, or nothing if it did not get
// there.
func (s Scheduler) start(ctx context.Context, runs int) (agentPrint, error) {
	if _, err := s.launchctl(ctx, "kickstart", s.service()); err != nil {
		return agentPrint{}, err
	}
	for range 50 {
		out, err := s.launchctl(ctx, "print", s.service())
		if err != nil {
			return agentPrint{}, nil
		}
		if p := readPrint(out); p.runs > runs && (p.running || p.idle && p.ended) {
			return p, nil
		}
		select {
		case <-ctx.Done():
			return agentPrint{}, ctx.Err()
		case <-time.After(settle):
		}
	}
	return agentPrint{}, nil
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

// lookupAgent reads the plist and asks launchd whether it is loaded, and
// with which command: launchd keeps the definition it loaded, whatever the
// plist says since, until the agent is loaded again. A run the agent started
// was not refused, whatever launchd says of the run before.
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
	}
	out, err := s.launchctl(ctx, "print", s.service())
	switch {
	case err != nil && !s.session(ctx):
		// With nobody logged in at the screen there is no domain to load
		// the agent in, and Install says so.
		return Absent, nil
	case err != nil:
		return Unloaded, nil
	case !loadedRuns(out, agentArgs(exe, home)):
		return Stale, nil
	case !s.InAgent && readPrint(out).refused:
		return Refused, nil
	}
	return Active, nil
}

// session reports whether launchd has the login session's domain, which a
// person gets by logging in at the screen.
func (s Scheduler) session(ctx context.Context) bool {
	_, err := s.launchctl(ctx, "print", s.domain())
	return err == nil
}

func (s Scheduler) loaded(ctx context.Context) bool {
	_, err := s.launchctl(ctx, "print", s.service())
	return err == nil
}

// agentPrint is what launchctl print says of the agent: how many times
// launchd started it or tried to; whether it is not running, or runs, and
// not on its way between the two, as while launchd spawns it; whether the
// outcome of the last try is printed yet; whether launchd pinned it to its
// binary's code signature; and whether launchd refused to start it at its
// last try for that pin, as once an update replaced the binary.
type agentPrint struct {
	runs                 int
	idle, running, ended bool
	pinned, refused      bool
}

// readPrint reads the agent's own lines, which launchctl indents once. A
// refused agent waits for launchd to update its pin, and prints the refusal
// as its last exit's reason, and a moment later also as a job state of spawn
// failed.
func readPrint(print []byte) agentPrint {
	var p agentPrint
	repin, refusal := false, false
	for l := range strings.SplitSeq(string(print), "\n") {
		if !strings.HasPrefix(l, "\t") || strings.HasPrefix(l, "\t\t") {
			continue
		}
		k, v, _ := strings.Cut(strings.TrimSpace(l), " = ")
		switch k {
		case "runs":
			p.runs, _ = strconv.Atoi(v)
		case "state":
			p.idle, p.running = v == "not running", v == "running"
		case "job state":
			p.ended = true
			refusal = refusal || v == "spawn failed"
		case "last exit reason":
			p.ended = true
			refusal = refusal || strings.Contains(v, "OS_REASON_CODESIGNING")
		case "last exit code":
			p.ended = p.ended || v != "(never exited)"
		case "properties":
			props := strings.Split(v, " | ")
			p.pinned, repin = slices.Contains(props, "has LWCR"), slices.Contains(props, "needs LWCR update")
		}
	}
	p.refused = p.idle && p.pinned && repin && refusal
	return p
}

// loadedRuns reports whether the agent launchctl print shows runs argv. It
// prints each argument on a line of its own, indented twice, between
// "arguments = {" and "}". A print without them says nothing against argv.
func loadedRuns(print []byte, argv []string) bool {
	var args []string
	in, found := false, false
	for l := range strings.SplitSeq(string(print), "\n") {
		switch {
		case l == "\targuments = {":
			in, found = true, true
		case in && l == "\t}":
			in = false
		case in:
			args = append(args, strings.TrimPrefix(l, "\t\t"))
		}
	}
	return !found || slices.Equal(args, argv)
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
